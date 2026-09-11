package physical

import (
	"context"
	"os"
	"path/filepath"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// sortSink orders rows.
//
// # Why it buffers
//
// A comparator needs both rows in hand, and any input row can end up adjacent to
// any other, so sorting cannot begin until the last row has arrived. That makes it
// a pipeline breaker and the place where memory pressure first becomes real. It was
// the first buffering operator in ursus that could do something about it: past its
// budget it sorts what it holds, writes that out as a RUN, and merges the runs at
// the end. See extsort.go.
//
// group_by is the second, and its shape is the opposite one — extagg.go partitions
// the KEY SPACE instead of writing ordered runs, so it needs no comparator, no
// merge and no stability rule. The two share only internal/spill and the gather.
//
// # Computed keys are evaluated, kept, and then discarded
//
// `Sort(Col("a").Add(1))` needs a column that does not exist in the input and must
// not exist in the output either: Sort's schema is its input's schema. So key
// columns are evaluated into a side batch used only for comparison, and the gather
// at the end is applied to the DATA columns.
//
// The keys are NOT spilled with the data. A run is re-keyed when it is read back,
// which halves what a run costs on disk and removes any possibility of the data
// and its keys drifting apart. See keyColumns.
type sortSink struct {
	schema *dtype.Schema
	keys   []plan.SortKey
	limit  int
	kSch   *dtype.Schema

	rows []*data.Batch // input batches, retained until Finish or a spill
	keyB []*data.Batch // the matching evaluated key columns

	mem    *execopt.Account
	budget *execopt.Budget
	batch  int

	dir  string   // spill directory, created on the first spill
	runs []string // spilled run files, in input order
}

func (s *sortSink) Schema() *dtype.Schema { return s.schema }

// specs derives the kernel's ordering specs from the plan's sort keys.
//
// Derived rather than stored, because a stored copy is a second source of truth
// for direction and null placement — and a sort whose specs disagreed with its
// keys would order correctly in Explain and wrongly in the answer.
func (s *sortSink) specs() []kernel.SortSpec {
	out := make([]kernel.SortSpec, len(s.keys))
	for i, k := range s.keys {
		out[i] = kernel.SortSpec{Descending: k.Descending, NullsLast: k.NullsLast}
	}
	return out
}

// chunk is the output batch size, defaulting when the sink was built by hand.
func (s *sortSink) chunk() int {
	if s.batch <= 0 {
		return DefaultOptions().BatchSize
	}
	return s.batch
}

// Close releases the accounting and removes the spill directory.
//
// breaker.Close closes the output operator first, so the merge has already let go
// of its readers by the time the files are removed.
func (s *sortSink) Close() error {
	s.mem.Release()
	if s.dir == "" {
		return nil
	}
	dir := s.dir
	s.dir, s.runs = "", nil
	if err := os.RemoveAll(dir); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sort", "removing spill directory %s", dir)
	}
	return nil
}

func (s *sortSink) Consume(ctx context.Context, in *data.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if in.Rows() == 0 {
		return nil
	}

	cols, err := keyColumns(ctx, s.keys, s.kSch, in)
	if err != nil {
		return err
	}
	kb, err := data.NewBatch(s.kSch, cols)
	if err != nil {
		return err
	}

	s.rows = append(s.rows, in)
	s.keyB = append(s.keyB, kb)

	// Retained, not copied — so the budget counts the SOURCE's allocations, and a
	// key that is a bare column reference shares its payload with the data column
	// and is counted once.
	s.mem.Retain(in)
	s.mem.Retain(kb)

	if !s.mem.Over() {
		return nil
	}
	if s.limit > 0 {
		return s.compact(ctx)
	}
	return s.spill(ctx)
}

