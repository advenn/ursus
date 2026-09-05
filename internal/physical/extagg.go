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
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// Spilling hash aggregation: radix partitioning of the keys admitted after the
// residency freeze. See hashAggSink's doc for the invariant this rests on.

const (
	// partBits fixes the fan-out at 16, and it is not adaptive on purpose.
	//
	// Deriving it from the budget would make the FILE LAYOUT — and therefore the
	// unordered output order — depend on the limit in a second, independent way.
	// One such dependence (the freeze point) is already more than enough to have to
	// document. Sixteen per level with recursion covers 256x the budget at two
	// levels and 4096x at three.
	partBits = 4
	nParts   = 1 << partBits

	// partBufBytes is deliberately smaller than spill.DefaultWriteBuffer. Sixteen
	// writers at 64 KiB would be 1 MiB — larger than most limits the memory tests
	// use — and Write flushes at the end of every batch anyway, so a partition
	// writer never holds more than one batch-slice.
	partBufBytes = 1 << 13

	// maxSpillDepth turns "one key too large for any budget" into an error naming
	// the cause rather than a long descent. Termination does not depend on it: a
	// sub-sink admits every key of its first batch before it can freeze, so each
	// level strictly reduces the largest partition's distinct-key count.
	maxSpillDepth = 4

	// ordCol carries a routed row's input ordinal so first-appearance order can be
	// rebuilt. It is in the SPILL schema only, never in the sink's output.
	ordCol = "__ord"
)

// partitionOf picks a radix bucket for an encoded group key at a given depth.
//
// # The depth is mixed into the hash, not used to select different bits
//
// That is termination, not quality. Every key inside a partition already agrees in
// the bits the parent used, so re-hashing with the same function would map the
// whole partition onto one sub-partition and the recursion would never narrow. The
// bit-slice alternative fails a second way: at depth 16 it shifts a uint64 by 64,
// which Go defines as zero, so every key collapses into bucket 0.
func partitionOf(key []byte, level int) int {
	h := kernel.HashKey(key)
	if level > 0 {
		h = kernel.Mix64(h ^ kernel.Mix64(uint64(level)*0x9E3779B97F4A7C15))
	}
	return int(h & (nParts - 1))
}

// partWriter is one radix bucket's open spill file.
type partWriter struct {
	w    *spill.Writer
	path string
}

// --- routing -------------------------------------------------------------------

// route writes this batch's non-resident rows out, one slice per bucket.
//
// Rows are appended in ascending index order and batches arrive in input order, so
// each file is in INPUT ORDER — which is what makes First, Last, ArgMin and ArgMax
// correct on a replayed partition with no position shift.
func (s *hashAggSink) route(in *data.Batch) error {
	src, err := s.withOrdinal(in)
	if err != nil {
		return err
	}
	for p, rows := range s.pend {
		if len(rows) == 0 {
			continue
		}
		w, err := s.writer(p)
		if err != nil {
			return err
		}
		b, err := takeBatch(s.spillSchema, src, rows)
		if err != nil {
			return err
		}
		if err := w.w.Write(b); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "group_by",
				"writing spill partition %s", w.path)
		}
		s.pend[p] = rows[:0]
	}
	return nil
}

// writer opens bucket p's file on first use.
//
// Lazily, which is why an empty partition file cannot exist: a writer is only ever
// created from inside route, which skips a bucket with no rows. Sixteen eager files
// for a three-key overflow would be sixteen syscalls and sixteen buffers for
// nothing.
func (s *hashAggSink) writer(p int) (*partWriter, error) {
	if w := s.parts[p]; w != nil {
		return w, nil
	}
	if s.dir == "" {
		dir, err := os.MkdirTemp(s.budget.SpillDir(), "ursus-agg-")
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindResource, "group_by",
				"creating a spill directory").
				Hint("choose a writable location with WithSpillDir")
		}
		s.dir, s.dirOwner = dir, true
		s.prefix = filepath.Join(dir, "p")
	}
	path := s.prefix + itoa(p) + ".ursspill"
	w, err := spill.CreateSized(path, s.spillSchema, partBufBytes)
	if err != nil {
		return nil, err
	}
	s.parts[p] = &partWriter{w: w, path: path}
	s.mem.RetainBytes(partBufBytes)
	s.budget.NoteSpill()
	return s.parts[p], nil
}

