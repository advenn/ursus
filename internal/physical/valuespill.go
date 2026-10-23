package physical

import (
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// groupRuns is where a group-by's accumulator puts its state past the budget, in
// sorted runs of a group a row: a quantile's values (v0.6.3 step 220, valueSpill),
// n_unique's keys (v0.6.4 step 241, keySpill), a Spearman's pairs (step 339,
// pairSpill) and a mode's entries (step 340, modeSpill), over run files in the
// group-by's spill directory.
//
// The kernel puts each run already in order, a group at a time, so a run is written
// as it comes, and read back through the external sort's merge on (gid, the second
// column), any later column carried. That merge orders a float by OrderKeyF64, the
// quantile's own order: NaN last, and −0 and +0 alike; and bytes as bytes, which is
// all n_unique's count needs.
//
// # Runs are merged as they gather
//
// A run is what the accumulator held when its sink was over the budget, so it is
// about a budget of state, as an external sort's runs are. But nothing makes it
// large: a keyless aggregate spills whenever the query is over, and another operator
// can keep it over, so a run can be one batch. The final merge holds a batch of every
// run, so runs are merged runFanIn at a time as they gather, a level at a time: the
// merge at the end reads at most runFanIn runs a level, and each row is rewritten
// once a level.
type groupRuns struct {
	ctx    context.Context // the sink's, set before each use: the kernel's interface carries none
	budget *execopt.Budget
	batch  int
	prefix string // a run is prefix + its number + ".ursspill"
	n      int    // runs named so far

	schema *dtype.Schema    // gid, then the state
	keys   []plan.SortKey   // the merge's: gid, then the state's first column
	types  []dtype.DataType // and their types

	w      *spill.Writer // the open run, and its path
	path   string
	levels [][]string // closed runs, by how many merges made them
}

// runFanIn is how many runs of one level are merged into one of the next.
const runFanIn = 16

// groupSpill is a spill a sink keeps for an accumulator: a group-by's, or a
// window's over one partition (step 242).
type groupSpill interface {
	setCtx(context.Context)
	// take hands every value acc holds to the spill, as one run, and lets them go.
	take(acc kernel.Accumulator) error
	close() error
}

// spillable reports whether acc keeps its state a value at a time, so a groupSpill
// can take it: a quantile's values, n_unique's keys, a Spearman's pairs or a mode's
// entries.
func spillable(acc kernel.Accumulator) bool {
	switch acc.(type) {
	case kernel.ValueSpiller, kernel.KeySpiller, kernel.PairSpiller, kernel.ModeSpiller:
		return true
	}
	return false
}

// spillState hands the state of every spillable accumulator of accs to its spill,
// made the first time under the prefix prefix(i) gives; *spills is made when the
// first is. A nil accumulator is passed over.
func spillState(ctx context.Context, accs []kernel.Accumulator, spills *[]groupSpill,
	budget *execopt.Budget, batch int, prefix func(i int) (string, error)) error {

	for i, a := range accs {
		if !spillable(a) {
			continue
		}
		if *spills == nil {
			*spills = make([]groupSpill, len(accs))
		}
		sp := (*spills)[i]
		if sp == nil {
			p, err := prefix(i)
			if err != nil {
				return err
			}
			switch a.(type) {
			case kernel.KeySpiller:
				sp = newKeySpill(ctx, budget, batch, p)
			case kernel.PairSpiller:
				sp = newPairSpill(ctx, budget, batch, p)
			case kernel.ModeSpiller:
				sp = newModeSpill(ctx, budget, batch, p)
			default:
				sp = newValueSpill(ctx, budget, batch, p)
			}
			(*spills)[i] = sp
		}
		sp.setCtx(ctx)
		if err := sp.take(a); err != nil {
			return err
		}
	}
	return nil
}

// setSpillsCtx gives every spill ctx, for a Finish that reads its values back.
func setSpillsCtx(ctx context.Context, spills []groupSpill) {
	for _, sp := range spills {
		if sp != nil {
			sp.setCtx(ctx)
		}
	}
}

// closeSpills lets go of every spill's runs.
func closeSpills(spills []groupSpill) error {
	var errs []error
	for _, sp := range spills {
		if sp != nil {
			errs = append(errs, sp.close())
		}
	}
	return errors.Join(errs...)
}

func newGroupRuns(ctx context.Context, budget *execopt.Budget, batch int, prefix string,
	state ...dtype.Field) groupRuns {

	sch := dtype.MustSchema(append([]dtype.Field{dtype.NotNull("gid", dtype.Int32)}, state...)...)
	return groupRuns{ctx: ctx, budget: budget, batch: batch, prefix: prefix, schema: sch,
		keys:  []plan.SortKey{{Expr: &expr.Col{Name: "gid"}}, {Expr: &expr.Col{Name: state[0].Name}}},
		types: []dtype.DataType{dtype.Int32, state[0].Type}}
}

func (s *groupRuns) setCtx(ctx context.Context) { s.ctx = ctx }

// write adds b to the open run, opening one if none is, a batch at a time: the
// merge holds a block of every run it reads, and the kernel puts its values in
// chunks of its own size, whatever the query's batch.
func (s *groupRuns) write(b *data.Batch) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.w == nil {
		path := s.next()
		w, err := spill.Create(path, s.schema)
		if err != nil {
			return err
		}
		s.w, s.path = w, path
	}
	step := s.chunk()
	for off := 0; off < b.Rows(); off += step {
		if err := s.w.Write(b.Slice(off, min(step, b.Rows()-off))); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "group_by", "writing spill run %s", s.path)
		}
	}
	return nil
}