func (s *sortSink) Merge(other Sink) error {
	o, ok := other.(*sortSink)
	if !ok {
		return uerrInternal("sortSink", other)
	}
	if len(s.runs) > 0 || len(o.runs) > 0 {
		return uerr.Internalf("sort: cannot merge sinks that have spilled")
	}
	// `other` consumed a later slice of the input, so appending preserves the
	// original row order — which is what makes the stable sort below stable across
	// a merge as well as within one.
	s.rows = append(s.rows, o.rows...)
	s.keyB = append(s.keyB, o.keyB...)

	// Retained, not copied, so the batches must be accounted here. Merging without
	// retaining is the defect joinBuildSink.Merge was fixed for at step 13 and
	// reverseSink.Merge at step 51; this was the third instance, latent for the
	// same reason as the second — nothing calls it.
	for _, b := range o.rows {
		s.mem.Retain(b)
	}
	return s.mem.Check()
}

// order sorts the buffered rows and returns them as references into s.rows.
//
// Only the KEY batches are concatenated, never the data: the data is gathered
// through rowRefs instead, so the peak never includes a second copy of the input.
func (s *sortSink) order(limit int) ([]rowRef, error) {
	keys, err := kernel.Concat(s.kSch, s.keyB)
	if err != nil {
		return nil, err
	}
	n := keys.Rows()

	// A bounded limit turns the sort into a top-k: O(n log k) instead of
	// O(n log n). ArgTopK is required to agree with ArgSort[:k] exactly, so this
	// is a pure performance substitution.
	var sel []int32
	if limit > 0 && limit < n {
		cmp, err := kernel.NewComparator(keys.Columns(), s.specs())
		if err != nil {
			return nil, err
		}
		sel = kernel.ArgTopK(n, limit, cmp)
	} else {
		// ArgSortColumns rather than a comparator: given the key columns it can
		// compute each row's order key ONCE instead of once per comparison, which
		// is what lets it radix-sort. It falls back to exactly the comparator
		// above for String and Int128 keys.
		var err error
		sel, err = kernel.ArgSortColumns(keys.Columns(), s.specs(), n)
		if err != nil {
			return nil, err
		}
	}

	// Map global row indices back onto (batch, row).
	starts := make([]int32, len(s.rows)+1)
	for i, b := range s.rows {
		starts[i+1] = starts[i] + int32(b.Rows())
	}
	refs := make([]rowRef, len(sel))
	for i, g := range sel {
		// A binary search would be asymptotically better; a linear scan from the
		// last source is not, because sel is in SORTED order, not input order.
		lo, hi := 0, len(s.rows)-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if starts[mid] <= g {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		refs[i] = rowRef{src: int32(lo), row: g - starts[lo]}
	}
	return refs, nil
}

// compact reduces a BOUNDED sort's buffer to the best `limit` rows.
//
// This is why a top-k never spills. Pruning a prefix to its own top k cannot drop
// a row that the whole input's top k would have kept — adding rows only pushes
// rows out — and it preserves stability: the retained rows keep their relative
// order, and they all precede every row consumed afterwards, which is exactly the
// order their input positions had. So ArgTopK == ArgSort[:k] survives compaction,
// which is the contract the merge would otherwise have had to reproduce across
// runs with no original row index left to break ties on.
func (s *sortSink) compact(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	refs, err := s.order(s.limit)
	if err != nil {
		return err
	}
	kept, err := gatherRows(s.schema, s.rows, refs)
	if err != nil {
		return err
	}
	// The same refs address the key batches: Consume appends to rows and keyB
	// together and skips empty batches for both, so the two lists stay aligned.
	keptKeys, err := gatherRows(s.kSch, s.keyB, refs)
	if err != nil {
		return err
	}

	s.mem.Release()
	s.rows = []*data.Batch{kept}
	s.keyB = []*data.Batch{keptKeys}
	s.mem.Retain(kept)
	s.mem.Retain(keptKeys)
	return nil
}

// spill sorts what is buffered, writes it out as a run, and lets go of it.
func (s *sortSink) spill(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	refs, err := s.order(0)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}

	if s.dir == "" {
		dir, err := os.MkdirTemp(s.budget.SpillDir(), "ursus-sort-")
		if err != nil {
			return uerr.Wrap(err, uerr.KindResource, "sort", "creating a spill directory").
				Hint("choose a writable location with WithSpillDir")
		}
		s.dir = dir
	}
	path := filepath.Join(s.dir, "run"+itoa(len(s.runs))+".ursspill")
	w, err := spill.Create(path, s.schema)
	if err != nil {
		return err
	}

	// Gathered a batch at a time rather than all at once, so writing a run costs
	// one output batch of memory rather than a second copy of the buffer.
	for off := 0; off < len(refs); off += s.chunk() {
		if err := ctx.Err(); err != nil {
			w.Close()
			return err
		}
		b, err := gatherRows(s.schema, s.rows, refs[off:min(off+s.chunk(), len(refs))])
		if err != nil {
			w.Close()
			return err
		}
		if err := w.Write(b); err != nil {
			w.Close()
			return uerr.Wrap(err, uerr.KindIO, "sort", "writing spill run %s", path)
		}
	}
	if err := w.Close(); err != nil {
		return err
	}

	s.runs = append(s.runs, path)
	s.rows, s.keyB = nil, nil
	s.mem.Release()
	s.budget.NoteSpill()
	return nil
}