// closeParts finishes every open writer and records its file.
func (s *hashAggSink) closeParts() error {
	var errs []error
	for p, w := range s.parts {
		if w == nil {
			continue
		}
		if err := w.w.Close(); err != nil {
			errs = append(errs, err)
		} else {
			s.files = append(s.files, w.path)
		}
		s.parts[p] = nil
		s.mem.RetainBytes(-partBufBytes)
	}
	return errors.Join(errs...)
}

// takeBatch gathers rows out of one batch under a target schema.
//
// Not gatherRows: this has exactly one source, and it must preserve the row count
// of a ZERO-COLUMN batch, which only NewBatchRows can express.
func takeBatch(sch *dtype.Schema, in *data.Batch, sel []int32) (*data.Batch, error) {
	if in.NumCols() == 0 {
		return data.NewBatchRows(sch, nil, len(sel)), nil
	}
	cols := make([]*data.Column, in.NumCols())
	for j, c := range in.Columns() {
		g, err := kernel.Take(c, sel)
		if err != nil {
			return nil, err
		}
		cols[j] = g.Rename(sch.Field(j).Name)
	}
	return data.NewBatch(sch, cols)
}

// withOrdinal appends the input-ordinal column when order is being maintained.
//
// At level 0 it is synthesised from the running row count; deeper, the column is
// already in the batch because the parent wrote it, so it is gathered like any
// other and the ordinals stay GLOBAL with no extra aggregate spec.
func (s *hashAggSink) withOrdinal(in *data.Batch) (*data.Batch, error) {
	if !s.ordered || s.level > 0 {
		return in, nil
	}
	n := in.Rows()
	ord := make([]int64, n)
	for i := range n {
		ord[i] = s.nRows + int64(i)
	}
	cols := append(append([]*data.Column(nil), in.Columns()...),
		data.NewFixed(ordCol, dtype.Int64, ord, bitmap.AllSet(n)))
	return data.NewBatch(s.spillSchema, cols)
}

// ordinalOf is the input ordinal of row i of the current batch.
func (s *hashAggSink) ordinalOf(in *data.Batch, i int) int64 {
	if s.level == 0 {
		return s.nRows + int64(i)
	}
	c, ok := in.ByName(ordCol)
	if !ok {
		return s.nRows + int64(i)
	}
	v, err := data.Values[int64](c)
	if err != nil {
		return s.nRows + int64(i)
	}
	return v[i]
}

// --- the budget rule ------------------------------------------------------------

// overBudget reports the one kind of pressure the freeze cannot relieve.
//
// Before the freeze this is Account.Check verbatim. After it the query is over
// budget BY CONSTRUCTION — that is what froze it — so a bare Check would fail every
// spilling query. What has to be detected is narrower: RESIDENT state that keeps
// growing after residency was closed.
//
// keyParts cannot grow, because new keys are routed. Reserve(nGroups) is a no-op
// once nGroups is fixed, so every O(groups) accumulator's NBytes goes flat at the
// freeze. The only things that can still grow are the two holistic accumulators —
// quantile keeps every value of a group, n_unique every distinct value — and radix
// partitioning divides across KEYS, never within one, so there is nothing left to
// try.
//
// So the property this states is exact rather than heuristic: a spilling group-by
// refuses exactly when an accumulator's state is O(rows), and never otherwise.
func (s *hashAggSink) overBudget() error {
	if !s.frozen {
		return s.mem.Check()
	}
	limit := s.budget.Limit()
	if limit <= 0 || s.accBytes <= s.frozenAcc+limit {
		return nil
	}
	return uerr.New(uerr.KindResource, "group_by",
		"memory limit of %s exceeded: group_by has already partitioned its keys to "+
			"disk and is still holding %s of per-group state for %d resident groups",
		execopt.Bytes(limit), execopt.Bytes(s.accBytes), s.ids.Len()).
		Hint("radix partitioning divides work across KEYS, never within one — " +
			"quantile and median keep every value of a group, and n_unique every " +
			"distinct value, so a single hot key cannot be spilled").
		Hint("raise the limit with WithMemoryLimit, or use a bounded aggregate")
}

// --- replay ----------------------------------------------------------------------

