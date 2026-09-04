package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// External merge sort: sorted runs on disk, merged back in bounded memory.
//
// # Spilling does not go through Sink.Merge, and that corrects a documented claim
//
// sink.go says "Merge is also the spilling seam: a Sink that runs out of memory
// writes its partial state out, starts fresh, and merges at the end." Read
// literally, every verb in that sentence except "merges" is missing from the
// interface: there is no spill, no reset, and Merge takes a live Sink rather than
// anything on disk.
//
// Worse for the sort specifically, Merge's contract is that `other` consumed a
// LATER portion of the input, and sortSink.Merge honours it by concatenating in
// INPUT order — neither sink has sorted anything at that point. So the operator
// whose doc named Merge as the external-sort seam has a Merge that does the
// opposite of a merge. Spilling is therefore internal to Consume, Merge keeps its
// existing meaning, and the claim in sink.go is corrected rather than the code
// contorted to fit it.

// rowRef names one row inside one of several source batches.
type rowRef struct {
	src int32
	row int32
}

// gatherRows builds one batch from rows selected out of SEVERAL source batches,
// in the order given.
//
// This is the primitive both halves of the external sort need — spilling picks
// rows out of the buffered input batches, merging picks them out of one resident
// batch per run — and neither can use kernel.Take alone, which gathers within a
// single column.
//
// It works in two passes rather than one. First each source is gathered
// independently, which is exactly what Take does well; then the per-source
// results are concatenated and permuted back into the requested order. The
// alternative, a multi-source gather kernel, would be a new kernel per physical
// type for a path that runs once per batch rather than once per row.
func gatherRows(schema *dtype.Schema, srcs []*data.Batch, pick []rowRef) (*data.Batch, error) {
	if len(pick) == 0 {
		return data.NewBatch(schema, emptyColumns(schema))
	}
	if schema.Len() == 0 {
		// A frame can legitimately have rows and no columns — that is what
		// NewBatchRows exists for, after Select() with nothing. NewBatch infers the
		// row count FROM the columns and answers 0 when there are none, so without
		// this arm `Scan(src).Select().Sort(...)` lost every row.
		return data.NewBatchRows(schema, nil, len(pick)), nil
	}

	selBySrc := make([][]int32, len(srcs))
	rank := make([]int32, len(pick))
	for p, r := range pick {
		if int(r.src) >= len(srcs) {
			return nil, uerr.Internalf("physical: gather references source %d of %d",
				r.src, len(srcs))
		}
		rank[p] = int32(len(selBySrc[r.src]))
		selBySrc[r.src] = append(selBySrc[r.src], r.row)
	}

	base := make([]int32, len(srcs))
	parts := make([]*data.Batch, 0, len(srcs))
	var acc int32
	for i, sel := range selBySrc {
		base[i] = acc
		if len(sel) == 0 {
			continue
		}
		acc += int32(len(sel))
		cols := make([]*data.Column, srcs[i].NumCols())
		for j, c := range srcs[i].Columns() {
			g, err := kernel.Take(c, sel)
			if err != nil {
				return nil, err
			}
			cols[j] = g.Rename(schema.Field(j).Name)
		}
		b, err := data.NewBatch(schema, cols)
		if err != nil {
			return nil, err
		}
		parts = append(parts, b)
	}

	if len(parts) == 1 {
		// Every row came from one source, so the single gather above already
		// produced them in the requested order and the permutation is the identity.
		return parts[0], nil
	}

	cat, err := kernel.Concat(schema, parts)
	if err != nil {
		return nil, err
	}
	perm := make([]int32, len(pick))
	for p, r := range pick {
		perm[p] = base[r.src] + rank[p]
	}
	cols := make([]*data.Column, cat.NumCols())
	for i, c := range cat.Columns() {
		if cols[i], err = kernel.Take(c, perm); err != nil {
			return nil, err
		}
	}
	return data.NewBatch(schema, cols)
}

// --- runs ----------------------------------------------------------------------

// runSource yields one sorted run's batches in order.
//
// Two implementations: a run that was written to disk, and the remainder that
// never left memory. The merge does not care which, which is what keeps it a
// single code path rather than one for the spilling case and one for not.
type runSource interface {
	Next() (*data.Batch, error) // io.EOF when the run is exhausted
	Close() error
}