func (s *sortSink) Finish(ctx context.Context) (Operator, error) {
	if len(s.rows) == 0 && len(s.runs) == 0 {
		empty, err := data.NewBatch(s.schema, emptyColumns(s.schema))
		if err != nil {
			return nil, err
		}
		return newBatchOperator(s.schema, empty), nil
	}

	// The remainder, sorted but never materialised: sortedBufferRun gathers one
	// output batch at a time out of the retained input batches.
	var tail runSource
	if len(s.rows) > 0 {
		refs, err := s.order(s.limit)
		if err != nil {
			return nil, err
		}
		tail = &sortedBufferRun{schema: s.schema, srcs: s.rows, pick: refs, n: s.chunk()}
	}

	if len(s.runs) == 0 {
		return &runOperator{schema: s.schema, src: tail}, nil
	}

	// The spilled runs first, the in-memory remainder last: runs are created in
	// input order, and the merge's stability tie-break depends on that.
	srcs := make([]runSource, 0, len(s.runs)+1)
	for _, p := range s.runs {
		r, err := spill.Open(p)
		if err != nil {
			for _, o := range srcs {
				o.Close()
			}
			return nil, err
		}
		srcs = append(srcs, &fileRun{r: r})
	}
	if tail != nil {
		srcs = append(srcs, tail)
	}

	types := make([]dtype.DataType, s.kSch.Len())
	for i, f := range s.kSch.All() {
		types[i] = f.Type
	}
	rs, err := kernel.NewRunSet(len(srcs), types, s.specs())
	if err != nil {
		return nil, err
	}
	return &mergeOperator{
		schema: s.schema,
		keys:   s.keys,
		kSch:   s.kSch,
		batch:  s.chunk(),
		runs:   srcs,
		rs:     rs,
		m:      kernel.NewMerger(rs),
		mem:    s.budget.Account("sort"),
		cur:    make([]*data.Batch, len(srcs)),
		pos:    make([]int, len(srcs)),
		rows:   make([]int, len(srcs)),
	}, nil
}

func planSort(ctx context.Context, sn *plan.Sort, opts Options) (Operator, error) {
	child, err := planPipelined(ctx, sn.Input, opts)
	if err != nil {
		return nil, err
	}
	in, err := sn.Input.Schema()
	if err != nil {
		return nil, err
	}
	kSch, err := keySchema(sn.Keys, in)
	if err != nil {
		return nil, err
	}

	sink := &sortSink{
		schema: in,
		keys:   sn.Keys,
		limit:  sn.Limit,
		kSch:   kSch,
		budget: opts.Budget,
		mem:    opts.Budget.Account("sort"),
		batch:  opts.batchSize(),
	}
	return &breaker{child: child, sink: sink}, nil
}
