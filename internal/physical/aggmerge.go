package physical

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// goSerial is how parallelSink hands the rest of the input to the merged sink.
func (s *hashAggSink) goSerial() { s.parallel = false }

// discard drops a merged-in sink's state as soon as it has been folded, so the fold
// of N tables holds about one table at a time, not two. Its account is already
// released by Merge, and Close still runs.
func (s *hashAggSink) discard() {
	s.ids, s.accs, s.keyParts, s.firstSeen = s.ids.empty(), nil, nil, nil
}

// recharge measures the accumulator state again and charges the difference, and
// returns it.
//
// Accumulator state is tracked as a DELTA rather than released and re-retained, so
// accounting stays O(groups) per batch rather than O(groups) map churn.
func (s *hashAggSink) recharge() int64 {
	var acc int64
	for _, a := range s.accs {
		acc += a.NBytes()
	}
	acc += int64(cap(s.firstSeen))*8 + s.ids.NBytes()
	s.mem.RetainBytes(acc - s.accBytes)
	s.accBytes = acc
	return acc
}

func (s *hashAggSink) Merge(other Sink) error {
	o, ok := other.(*hashAggSink)
	if !ok {
		return uerr.Internalf("physical: cannot merge %T into hashAggSink", other)
	}
	// Two spilling sinks each hold their own partition files, which this fold would
	// silently drop; and inserting a key into a table that has already frozen breaks
	// the residency invariant. sortSink.Merge and joinBuildSink.Merge have the same
	// guard, and aggCanParallelise refuses to build workers under a memory limit at
	// all, so this is the second line of defence rather than the first.
	if s.frozen || o.frozen || len(s.files) > 0 || len(o.files) > 0 ||
		s.spills != nil || o.spills != nil {
		return uerr.Internalf("physical: cannot merge hashAggSinks that have spilled")
	}
	// MaintainOrder is not mergeable here. firstSeen holds each group's input
	// ordinal, and two sinks that consumed INTERLEAVED portions number their rows
	// independently — worker 1's ordinal 5 may precede worker 0's ordinal 3 — so
	// there is no way to reconcile them without a global sequence the driver does
	// not carry. Refused rather than approximated; aggCanParallelise declines the
	// same case up front.
	if s.ordered || o.ordered {
		return uerr.Internalf(
			"physical: cannot merge hashAggSinks with MaintainOrder — first-appearance " +
				"ordinals are per-sink and the two saw interleaved portions of the input")
	}

	// The remap: o's group ids expressed in this sink's numbering.
	//
	// o's keys are visited in o's OWN ID ORDER, which matters for more than
	// tidiness. Group ids are assigned by first appearance and Finish emits in id
	// order, so the serial path produces first-appearance ordering as a side
	// effect; visiting in any other order would make the merged ordering vary from
	// run to run, turning a deterministic output into a random one for every query
	// that aggregates without MaintainOrder.
	//
	// KeyAt indexes by id directly. This used to invert the map into a []string
	// first, purely because a Go map cannot be indexed by value. A chunk at a time
	// since step 160, which gives ids in the same order.
	nOther := o.ids.Len()
	remap := make([]int32, nOther)
	var newRows []int32 // o's row index, for keys this sink has never seen
	var (
		oids     [kernel.ManyChunk]int32
		ids      [kernel.ManyChunk]int32
		inserted [kernel.ManyChunk]bool
	)
	for lo := 0; lo < nOther; lo += kernel.ManyChunk {
		m := min(kernel.ManyChunk, nOther-lo)
		for j := range m {
			oids[j] = int32(lo + j)
		}
		s.ids.insertFrom(o.ids, oids[:m], ids[:m], inserted[:m])
		for j := range m {
			if inserted[j] {
				newRows = append(newRows, oids[j])
			}
			remap[lo+j] = ids[j]
		}
	}

	// The key VALUES of the groups only o saw. This is the piece joinBuildSink.Merge
	// does not need: a join's per-key state is a row count, so its keys never have
	// to be materialised again. Here Finish concatenates keyParts and pairs the
	// result positionally with one aggregate row per group, so a group with no key
	// row shifts every key after it.
	if len(newRows) > 0 && len(s.keys) > 0 {
		keys, err := kernel.Concat(o.keySchema, o.keyParts)
		if err != nil {
			return err
		}
		cols := make([]*data.Column, keys.NumCols())
		for i := range cols {
			taken, err := kernel.Take(keys.Column(i), newRows)
			if err != nil {
				return err
			}
			cols[i] = taken.Rename(s.keySchema.Field(i).Name)
		}
		part, err := data.NewBatch(s.keySchema, cols)
		if err != nil {
			return err
		}
		s.keyParts = append(s.keyParts, part)
		s.mem.Retain(part)
	}

	for i := range s.accs {
		if err := s.accs[i].Merge(o.accs[i], remap); err != nil {
			return err
		}
	}
	s.nRows += o.nRows

	// Re-measure the accumulator state as a delta, exactly as Consume does. The
	// merge grew both the group count and the per-group state, and without this the
	// reported peak omits everything the fold pulled in.
	s.recharge()
	// other's state now lives in s, which has charged for it, so other's account
	// is released here rather than at Close: under a budget, the merged sink would
	// otherwise see the query as over it twice and freeze early (step 91). Buffers
	// both hold are counted by allocation, so s keeps paying for those.
	o.mem.Release()
	o.accBytes = 0
	return nil
}