// newSub builds the sink that re-aggregates one partition file.
//
// It shares everything static — schema, keys, specs, key schema, spill directory,
// budget — and gets FRESH group state at the next radix depth. specs carry their
// own bind, params and input type, which is what stops a replayed Quantile(0.9)
// silently becoming a median.
func (s *hashAggSink) newSub(path string) (*hashAggSink, error) {
	if s.level+1 > maxSpillDepth {
		return nil, uerr.New(uerr.KindResource, "group_by",
			"memory limit exceeded: a single group is too large to partition").
			Hint("partitioning divides work across KEYS, so %d levels of it have not "+
				"split this one — the data is skewed onto one key", maxSpillDepth).
			Hint("raise the limit with WithMemoryLimit")
	}
	accs := make([]kernel.Accumulator, len(s.specs))
	for i, sp := range s.specs {
		a, err := kernel.NewAccumulator(sp.op, sp.inType, sp.bind, sp.params)
		if err != nil {
			return nil, err
		}
		accs[i] = a
	}
	return &hashAggSink{
		schema: s.schema, keys: s.keys, specs: s.specs, keySchema: s.keySchema,
		ids:  kernel.NewKeyTable(),
		accs: accs,
		mem:  s.budget.Account("group_by"),

		budget: s.budget, batch: s.batch, level: s.level + 1,
		inSchema: s.spillSchema, spillSchema: s.spillSchema,
		dir: s.dir, dirOwner: false, prefix: path + "-",
		parts: make([]*partWriter, nParts), pend: make([][]int32, nParts),

		ordered: s.ordered,
	}, nil
}

// aggSpillOp emits the resident groups, then re-aggregates each spilled partition
// in turn and emits its groups.
//
// PARTITION-MAJOR: resident groups in first-appearance order, then partition 0's,
// then partition 1's. That is the order MaintainOrder buys back, and without it the
// contract plan.Aggregate has always stated — "without it the output order is
// genuinely unspecified" — finally becomes load-bearing.
//
// It holds ONE sub-aggregation at a time, which is what keeps it bounded: the
// sub-sink gets the same budget, freezes on the same rule, and spills its own
// sub-partitions if it has to. The recursion is not extra machinery — newSub
// returns the same type.
type aggSpillOp struct {
	schema *dtype.Schema
	parent *hashAggSink
	head   *data.Batch // the resident answer, chunked out first
	pos, n int
	files  []string
	next   int
	cur    Operator
	sink   *hashAggSink
}

func (o *aggSpillOp) Schema() *dtype.Schema { return o.schema }

// Close closes the live sub-aggregation if there is one. The partition FILES are
// removed by the owning sink's Close, which breaker runs after this.
func (o *aggSpillOp) Close() error {
	var errs []error
	if o.cur != nil {
		errs = append(errs, o.cur.Close())
	}
	if o.sink != nil {
		errs = append(errs, o.sink.Close())
	}
	o.cur, o.sink = nil, nil
	return errors.Join(errs...)
}

func (o *aggSpillOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if o.head != nil {
			end := min(o.pos+o.n, o.head.Rows())
			b := o.head.Slice(o.pos, end-o.pos)
			o.pos = end
			if o.pos >= o.head.Rows() {
				o.head = nil
				// The resident answer has been handed downstream, so the parent's claim
				// on it goes with it — and the partitions about to be replayed get the
				// WHOLE budget rather than what the parent left over. See releaseState.
				o.parent.mem.Release()
			}
			if b.Rows() > 0 {
				return b, nil
			}
			continue
		}
		if o.cur == nil {
			if o.next >= len(o.files) {
				return nil, io.EOF
			}
			if err := o.start(ctx, o.files[o.next]); err != nil {
				return nil, err
			}
			o.next++
		}
		b, err := o.cur.Next(ctx)
		if errors.Is(err, io.EOF) {
			// Drop the whole sub-aggregation before opening the next file: that is
			// what keeps this to one live sub-sink rather than one per partition.
			err = errors.Join(o.cur.Close(), o.sink.Close())
			o.cur, o.sink = nil, nil
			if err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.Rows() == 0 {
			continue // an empty batch is not end-of-stream
		}
		return b, nil
	}
}