// fileRun replays a spilled run.
type fileRun struct{ r *spill.Reader }

func (f *fileRun) Next() (*data.Batch, error) { return f.r.Next() }
func (f *fileRun) Close() error               { return f.r.Close() }

// sortedBufferRun serves the rows still in memory, in sorted order.
//
// It gathers a batch at a time rather than materialising the whole sorted
// remainder, so a sort that never spills still holds ONE output batch instead of
// a second copy of its entire input. That is where "even the output of a sort is
// one allocation the size of the input" stops being true.
type sortedBufferRun struct {
	schema *dtype.Schema
	srcs   []*data.Batch
	pick   []rowRef
	pos    int
	n      int
}

func (r *sortedBufferRun) Next() (*data.Batch, error) {
	if r.pos >= len(r.pick) {
		return nil, io.EOF
	}
	end := min(r.pos+r.n, len(r.pick))
	b, err := gatherRows(r.schema, r.srcs, r.pick[r.pos:end])
	if err != nil {
		return nil, err
	}
	r.pos = end
	return b, nil
}

func (r *sortedBufferRun) Close() error {
	r.srcs, r.pick = nil, nil
	return nil
}

// batchRun chunks one materialised batch, so a Sink's Finish can stream a result
// it computed in one piece rather than handing over a single allocation.
type batchRun struct {
	b   *data.Batch
	pos int
	n   int
}

func (r *batchRun) Next() (*data.Batch, error) {
	if r.b == nil || r.pos >= r.b.Rows() {
		return nil, io.EOF
	}
	end := min(r.pos+r.n, r.b.Rows())
	b := r.b.Slice(r.pos, end-r.pos)
	r.pos = end
	return b, nil
}

func (r *batchRun) Close() error { r.b = nil; return nil }

// runOperator streams one run. It is what a sort that did not spill returns.
type runOperator struct {
	schema *dtype.Schema
	src    runSource
}

func (o *runOperator) Schema() *dtype.Schema { return o.schema }
func (o *runOperator) Close() error          { return o.src.Close() }

func (o *runOperator) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := o.src.Next()
		if err != nil {
			return nil, err
		}
		if b.Rows() == 0 {
			continue // an empty batch is not end-of-stream
		}
		return b, nil
	}
}

// --- the merge operator ---------------------------------------------------------

// mergeOperator merges sorted runs into one ordered stream.
//
// concatOp is the template — the same children-in-order shape, the same flat
// close-all rule, the same "an empty batch is not end-of-stream" discipline — and
// it differs in exactly one place: it advances every cursor by comparison rather
// than draining one child to exhaustion before starting the next.
//
// plan.Union is deliberately NOT the runs-reader. Spilling is a runtime decision,
// so putting runs in the logical plan would make golden Explain output depend on
// how much memory happened to be available.
//
// # What it holds, and the one term that is not bounded by the budget
//
// One resident batch per run, plus the output window. The first term is the
// honest limitation: the number of runs is input size divided by the budget, so a
// single-pass k-way merge holds O(input / budget) batches and the peak is
//
//	budget  +  runs x batch size
//
// not `budget`. At the scale the tests exercise — ten runs of 512 rows — that
// second term is a fifth of the first. At a thousand runs it would dominate, and
// the answer then is a multi-pass merge that bounds the fan-in. Not built, and
// stated here rather than left for someone to discover from an OOM.
type mergeOperator struct {
	schema *dtype.Schema
	keys   []plan.SortKey
	kSch   *dtype.Schema
	batch  int

	runs []runSource
	rs   *kernel.RunSet
	m    *kernel.Merger
	mem  *execopt.Account

	cur   []*data.Batch // the resident batch of each run
	pos   []int         // the next unemitted row of each run
	rows  []int         // how many rows that batch has
	seeds bool
}

func (o *mergeOperator) Schema() *dtype.Schema { return o.schema }

// Close closes every run, including the ones already exhausted and the ones never
// reached — the flat rule concatOp and joinBreaker use, for the same reason: a
// conditional close is a leak waiting for an early break.
func (o *mergeOperator) Close() error {
	o.mem.Release()
	errs := make([]error, 0, len(o.runs))
	for _, r := range o.runs {
		errs = append(errs, r.Close())
	}
	return errors.Join(errs...)
}