// fewAggRows is the most rows a group-by's input holds, by the sources' counts, for it
// to build one sink where the caller gave no WithThreads (step 396).
const fewAggRows = 32_768

// aggWorkers decides how many sinks a group-by gets, and is the whole safety
// argument for running one on several threads.
//
// It returns 1 — the serial breaker, byte-identical to every step before this one —
// unless BOTH gates pass. Each is a property that was checked against the code
// rather than assumed, and each declines rather than approximates.
//
// # Order
//
// Sink.Merge's contract is that `other` consumed a LATER portion of the input, and
// parallelSink's round-robin dispatcher does not satisfy it: worker 0 holds batches
// 0, N, 2N…, so worker 1's batch 1 precedes worker 0's batch N. So the aggregate
// may only run in parallel when its answer does not depend on order at all.
//
//	MaintainOrder      firstSeen holds per-sink input ordinals, and two sinks that
//	                   saw interleaved portions numbered their rows independently.
//	First/Last         name a position outright.
//	ArgMin/ArgMax      report an INDEX, and their Merge shifts by the row count of
//	                   a portion it assumes came earlier.
//
// The whole aggregate is declined rather than the individual expression, because
// one Agg list is one hash table.
//
// # Memory
//
// hashAggSink refuses to Merge once it has frozen or spilled, so the workers never
// freeze: a worker that would returns errWantSerial instead, and parallelSink folds
// the workers together and finishes the input on the one merged sink, which then
// freezes and spills exactly as the serial path does (step 91). A group-by that fits
// stays parallel; one that does not degrades to serial at the point it stopped
// fitting, rather than failing.
//
// This used to be decided up front — serial under any limit — and was harmless
// while a limit was rare. Step 90 gave every query a default budget, and every
// group-by went serial: h2o's small ones half again as slow.
func aggWorkers(a *plan.Aggregate, specs []aggSpec, opts Options) int {
	if opts.Threads <= 1 || a.MaintainOrder {
		return 1
	}
	// Under a limit the caller set, serial, as before step 91. Where a parallel
	// group-by switches to serial depends on which worker crosses half the budget
	// first, which is scheduling, and an unordered group-by's row order depends on
	// where it switched: CI found two runs at one limit giving two orders. Under the
	// default budget that switch needs half the machine, and is rare; under an
	// explicit limit, run to run reproducibility is what the caller gets (step 92).
	if opts.Budget.Limit() > 0 && !opts.Budget.IsDefault() {
		return 1
	}
	// A few batches, where the caller gave no WithThreads, on one sink (step 396): a
	// 10,000-row group-by took 395 µs on eight sinks and their merge, and 248 on one;
	// at 40,000 rows two sinks were the quicker.
	if opts.Fewer {
		if rows, ok := plan.EstimateRows(a.Input); ok && rows <= fewAggRows {
			return 1
		}
	}
	for _, spec := range specs {
		if spec.op.IsOrderDependent() {
			return 1
		}
	}
	return opts.Threads
}

// aggBreaker builds n sinks and wraps them in the matching driver.
//
// n == 1 returns the plain breaker rather than a one-worker parallelSink, so the
// serial path keeps exactly the operator tree it had — no goroutine, no channel,
// and nothing to explain when a profile is read.
func aggBreaker(child Operator, newSink SinkFactory, n int) (Operator, error) {
	sinks := make([]Sink, n)
	for i := range n {
		s, err := newSink()
		if err != nil {
			return nil, err
		}
		sinks[i] = s
	}
	if n == 1 {
		return &breaker{child: child, sink: sinks[0]}, nil
	}
	for _, s := range sinks {
		if h, ok := s.(*hashAggSink); ok {
			h.parallel = true
		}
	}
	ps := newParallelSink(child, sinks)
	ps.newSink = newSink
	return ps, nil
}

