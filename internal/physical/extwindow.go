package physical

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// A window that spills, and answers exactly as it does in memory.
//
// # Why it can, when Merge cannot
//
// windowSink.Merge refuses because two sinks that saw different rows numbered their
// partitions independently. Spilling never merges sinks: it routes every row by the
// radix of its PARTITION key, so each partition lies wholly in one file, and each
// file gets a fresh sink of its own. A partition's answer depends only on its own
// rows, in their input order, and a file holds them in exactly that order — so an
// ordered function, a first or a last, sees what it would have seen in memory.
//
// # The order, and several partitionings
//
// Each row is stamped with its input ordinal as it is routed. A file's sink emits
// its rows in file order, which is ordinal order, and the files' results are merged
// back on the ordinal, which is the input's order.
//
// `Select(x.Sum().Over(g), y.Max().Over(h))` partitions two ways, and no one routing
// serves both, so it runs one pass per distinct partitioning: route by g, compute
// g's windows per file, merge on the ordinal; route that by h, compute h's windows,
// merge again. Each pass adds its own columns, and the last is trimmed to the
// window's output schema.
//
// # What still fails
//
// A window with no partition keys is one partition, and so is one group larger
// than the limit: routing divides partitions, not the rows of one. Both refuse,
// saying which.

// overBudget starts spilling when the query is over its limit and the window can
// be divided, and refuses, naming why, when it cannot.
func (s *windowSink) overBudget(ctx context.Context) error {
	if !s.mem.Over() {
		return nil
	}
	for _, p := range s.parts {
		if len(p.keys) == 0 {
			return s.refuse("a window with no partition_by is one partition, which partitioning "+
				"to disk cannot divide",
				"give the window a partition_by, or raise the limit with WithMemoryLimit")
		}
	}
	if s.level >= maxSpillDepth || (s.level > 0 && len(s.parts) == 1 && s.parts[0].ids.Len() == 1) {
		return s.refuse("one partition holds more rows than the limit, and partitioning to disk "+
			"divides partitions, not the rows of one",
			"raise the limit with WithMemoryLimit")
	}
	return s.startSpill(ctx)
}

func (s *windowSink) refuse(why, hint string) error {
	return uerr.New(uerr.KindResource, "over", "memory limit of %s exceeded: %s",
		execopt.Bytes(s.budget.Limit()), why).Hint("%s", hint)
}

// startSpill routes every retained row out and drops the in-memory state; from
// here Consume routes each batch as it arrives.
func (s *windowSink) startSpill(ctx context.Context) error {
	if s.budget.Limit() <= 0 {
		return s.mem.Check()
	}
	s.spilling = true
	rows := s.rows
	s.rows, s.specs, s.copy = nil, nil, data.BufferSet{}
	for _, p := range s.parts {
		p.ids, p.perRow = nil, nil
	}
	s.mem.Release()
	s.stateBytes = 0
	s.routeParts, s.pend = make([]*partWriter, nParts), make([][]int32, nParts)
	for _, b := range rows {
		if err := s.route(ctx, b, s.parts[0].keys); err != nil {
			return err
		}
	}
	return nil
}

// passExprs are the window expressions partitioned by s.parts[pass].
//
// newWindowSink builds one spec per expression, in order, so spec i's partitioning
// is expression i's. The specs are dropped on spilling; this reads the mapping off
// the expressions themselves.
func (s *windowSink) passExprs(pass int) []expr.Node {
	want := expr.IdentityAll(s.parts[pass].keys)
	var out []expr.Node
	for _, e := range s.exprs {
		if expr.IdentityAll(e.(*expr.Alias).Child.(*expr.Window).PartitionBy) == want {
			out = append(out, e)
		}
	}
	return out
}

// route writes in's rows to the files of their partition under keys, stamping
// each with its input ordinal if it has none yet.
func (s *windowSink) route(ctx context.Context, in *data.Batch, keys []expr.Node) error {
	src, err := s.withOrdinal(in)
	if err != nil {
		return err
	}
	cols := make([]*data.Column, len(keys))
	for i, k := range keys {
		if cols[i], err = evalColumn(ctx, k, src); err != nil {
			return err
		}
	}
	enc, err := kernel.NewGroupKeyEncoder("over", cols)
	if err != nil {
		return err
	}
	for r := range src.Rows() {
		p := partitionOf(enc.Encode(r), s.level)
		s.pend[p] = append(s.pend[p], int32(r))
	}
	for p, rows := range s.pend {
		if len(rows) == 0 {
			continue
		}
		w, err := s.writer(p, src.Schema())
		if err != nil {
			return err
		}
		b, err := takeBatch(src.Schema(), src, rows)
		if err != nil {
			return err
		}
		if err := w.w.Write(b); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "over", "writing spill partition %s", w.path)
		}
		s.pend[p] = rows[:0]
	}
	return nil
}

// withOrdinal stamps each row with its input position, once: rows a deeper level
// or a later pass reads carry it already.
func (s *windowSink) withOrdinal(in *data.Batch) (*data.Batch, error) {
	if in.Schema().IndexOf(s.ord) >= 0 {
		return in, nil
	}
	n := in.Rows()
	ord := make([]int64, n)
	for i := range ord {
		ord[i] = s.nRows + int64(i)
	}
	s.nRows += int64(n)
	cols := append(append([]*data.Column(nil), in.Columns()...),
		data.NewFixed(s.ord, dtype.Int64, ord, bitmap.AllSet(n)))
	return data.NewBatch(s.spillSchema, cols)
}

