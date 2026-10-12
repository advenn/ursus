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

// valueSpill is where a quantile's values go past its group-by's budget (v0.6.3 step
// 220): kernel.ValueSpill over run files in the group-by's spill directory.
//
// The kernel puts each run already in order, a group at a time and each group's
// values sorted, so a run is written as it comes, and read back through the external
// sort's merge. That merge orders a float by OrderKeyF64, the quantile's own order:
// NaN last, and −0 and +0 alike.
//
// # Runs are merged as they gather
//
// A run is what the accumulator held when its sink was over the budget, so it is
// about a budget of values, as an external sort's runs are. But nothing makes it
// large: a keyless aggregate spills whenever the query is over, and another operator
// can keep it over, so a run can be one batch. The final merge holds a batch of every
// run, so runs are merged valueFanIn at a time as they gather, a level at a time:
// the merge at the end reads at most valueFanIn runs a level, and each value is
// rewritten once a level.
type valueSpill struct {
	ctx    context.Context // the sink's, set before each use: the kernel's interface carries none
	budget *execopt.Budget
	batch  int
	prefix string // a run is prefix + its number + ".ursspill"
	n      int    // runs named so far

	w      *spill.Writer // the open run, and its path
	path   string
	levels [][]string // closed runs, by how many merges made them
}

// valueFanIn is how many runs of one level are merged into one of the next.
const valueFanIn = 16

var (
	valueSchema = dtype.MustSchema(dtype.NotNull("gid", dtype.Int32),
		dtype.NotNull("v", dtype.Float64))
	valueKeys = []plan.SortKey{{Expr: &expr.Col{Name: "gid"}}, {Expr: &expr.Col{Name: "v"}}}
)

func (s *valueSpill) Put(gids []int32, vals []float64) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.w == nil {
		path := s.next()
		w, err := spill.Create(path, valueSchema)
		if err != nil {
			return err
		}
		s.w, s.path = w, path
	}
	b := data.NewBatchRows(valueSchema, []*data.Column{
		data.NewFixed("gid", dtype.Int32, gids, bitmap.View{}),
		data.NewFixed("v", dtype.Float64, vals, bitmap.View{}),
	}, len(gids))
	if err := s.w.Write(b); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "group_by", "writing spill run %s", s.path)
	}
	return nil
}

func (s *valueSpill) EndRun() error {
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
// holds valueFanIn.
func (s *valueSpill) add(level int, path string) error {
	for len(s.levels) <= level {
		s.levels = append(s.levels, nil)
	}
	s.levels[level] = append(s.levels[level], path)
	if len(s.levels[level]) < valueFanIn {
		return nil
	}
	runs := s.levels[level]
	s.levels[level] = nil
	out := s.next()
	w, err := spill.Create(out, valueSchema)
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

func (s *valueSpill) Ordered(fn func(gid int32, v float64) error) error {
	if err := s.EndRun(); err != nil {
		return err
	}
	var runs []string
	for _, l := range s.levels {
		runs = append(runs, l...)
	}
	s.levels = nil
	err := s.merged(runs, func(b *data.Batch) error {
		gids, vals := data.MustValues[int32](b.Column(0)), data.MustValues[float64](b.Column(1))
		for i, g := range gids {
			if err := fn(g, vals[i]); err != nil {
				return err
			}
		}
		return nil
	})
	return errors.Join(err, removeAll(runs))
}

// merged gives fn the runs' values a batch at a time, in order of (gid, v).
func (s *valueSpill) merged(runs []string, fn func(*data.Batch) error) (err error) {
	kSch, err := keySchema(valueKeys, valueSchema)
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
	rs, err := kernel.NewRunSet(len(srcs), []dtype.DataType{dtype.Int32, dtype.Float64},
		make([]kernel.SortSpec, 2))
	if err != nil {
		return err
	}
	m := &mergeOperator{
		schema: valueSchema, keys: valueKeys, kSch: kSch, batch: s.chunk(),
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
func (s *valueSpill) close() error {
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

func (s *valueSpill) next() string {
	s.n++
	return s.prefix + itoa(s.n-1) + ".ursspill"
}

func (s *valueSpill) chunk() int {
	if s.batch <= 0 {
		return DefaultOptions().BatchSize
	}
	return s.batch
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