// partitionedFoldMin is how many groups the workers must hold between them for the
// fold to run partitioned. Below it the serial fold takes milliseconds, and keeps
// the first-appearance order every small group-by has always come out in.
const partitionedFoldMin = 1 << 16

// partitionedFolds counts the folds that ran partitioned, for a test to see that
// one did.
var partitionedFolds atomic.Int64

// foldPartitioned folds the workers' sinks into as many partitions, all at once,
// each on its own goroutine (step 150): v0.5 item 2, the radix group-by's stage a.
//
// # Why
//
// The serial fold, Merge, re-inserts every worker's every key into one table, on
// one goroutine, after the workers have finished. Over two million groups it was
// about 0.43 s of a 0.70 s group-by whose consuming took eight cores a quarter of
// that, and 24% of gb10's allocation (step 132).
//
// # How
//
// Each worker's groups are routed by the hash its key table stored, so no key is
// hashed again. The top half of the hash names the partition: the tables index
// their slots by the low bits, so a partition taken from those would crowd its keys
// into a fraction of its own table's slots. Each partition then merges its share of
// every worker, in worker order, into a fresh sink: the keys through
// GetOrInsertHashed, the key values taken straight from the worker's parts, and the
// accumulators through Merge with a sparse remap of the partition's groups alone
// (kernel.SparseRemap). A worker is only read, by every partition at once.
//
// # What it gives up
//
//   - Order. The answer is partition-major: deterministic for an input and a thread
//     count, and not first appearance, which an unordered group-by does not promise.
//     MaintainOrder never runs in parallel.
//   - Memory. Every worker's state stays until the last partition has merged, and
//     the partitions grow to about as much again; the serial fold lets each worker
//     go as it is folded. Under a budget that cannot hold both, it is the serial
//     fold that runs.
//
// It returns a nil Operator, and folds nothing, when it does not apply. The sinks it
// returns are the partitions', for the driver to close.
func (s *hashAggSink) foldPartitioned(ctx context.Context, sinks []Sink, newSink SinkFactory) (Operator, []Sink, error) {
	ws := make([]*hashAggSink, len(sinks))
	total, held := 0, int64(0)
	for i, x := range sinks {
		w, ok := x.(*hashAggSink)
		if !ok || w.ordered || w.frozen || len(w.files) > 0 || len(w.keys) == 0 {
			return nil, nil, nil
		}
		ws[i] = w
		total += w.ids.Len()
		held += w.mem.Used()
	}
	if total < partitionedFoldMin {
		return nil, nil, nil
	}
	if room := s.budget.Room(); room > 0 && s.budget.Used()+held > room {
		return nil, nil, nil
	}

	nParts := len(ws)
	parts := make([]*hashAggSink, 0, nParts)
	var made []Sink
	for range nParts {
		x, err := newSink()
		if err != nil {
			return nil, made, err
		}
		made = append(made, x)
		h, ok := x.(*hashAggSink)
		if !ok {
			return nil, made, uerr.Internalf("physical: a partitioned fold built a %T", x)
		}
		parts = append(parts, h)
	}

	// Route each worker's groups, in id order: routed[w][p] is worker w's share of
	// partition p.
	routed := make([][][]int32, len(ws))
	err := eachConcurrently(len(ws), len(ws), func(w int) error {
		r := make([][]int32, nParts)
		for id := range ws[w].ids.Len() {
			p := kernel.PartitionOf(ws[w].ids.HashAt(int32(id)), nParts)
			r[p] = append(r[p], int32(id))
		}
		routed[w] = r
		return nil
	})
	if err != nil {
		return nil, made, err
	}
	err = eachConcurrently(nParts, nParts, func(p int) error {
		return parts[p].mergeShare(ws, routed, p)
	})
	if err != nil {
		return nil, made, err
	}

	partitionedFolds.Add(1)

	// The workers are folded: their state goes, and their accounts' claim on it.
	for _, w := range ws {
		w.mem.Release()
		w.accBytes = 0
		w.discard()
	}

	// One answer per partition, in partition order.
	runs := make([]*batchRun, 0, nParts)
	for _, h := range parts {
		res, err := h.residentResult(ctx)
		if err != nil {
			return nil, made, err
		}
		h.mem.Retain(res)
		runs = append(runs, &batchRun{b: res, n: h.chunk()})
	}
	return &runOperator{schema: s.schema, src: &partsRun{runs: runs}}, made, nil
}