func (s *groupRuns) EndRun() error {
	if s.w == nil {
		return nil
	}
	w := s.w
	s.w = nil
	if err := w.Close(); err != nil {
		return err
	}
	s.budget.NoteSpill()
	return s.add(0, s.path)
}

// add files a run at a level, and merges the level into one run of the next once it
// holds runFanIn.
func (s *groupRuns) add(level int, path string) error {
	for len(s.levels) <= level {
		s.levels = append(s.levels, nil)
	}
	s.levels[level] = append(s.levels[level], path)
	if len(s.levels[level]) < runFanIn {
		return nil
	}
	runs := s.levels[level]
	s.levels[level] = nil
	out := s.next()
	w, err := spill.Create(out, s.schema)
	if err != nil {
		return errors.Join(err, removeAll(runs))
	}
	err = s.merged(runs, func(b *data.Batch) error {
		if err := w.Write(b); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "group_by", "writing spill run %s", out)
		}
		return nil
	})
	if err = errors.Join(err, w.Close(), removeAll(runs)); err != nil {
		return errors.Join(err, removeAll([]string{out}))
	}
	return s.add(level+1, out)
}

// kept ends the open run and gives fn every row put, merged across the runs, a batch
// at a time, and keeps the runs for another read: close removes them.
func (s *groupRuns) kept(fn func(*data.Batch) error) error {
	if err := s.EndRun(); err != nil {
		return err
	}
	var runs []string
	for _, l := range s.levels {
		runs = append(runs, l...)
	}
	return s.merged(runs, fn)
}

// each ends the open run and gives fn every row put, merged across the runs, a batch
// at a time; then it removes the runs.
func (s *groupRuns) each(fn func(*data.Batch) error) error {
	if err := s.EndRun(); err != nil {
		return err
	}
	var runs []string
	for _, l := range s.levels {
		runs = append(runs, l...)
	}
	s.levels = nil
	return errors.Join(s.merged(runs, fn), removeAll(runs))
}