func (o *mergeOperator) Next(ctx context.Context) (*data.Batch, error) {
	if !o.seeds {
		for r := range o.runs {
			if err := o.advance(ctx, r); err != nil {
				return nil, err
			}
		}
		o.seeds = true
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if o.m.Len() == 0 {
			return nil, io.EOF
		}

		// srcOf maps a run to its slot in srcs for THIS output batch. It resets when
		// a run refills, because the rows already picked came from the old batch and
		// the ones to come will not.
		srcs := make([]*data.Batch, 0, len(o.runs))
		srcOf := make([]int, len(o.runs))
		for i := range srcOf {
			srcOf[i] = -1
		}
		pick := make([]rowRef, 0, o.batch)

		for len(pick) < o.batch {
			c, ok := o.m.Pop()
			if !ok {
				break
			}
			if srcOf[c.Run] < 0 {
				srcOf[c.Run] = len(srcs)
				srcs = append(srcs, o.cur[c.Run])
			}
			pick = append(pick, rowRef{src: int32(srcOf[c.Run]), row: int32(c.Row)})

			o.pos[c.Run]++
			if o.pos[c.Run] < o.rows[c.Run] {
				o.m.Push(c.Run, o.pos[c.Run])
				continue
			}
			if err := o.advance(ctx, c.Run); err != nil {
				return nil, err
			}
			srcOf[c.Run] = -1
		}

		if len(pick) == 0 {
			return nil, io.EOF
		}
		return gatherRows(o.schema, srcs, pick)
	}
}

// advance loads run r's next batch, resolves its key columns and pushes its head.
// A run with nothing left simply contributes no cursor.
func (o *mergeOperator) advance(ctx context.Context, r int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := o.runs[r].Next()
		if errors.Is(err, io.EOF) {
			o.cur[r], o.rows[r], o.pos[r] = nil, 0, 0
			return nil
		}
		if err != nil {
			return err
		}
		if b.Rows() == 0 {
			continue
		}
		keys, err := keyColumns(ctx, o.keys, o.kSch, b)
		if err != nil {
			return err
		}
		if err := o.rs.Set(r, keys); err != nil {
			return err
		}
		o.cur[r], o.rows[r], o.pos[r] = b, b.Rows(), 0
		o.m.Push(r, 0)
		// Recomputed rather than accumulated, because a run REPLACES its resident
		// batch rather than adding one — the same shape tailOp's evicting ring uses.
		// It is bounded by the number of runs, so the loop is short.
		//
		// Without this the merge phase sat entirely outside the ledger and
		// MemoryStats.Peak measured only the accumulation phase, which is invisible
		// at ten runs and is not at a thousand.
		o.mem.Release()
		for _, b := range o.cur {
			o.mem.Retain(b)
		}
		return nil
	}
}

// keyColumns evaluates the sort keys against b under the key schema's names.
//
// The merge recomputes keys rather than spilling them alongside the data, which
// halves what a run costs on disk and removes the possibility of the two files
// drifting out of step. It is sound because a sort key is a row-local expression:
// evaluating it over a gathered batch gives the gathered key values. A key that
// were not row-local would already produce batch-size-dependent answers in
// Consume, which the batch-size invariance tests would have caught long ago.
func keyColumns(ctx context.Context, keys []plan.SortKey, kSch *dtype.Schema,
	b *data.Batch) ([]*data.Column, error) {

	cols := make([]*data.Column, len(keys))
	for i, k := range keys {
		c, err := evalColumn(ctx, k.Expr, b)
		if err != nil {
			return nil, err
		}
		cols[i] = c.Rename(kSch.Field(i).Name)
	}
	return cols, nil
}

// keySchema names the synthetic columns a sort's computed keys live in.
//
// Synthetic, so a computed key can never collide with a real column and the key
// batch stays independent of the data batch.
func keySchema(keys []plan.SortKey, in *dtype.Schema) (*dtype.Schema, error) {
	kf := make([]dtype.Field, len(keys))
	for i, k := range keys {
		f, err := expr.Resolve(k.Expr, in)
		if err != nil {
			return nil, err
		}
		kf[i] = f.Rename("__key" + itoa(i))
	}
	return dtype.NewSchema(kf...)
}