// mergeShare merges into s, a partition, its share of every worker: Merge, for the
// groups routed[w][p] of each worker w, in worker order, through a sparse remap.
func (s *hashAggSink) mergeShare(ws []*hashAggSink, routed [][][]int32, p int) error {
	n := 0
	for w := range ws {
		n += len(routed[w][p])
	}
	s.ids.Reserve(n)
	var (
		ids      [kernel.ManyChunk]int32
		inserted [kernel.ManyChunk]bool
		dst      []int32
		newRows  []int32
	)
	for w, o := range ws {
		share := routed[w][p]
		if len(share) == 0 {
			continue
		}
		dst, newRows = dst[:0], newRows[:0]
		for lo := 0; lo < len(share); lo += kernel.ManyChunk {
			chunk := share[lo:min(lo+kernel.ManyChunk, len(share))]
			s.ids.insertFrom(o.ids, chunk, ids[:], inserted[:])
			for j, oid := range chunk {
				dst = append(dst, ids[j])
				if inserted[j] {
					newRows = append(newRows, oid)
				}
			}
		}
		// Sparse: the worker's other groups are other partitions', and a dense remap
		// would walk them all, for every partition.
		rm := kernel.SparseRemap(share, dst)
		if len(newRows) > 0 {
			part, err := o.takeKeys(newRows, s.keySchema)
			if err != nil {
				return err
			}
			s.keyParts = append(s.keyParts, part)
			s.mem.Retain(part)
		}
		for i := range s.accs {
			if err := s.accs[i].Merge(o.accs[i], rm); err != nil {
				return err
			}
		}
	}
	var acc int64
	for _, a := range s.accs {
		acc += a.NBytes()
	}
	acc += s.ids.NBytes()
	s.mem.RetainBytes(acc - s.accBytes)
	s.accBytes = acc
	return nil
}

// takeKeys gathers the key values of the groups ids, ascending, from the parts that
// hold them, named as schema names them: each part holds the groups of one batch,
// in id order, so a group's part and its row in it follow from the parts' sizes.
// Unlike Merge it does not concatenate the parts first, so a partitioned fold does
// not hold every worker's keys twice.
func (s *hashAggSink) takeKeys(ids []int32, schema *dtype.Schema) (*data.Batch, error) {
	var pieces []*data.Batch
	start, i := int32(0), 0
	for _, part := range s.keyParts {
		if i == len(ids) {
			break
		}
		end := start + int32(part.Rows())
		j := i
		for j < len(ids) && ids[j] < end {
			j++
		}
		if j > i {
			local := make([]int32, j-i)
			for k, id := range ids[i:j] {
				local[k] = id - start
			}
			cols := make([]*data.Column, part.NumCols())
			for c := range cols {
				taken, err := kernel.Take(part.Column(c), local)
				if err != nil {
					return nil, err
				}
				cols[c] = taken.Rename(schema.Field(c).Name)
			}
			b, err := data.NewBatch(schema, cols)
			if err != nil {
				return nil, err
			}
			pieces = append(pieces, b)
		}
		i, start = j, end
	}
	if i != len(ids) {
		return nil, uerr.Internalf("physical: %d of %d groups have no key part", len(ids)-i, len(ids))
	}
	if len(pieces) == 1 {
		return pieces[0], nil
	}
	return kernel.Concat(schema, pieces)
}

// partsRun serves several answers in order, each cut to its run's batch size.
type partsRun struct{ runs []*batchRun }

func (r *partsRun) Next() (*data.Batch, error) {
	for len(r.runs) > 0 {
		b, err := r.runs[0].Next()
		if errors.Is(err, io.EOF) {
			r.runs = r.runs[1:]
			continue
		}
		return b, err
	}
	return nil, io.EOF
}

func (r *partsRun) Close() error { return nil }

// chunk is the output batch size, defaulting when the sink was built by hand.
func (s *hashAggSink) chunk() int {
	if s.batch <= 0 {
		return DefaultOptions().BatchSize
	}
	return s.batch
}