// merged gives fn the runs' rows a batch at a time, in order of (gid, state).
func (s *groupRuns) merged(runs []string, fn func(*data.Batch) error) (err error) {
	kSch, err := keySchema(s.keys, s.schema)
	if err != nil {
		return err
	}
	srcs := make([]runSource, 0, len(runs))
	defer func() {
		for _, r := range srcs {
			err = errors.Join(err, r.Close())
		}
	}()
	for _, p := range runs {
		r, err := spill.Open(p)
		if err != nil {
			return err
		}
		srcs = append(srcs, &fileRun{r: r})
	}
	rs, err := kernel.NewRunSet(len(srcs), s.types, make([]kernel.SortSpec, 2))
	if err != nil {
		return err
	}
	m := &mergeOperator{
		schema: s.schema, keys: s.keys, kSch: kSch, batch: s.chunk(),
		runs: srcs, rs: rs, m: kernel.NewMerger(rs), mem: s.budget.Account("group_by"),
		cur: make([]*data.Batch, len(srcs)), pos: make([]int, len(srcs)), rows: make([]int, len(srcs)),
	}
	defer m.mem.Release()
	for {
		b, err := m.Next(s.ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(b); err != nil {
			return err
		}
	}
}

// close lets go of every run, for a sink closed before its answer.
func (s *groupRuns) close() error {
	var err error
	if s.w != nil {
		err = s.w.Close()
		s.w = nil
		s.levels = append(s.levels, []string{s.path})
	}
	for _, l := range s.levels {
		err = errors.Join(err, removeAll(l))
	}
	s.levels = nil
	return err
}

func (s *groupRuns) next() string {
	s.n++
	return s.prefix + itoa(s.n-1) + ".ursspill"
}

func (s *groupRuns) chunk() int {
	if s.batch <= 0 {
		return DefaultOptions().BatchSize
	}
	return s.batch
}

// valueSpill is kernel.ValueSpill: a quantile's values, a Float64 a row.
type valueSpill struct{ groupRuns }

func newValueSpill(ctx context.Context, budget *execopt.Budget, batch int, prefix string) *valueSpill {
	return &valueSpill{newGroupRuns(ctx, budget, batch, prefix, dtype.NotNull("v", dtype.Float64))}
}

func (s *valueSpill) take(acc kernel.Accumulator) error {
	_, err := acc.(kernel.ValueSpiller).SpillValues(s)
	return err
}

func (s *valueSpill) Put(gids []int32, vals []float64) error {
	return s.write(data.NewBatchRows(s.schema, []*data.Column{
		data.NewFixed("gid", dtype.Int32, gids, bitmap.View{}),
		data.NewFixed("v", dtype.Float64, vals, bitmap.View{}),
	}, len(gids)))
}

func (s *valueSpill) Ordered(fn func(gid int32, v float64) error) error {
	return s.each(func(b *data.Batch) error {
		gids, vals := data.MustValues[int32](b.Column(0)), data.MustValues[float64](b.Column(1))
		for i, g := range gids {
			if err := fn(g, vals[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// keySpill is kernel.KeySpill: n_unique's keys, the group-key encoder's bytes a row.
type keySpill struct{ groupRuns }

func newKeySpill(ctx context.Context, budget *execopt.Budget, batch int, prefix string) *keySpill {
	return &keySpill{newGroupRuns(ctx, budget, batch, prefix, dtype.NotNull("k", dtype.Binary))}
}

func (s *keySpill) take(acc kernel.Accumulator) error {
	_, err := acc.(kernel.KeySpiller).SpillKeys(s)
	return err
}

func (s *keySpill) Put(gids []int32, keys [][]byte) error {
	offs := make([]int32, len(keys)+1)
	n := 0
	for _, k := range keys {
		n += len(k)
	}
	chars := make([]byte, 0, n)
	for i, k := range keys {
		chars = append(chars, k...)
		offs[i+1] = int32(len(chars))
	}
	return s.write(data.NewBatchRows(s.schema, []*data.Column{
		data.NewFixed("gid", dtype.Int32, gids, bitmap.View{}),
		data.NewStringParts("k", offs, chars, bitmap.View{}).WithDType(dtype.Binary),
	}, len(gids)))
}

func (s *keySpill) Ordered(fn func(gid int32, key []byte) error) error {
	return s.each(func(b *data.Batch) error {
		gids, keys := data.MustValues[int32](b.Column(0)), b.Column(1).Strings()
		for i, g := range gids {
			if err := fn(g, keys.Bytes(i)); err != nil {
				return err
			}
		}
		return nil
	})
}

// pairSpill is kernel.PairSpill: a Spearman's pairs, two Float64s a row, merged by
// the first (step 339). One from Sorting takes its pairs in any order: it holds them
// to a quarter of the limit, sorts them, and writes them as a run.
type pairSpill struct {
	groupRuns
	sorting bool
	held    []heldPair
	most    int // pairs a sorting spill holds before it writes a run
	mem     *execopt.Account
	next    *pairSpill // the spill from Sorting, let go with this one
}

// heldPair is a pair a sorting pairSpill holds, beside its order's key.
type heldPair struct {
	gid  int32
	key  uint64 // kernel.OrderKeyF64(a)
	a, b float64
}

// heldPairBytes is a heldPair's size, padded.
const heldPairBytes = 32

// minSortedRun is the fewest pairs a sorting spill holds before it writes a run,
// however low the limit: a run a batch would be a level of merges a batch.
const minSortedRun = 4096

func newPairSpill(ctx context.Context, budget *execopt.Budget, batch int, prefix string) *pairSpill {
	return &pairSpill{groupRuns: newGroupRuns(ctx, budget, batch, prefix,
		dtype.NotNull("a", dtype.Float64), dtype.NotNull("b", dtype.Float64))}
}

func (s *pairSpill) take(acc kernel.Accumulator) error {
	_, err := acc.(kernel.PairSpiller).SpillPairs(s)
	return err
}

func (s *pairSpill) Put(gids []int32, a, b []float64) error {
	if !s.sorting {
		return s.write(s.batchOf(gids, a, b))
	}
	for i, g := range gids {
		s.held = append(s.held, heldPair{g, kernel.OrderKeyF64(a[i]), a[i], b[i]})
	}
	s.mem.RetainBytes(int64(len(gids)) * heldPairBytes)
	if len(s.held) < s.most {
		return nil
	}
	return s.EndRun()
}

func (s *pairSpill) batchOf(gids []int32, a, b []float64) *data.Batch {
	return data.NewBatchRows(s.schema, []*data.Column{
		data.NewFixed("gid", dtype.Int32, gids, bitmap.View{}),
		data.NewFixed("a", dtype.Float64, a, bitmap.View{}),
		data.NewFixed("b", dtype.Float64, b, bitmap.View{}),
	}, len(gids))
}

// EndRun closes the open run; a sorting spill first sorts what it holds and writes
// it as the run.
func (s *pairSpill) EndRun() error {
	if s.sorting && len(s.held) > 0 {
		slices.SortFunc(s.held, func(x, y heldPair) int {
			if c := cmp.Compare(x.gid, y.gid); c != 0 {
				return c
			}
			return cmp.Compare(x.key, y.key)
		})
		step := s.chunk()
		gids, a, b := make([]int32, 0, step), make([]float64, 0, step), make([]float64, 0, step)
		for off := 0; off < len(s.held); off += step {
			gids, a, b = gids[:0], a[:0], b[:0]
			for _, p := range s.held[off:min(off+step, len(s.held))] {
				gids, a, b = append(gids, p.gid), append(a, p.a), append(b, p.b)
			}
			if err := s.write(s.batchOf(gids, a, b)); err != nil {
				return err
			}
		}
		s.held = s.held[:0]
		s.mem.Release()
	}
	return s.groupRuns.EndRun()
}

func (s *pairSpill) Ordered(fn func(gid int32, a, b float64) error) error {
	if err := s.EndRun(); err != nil {
		return err
	}
	return s.each(func(bt *data.Batch) error {
		gids := data.MustValues[int32](bt.Column(0))
		a, b := data.MustValues[float64](bt.Column(1)), data.MustValues[float64](bt.Column(2))
		for i, g := range gids {
			if err := fn(g, a[i], b[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *pairSpill) Sorting() (kernel.PairSpill, error) {
	if s.next != nil {
		return nil, uerr.Internalf("physical: a pair spill sorted twice")
	}
	s.next = newPairSpill(s.ctx, s.budget, s.batch, s.prefix+"x")
	s.next.sorting = true
	s.next.most = max(int(s.budget.Limit()/4/heldPairBytes), minSortedRun)
	s.next.mem = s.budget.Account("group_by")
	return s.next, nil
}

func (s *pairSpill) setCtx(ctx context.Context) {
	s.groupRuns.setCtx(ctx)
	if s.next != nil {
		s.next.setCtx(ctx)
	}
}

func (s *pairSpill) close() error {
	err := s.groupRuns.close()
	s.held = nil
	s.mem.Release()
	if s.next != nil {
		err = errors.Join(err, s.next.close())
	}
	return err
}

// modeSpill is kernel.ModeSpill: a mode's entries, a key, its rows, its first row
// and its value a row, merged by key (step 340). Its runs are read twice, and let go
// after the second. Its schema waits for the first entries: the value's type is
// theirs.
type modeSpill struct{ groupRuns }

func newModeSpill(ctx context.Context, budget *execopt.Budget, batch int, prefix string) *modeSpill {
	return &modeSpill{groupRuns{ctx: ctx, budget: budget, batch: batch, prefix: prefix}}
}

func (s *modeSpill) take(acc kernel.Accumulator) error {
	_, err := acc.(kernel.ModeSpiller).SpillModes(s)
	return err
}

func (s *modeSpill) Put(gids []int32, keys [][]byte, rows, first []int64, vals *data.Column) error {
	if s.schema == nil {
		r := newGroupRuns(s.ctx, s.budget, s.batch, s.prefix,
			dtype.NotNull("k", dtype.Binary), dtype.NotNull("n", dtype.Int64),
			dtype.NotNull("f", dtype.Int64), dtype.Of("v", vals.DType()))
		s.schema, s.keys, s.types = r.schema, r.keys, r.types
	}
	offs := make([]int32, len(keys)+1)
	n := 0
	for _, k := range keys {
		n += len(k)
	}
	chars := make([]byte, 0, n)
	for i, k := range keys {
		chars = append(chars, k...)
		offs[i+1] = int32(len(chars))
	}
	return s.write(data.NewBatchRows(s.schema, []*data.Column{
		data.NewFixed("gid", dtype.Int32, gids, bitmap.View{}),
		data.NewStringParts("k", offs, chars, bitmap.View{}).WithDType(dtype.Binary),
		data.NewFixed("n", dtype.Int64, rows, bitmap.View{}),
		data.NewFixed("f", dtype.Int64, first, bitmap.View{}),
		vals.Rename("v"),
	}, len(gids)))
}

func (s *modeSpill) Scan(fn func(gids []int32, keys data.StringAccessor, rows, first []int64, vals *data.Column) error) error {
	if s.schema == nil {
		return nil
	}
	return s.kept(modeBatch(fn))
}

func (s *modeSpill) Ordered(fn func(gids []int32, keys data.StringAccessor, rows, first []int64, vals *data.Column) error) error {
	if s.schema == nil {
		return nil
	}
	return s.each(modeBatch(fn))
}

// modeBatch reads a batch of a mode's spill for fn.
func modeBatch(fn func(gids []int32, keys data.StringAccessor, rows, first []int64, vals *data.Column) error) func(*data.Batch) error {
	return func(b *data.Batch) error {
		return fn(data.MustValues[int32](b.Column(0)), b.Column(1).Strings(),
			data.MustValues[int64](b.Column(2)), data.MustValues[int64](b.Column(3)), b.Column(4))
	}
}

// removeAll removes spill files.
func removeAll(paths []string) error {
	var errs []error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, uerr.Wrap(err, uerr.KindIO, "group_by",
				"removing spill run %s", p))
		}
	}
	return errors.Join(errs...)
}