// writer opens partition p's file on first use.
func (s *windowSink) writer(p int, sch *dtype.Schema) (*partWriter, error) {
	if w := s.routeParts[p]; w != nil {
		return w, nil
	}
	path, err := s.newFile()
	if err != nil {
		return nil, err
	}
	w, err := spill.CreateSized(path, sch, partBufBytes)
	if err != nil {
		return nil, err
	}
	s.routeParts[p] = &partWriter{w: w, path: path}
	s.mem.RetainBytes(partBufBytes)
	s.budget.NoteSpill()
	return s.routeParts[p], nil
}

// newFile names a fresh file in this sink's own spill directory, creating it first.
func (s *windowSink) newFile() (string, error) {
	if s.dir == "" {
		dir, err := os.MkdirTemp(s.budget.SpillDir(), "ursus-over-")
		if err != nil {
			return "", uerr.Wrap(err, uerr.KindResource, "over", "creating a spill directory").
				Hint("choose a writable location with WithSpillDir")
		}
		s.dir = dir
	}
	s.files++
	return filepath.Join(s.dir, "f"+itoa(s.files)+".ursspill"), nil
}

// closeParts finishes every open partition file and returns them in partition order.
func (s *windowSink) closeParts() ([]string, error) {
	var files []string
	var errs []error
	for p, w := range s.routeParts {
		if w == nil {
			continue
		}
		s.routeParts[p] = nil
		s.mem.RetainBytes(-partBufBytes)
		if err := w.w.Close(); err != nil {
			errs = append(errs, err)
			continue
		}
		files = append(files, w.path)
	}
	return files, errors.Join(errs...)
}

// finishSpilled runs the passes, one per partitioning, and returns the last
// pass's results merged back into input order.
func (s *windowSink) finishSpilled(ctx context.Context) (Operator, error) {
	files, err := s.closeParts()
	if err != nil {
		return nil, err
	}
	inSch := s.spillSchema
	for pass := 0; ; pass++ {
		exprs := s.passExprs(pass)
		outSch, err := passSchema(inSch, exprs)
		if err != nil {
			return nil, err
		}
		results := make([]string, 0, len(files))
		for _, f := range files {
			res, err := s.runPass(ctx, exprs, outSch, f)
			if err != nil {
				return nil, err
			}
			results = append(results, res)
			os.Remove(f)
		}
		merged, err := mergeByOrdinal(outSch, s.ord, results, s.budget.Account("over"), s.batch)
		if err != nil {
			return nil, err
		}
		if pass == len(s.parts)-1 {
			return &stage{child: merged, op: newTrimOp(s.schema)}, nil
		}

		// The next partitioning needs its own routing, of everything computed so far.
		for {
			b, err := merged.Next(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err == nil {
				err = s.route(ctx, b, s.parts[pass+1].keys)
			}
			if err != nil {
				return nil, errors.Join(err, merged.Close())
			}
		}
		if err := merged.Close(); err != nil {
			return nil, err
		}
		for _, r := range results {
			os.Remove(r)
		}
		if files, err = s.closeParts(); err != nil {
			return nil, err
		}
		inSch = outSch
	}
}

// passSchema is in plus the columns exprs compute.
func passSchema(in *dtype.Schema, exprs []expr.Node) (*dtype.Schema, error) {
	fields := in.FieldSlice()
	for _, e := range exprs {
		f, err := expr.Resolve(e, in)
		if err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return dtype.NewSchema(fields...)
}

// runPass computes exprs over one partition file with a fresh sink a level deeper,
// and writes its rows, still in ordinal order, to a new file.
func (s *windowSink) runPass(ctx context.Context, exprs []expr.Node, outSch *dtype.Schema,
	file string) (_ string, err error) {

	r, err := spill.Open(file)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	sub, err := newWindowSink(r.Schema(), outSch, exprs, s.budget.Account("over"))
	if err != nil {
		return "", err
	}
	sub.budget, sub.batch, sub.level = s.budget, s.batch, s.level+1
	sub.ord, sub.spillSchema = s.ord, r.Schema()
	defer func() { err = errors.Join(err, sub.Close()) }()

	// A file is written a sixteenth of a batch at a time, and a sub-sink retains what
	// it reads, so the fragments are joined back into batches first: held as read,
	// a partition of a few dozen rows cost a buffer's padding per column per
	// fragment, and refused a limit its rows fit in many times over.
	var held []*data.Batch
	rows := 0
	flush := func() error {
		if len(held) == 0 {
			return nil
		}
		b, err := kernel.Concat(r.Schema(), held)
		held, rows = held[:0], 0
		if err != nil {
			return err
		}
		return sub.Consume(ctx, b)
	}
	for {
		b, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		held, rows = append(held, b), rows+b.Rows()
		if rows >= s.batch {
			if err := flush(); err != nil {
				return "", err
			}
		}
	}
	if err := flush(); err != nil {
		return "", err
	}
	op, err := sub.Finish(ctx)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, op.Close()) }()

	path, err := s.newFile()
	if err != nil {
		return "", err
	}
	w, err := spill.CreateSized(path, outSch, partBufBytes)
	if err != nil {
		return "", err
	}
	s.budget.NoteSpill()
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil {
			err = w.Write(b)
		}
		if err != nil {
			return "", errors.Join(err, w.Close())
		}
	}
	return path, w.Close()
}

// closeSpill closes any partition file still open and removes the spill directory.
func (s *windowSink) closeSpill() error {
	var errs []error
	for p, w := range s.routeParts {
		if w != nil {
			errs = append(errs, w.w.Close())
			s.routeParts[p] = nil
		}
	}
	if s.dir != "" {
		errs = append(errs, os.RemoveAll(s.dir))
		s.dir = ""
	}
	return errors.Join(errs...)
}
