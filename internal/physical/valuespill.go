package physical

import (
	"context"
	"errors"
	"io"
	"os"

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
// sorted runs of a group a row: a quantile's values (v0.6.3 step 220, valueSpill) and
// n_unique's keys (v0.6.4 step 241, keySpill), over run files in the group-by's spill
// directory.
//
// The kernel puts each run already in order, a group at a time, so a run is written
// as it comes, and read back through the external sort's merge on (gid, the second
// column). That merge orders a float by OrderKeyF64, the quantile's own order: NaN
// last, and −0 and +0 alike; and bytes as bytes, which is all n_unique's count needs.
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
	keys   []plan.SortKey   // the merge's: gid, then the state
	types  []dtype.DataType // and their types

	w      *spill.Writer // the open run, and its path
	path   string
	levels [][]string // closed runs, by how many merges made them
}

// runFanIn is how many runs of one level are merged into one of the next.
const runFanIn = 16

// groupSpill is a spill a group-by's sink keeps for an accumulator.
type groupSpill interface {
	setCtx(context.Context)
	close() error
}

func newGroupRuns(ctx context.Context, budget *execopt.Budget, batch int, prefix string,
	state dtype.Field) groupRuns {

	sch := dtype.MustSchema(dtype.NotNull("gid", dtype.Int32), state)
	return groupRuns{ctx: ctx, budget: budget, batch: batch, prefix: prefix, schema: sch,
		keys:  []plan.SortKey{{Expr: &expr.Col{Name: "gid"}}, {Expr: &expr.Col{Name: state.Name}}},
		types: []dtype.DataType{dtype.Int32, state.Type}}
}

func (s *groupRuns) setCtx(ctx context.Context) { s.ctx = ctx }

// write adds b to the open run, opening one if none is.
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
	if err := s.w.Write(b); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "group_by", "writing spill run %s", s.path)
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