// start drains one partition file into a fresh sink and installs its output.
func (o *aggSpillOp) start(ctx context.Context, path string) error {
	sub, err := o.parent.newSub(path)
	if err != nil {
		return err
	}
	if err := drainInto(ctx, sub, path); err != nil {
		sub.Close()
		return err
	}
	op, err := sub.Finish(ctx)
	if err != nil {
		sub.Close()
		return err
	}
	o.cur, o.sink = op, sub
	return nil
}

// drainInto feeds every batch of a partition file to a sink.
func drainInto(ctx context.Context, sink *hashAggSink, path string) error {
	r, err := spill.Open(path)
	if err != nil {
		return err
	}
	run := &fileRun{r: r}
	for {
		b, err := run.Next()
		if errors.Is(err, io.EOF) {
			return run.Close()
		}
		if err != nil {
			run.Close()
			return err
		}
		if err := sink.Consume(ctx, b); err != nil {
			run.Close()
			return err
		}
	}
}

// --- MaintainOrder ----------------------------------------------------------------

// collectOrdered drains this sink's whole answer into one batch, with the input
// ordinal of each group's first row beside it.
//
// It recurses through the sink's own partitions, so a group discovered at depth 2
// still carries the ordinal the level-0 sink stamped on its row.
func (s *hashAggSink) collectOrdered(ctx context.Context, resident *data.Batch,
) ([]*data.Batch, []int64, error) {

	parts := []*data.Batch{resident}
	ords := append([]int64(nil), s.firstSeen[:resident.Rows()]...)

	for _, path := range s.files {
		sub, err := s.newSub(path)
		if err != nil {
			return nil, nil, err
		}
		if err := drainInto(ctx, sub, path); err != nil {
			sub.Close()
			return nil, nil, err
		}
		subRes, err := sub.residentResult()
		if err != nil {
			sub.Close()
			return nil, nil, err
		}
		if err := sub.closeParts(); err != nil {
			sub.Close()
			return nil, nil, err
		}
		sub.releaseState()
		ps, os_, err := sub.collectOrdered(ctx, subRes)
		if err != nil {
			sub.Close()
			return nil, nil, err
		}
		parts = append(parts, ps...)
		ords = append(ords, os_...)
		if err := sub.Close(); err != nil {
			return nil, nil, err
		}
	}
	return parts, ords, nil
}

// finishOrdered restores first-appearance order across the resident groups and
// every partition.
//
// # It cannot stream, and that is inherent
//
// First appearance INTERLEAVES resident and spilled groups: the group created at
// input row 3 may be resident, the one at row 4 spilled to partition 9, the one at
// row 5 resident again. Emitting row 3's group before row 4's therefore requires
// partition 9's aggregation to be complete. So the whole result is materialised —
// O(distinct keys), which is what a non-spilling aggregation holds anyway, but it
// means an ORDERED spilling group-by peaks at budget + result rather than budget.
func (s *hashAggSink) finishOrdered(ctx context.Context, resident *data.Batch) (Operator, error) {
	// The account is already released (Finish rebased it) and stays released for the
	// whole collection, so each partition's replay gets the full budget. The parts
	// accumulating here are the documented "+ result" term, and they are charged back
	// below — with Retain, not Check, because exceeding the limit by the size of the
	// answer is what MaintainOrder was asked for, not a failure.
	parts, ords, err := s.collectOrdered(ctx, resident)
	if err != nil {
		return nil, err
	}
	all, err := kernel.Concat(s.schema, parts)
	if err != nil {
		return nil, err
	}
	s.mem.Retain(all)
	if all.Rows() != len(ords) {
		return nil, uerr.Internalf("physical: %d group rows but %d ordinals",
			all.Rows(), len(ords))
	}
	// Ordinals are distinct — one per group, taken from the row that created it —
	// so stability is moot and any correct sort gives the same answer.
	perm := kernel.ArgSort(len(ords), func(i, j int) int {
		switch {
		case ords[i] < ords[j]:
			return -1
		case ords[i] > ords[j]:
			return 1
		default:
			return 0
		}
	})
	pick := make([]rowRef, len(perm))
	for i, p := range perm {
		pick[i] = rowRef{src: 0, row: p}
	}
	return &runOperator{schema: s.schema, src: &sortedBufferRun{
		schema: s.schema, srcs: []*data.Batch{all}, pick: pick, n: s.chunk(),
	}}, nil
}
