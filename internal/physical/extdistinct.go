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
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// A unique that spills, and answers exactly as it does in memory — order included.
//
// # The freeze
//
// distinctOp holds one entry per distinct key. When the query goes over its limit
// it stops admitting keys, at a batch boundary, the rule hashAggSink's residency
// freeze uses: a row whose key is already held is a duplicate and is dropped, and
// any other row is routed — stamped with its input ordinal — to one of sixteen
// files by the radix of its key. Nothing is emitted while frozen.
//
// # The order
//
// Every row emitted before the freeze came earlier in the input than every row
// routed after it, so the answer is the rows already emitted, followed by the
// survivors of the routed rows in ordinal order. Each file is deduplicated by a
// fresh distinctOp a level deeper — its rows are in input order, so the first of
// each key is the one kept — and its survivors are written out, still in ordinal
// order. The sixteen results are then merged on the ordinal, which is the input's
// order exactly. A sub-level that has to spill again does the same thing, so its
// output is in ordinal order too.
//
// Not Unique's documented contract — it promises the first row per key, not an
// order — but the order the in-memory operator has always produced, and a limit
// that changed it would be a semantic knob.
//
// # What it holds
//
// One file's distinct keys at a time while replaying, and then one batch per file
// while merging. A key cannot be skewed the way a group-by key can — a duplicate
// adds nothing — so the recursion narrows by a factor of sixteen per level until a
// file's keys fit.

// overBudget freezes the seen-set when the query is over its limit and the
// operator can still spill, and reports the resource error otherwise.
func (d *distinctOp) overBudget() error {
	if d.frozen {
		return nil
	}
	if d.budget.Limit() > 0 && d.mem.Over() && len(d.seen) > 0 && d.level < maxSpillDepth {
		d.frozen = true
		return nil
	}
	return d.mem.Check()
}

// route sends every row of in whose key is not already held to its partition file.
func (d *distinctOp) route(in *data.Batch, enc *kernel.GroupKeyEncoder) error {
	for r := range in.Rows() {
		k := enc.Encode(r)
		if _, dup := d.seen[string(k)]; dup {
			continue
		}
		p := partitionOf(k, d.level)
		d.pend[p] = append(d.pend[p], int32(r))
	}
	src, err := d.withOrdinal(in)
	if err != nil {
		return err
	}
	d.nRows += int64(in.Rows())
	for p, rows := range d.pend {
		if len(rows) == 0 {
			continue
		}
		w, err := d.writer(p)
		if err != nil {
			return err
		}
		b, err := takeBatch(d.spillSchema, src, rows)
		if err != nil {
			return err
		}
		if err := w.w.Write(b); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "unique", "writing spill partition %s", w.path)
		}
		d.pend[p] = rows[:0]
	}
	return nil
}

// withOrdinal stamps each row with its input position. Below level 0 the rows
// already carry it, written by the level that routed them.
func (d *distinctOp) withOrdinal(in *data.Batch) (*data.Batch, error) {
	if d.level > 0 {
		return in, nil
	}
	n := in.Rows()
	ord := make([]int64, n)
	for i := range ord {
		ord[i] = d.nRows + int64(i)
	}
	cols := append(append([]*data.Column(nil), in.Columns()...),
		data.NewFixed(d.ord, dtype.Int64, ord, bitmap.AllSet(n)))
	return data.NewBatch(d.spillSchema, cols)
}

// writer opens partition p's file on first use, in a directory of this operator's
// own, which Close removes.
func (d *distinctOp) writer(p int) (*partWriter, error) {
	if w := d.parts[p]; w != nil {
		return w, nil
	}
	if err := d.ensureDir(); err != nil {
		return nil, err
	}
	path := filepath.Join(d.dir, "p"+itoa(p)+".ursspill")
	w, err := spill.CreateSized(path, d.spillSchema, partBufBytes)
	if err != nil {
		return nil, err
	}
	d.parts[p] = &partWriter{w: w, path: path}
	d.mem.RetainBytes(partBufBytes)
	d.budget.NoteSpill()
	return d.parts[p], nil
}

func (d *distinctOp) ensureDir() error {
	if d.dir != "" {
		return nil
	}
	dir, err := os.MkdirTemp(d.budget.SpillDir(), "ursus-unique-")
	if err != nil {
		return uerr.Wrap(err, uerr.KindResource, "unique", "creating a spill directory").
			Hint("choose a writable location with WithSpillDir")
	}
	d.dir = dir
	return nil
}

// replay runs once the input is exhausted: it deduplicates each partition file in
// turn and merges the survivors back into input order.
func (d *distinctOp) replay(ctx context.Context) error {
	var files []string
	for p, w := range d.parts {
		if w == nil {
			continue
		}
		d.parts[p] = nil
		d.mem.RetainBytes(-partBufBytes)
		if err := w.w.Close(); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "unique", "closing spill partition %s", w.path)
		}
		files = append(files, w.path)
	}
	// Every key held is now settled: its row was emitted before the freeze, and
	// every later row with that key was dropped on the way in.
	d.seen = nil
	d.mem.Release()

	results := make([]string, 0, len(files))
	for i, f := range files {
		res := filepath.Join(d.dir, "r"+itoa(i)+".ursspill")
		if err := d.dedupFile(ctx, f, res); err != nil {
			return err
		}
		results = append(results, res)
		os.Remove(f)
	}
	merged, err := mergeByOrdinal(d.spillSchema, d.ord, results, d.budget.Account("unique"), d.batch)
	if err != nil {
		return err
	}
	if d.level > 0 {
		d.out = merged
	} else {
		d.out = &stage{child: merged, op: newTrimOp(d.schema)}
	}
	return nil
}

// dedupFile writes the first row of each key in the partition file in to out, in
// the order they are found.
func (d *distinctOp) dedupFile(ctx context.Context, in, out string) (err error) {
	r, err := spill.Open(in)
	if err != nil {
		return err
	}
	sub := &distinctOp{
		child: &runOperator{schema: d.spillSchema, src: &fileRun{r: r}}, schema: d.spillSchema,
		subset: d.subset, seen: make(map[string]struct{}), mem: d.budget.Account("unique"),

		budget: d.budget, batch: d.batch, level: d.level + 1,
		ord: d.ord, spillSchema: d.spillSchema,
		parts: make([]*partWriter, nParts), pend: make([][]int32, nParts),
	}
	defer func() { err = errors.Join(err, sub.Close()) }()

	w, err := spill.CreateSized(out, d.spillSchema, partBufBytes)
	if err != nil {
		return err
	}
	d.budget.NoteSpill()
	for {
		b, err := sub.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil {
			err = w.Write(b)
		}
		if err != nil {
			return errors.Join(err, w.Close())
		}
	}
	return w.Close()
}

func (d *distinctOp) Close() error {
	d.mem.Release()
	errs := []error{d.child.Close()}
	if d.out != nil {
		errs = append(errs, d.out.Close())
		d.out = nil
	}
	for p, w := range d.parts {
		if w != nil {
			errs = append(errs, w.w.Close())
			d.parts[p] = nil
		}
	}
	if d.dir != "" {
		errs = append(errs, os.RemoveAll(d.dir))
		d.dir = ""
	}
	return errors.Join(errs...)
}
