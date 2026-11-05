package physical

import (
	"context"
	"errors"
	"os"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// --- the build side ---------------------------------------------------------------

type joinBuildSink struct {
	out    *dtype.Schema // the JOIN's output schema
	layout *plan.JoinLayout
	left   *dtype.Schema // probe side's own schema
	right  *dtype.Schema // build side's own schema

	keys     []expr.Node // RIGHT-side key expressions
	leftKeys []expr.Node // LEFT-side key expressions, carried to the probe operator
	spec     joinSpec

	// pairLayout is the namespace spec.residual is written in — both sides, right
	// names suffixed on collision — which for Semi and Anti is NOT `layout`, whose
	// schema is the left frame alone. nil when there is no residual.
	//
	// It must be copied in newSub. A replayed spill bucket that lost it would
	// evaluate nothing and answer a different question with no error anywhere.
	pairLayout *plan.JoinLayout

	// threads is how many probe workers to run, and is set ONLY by planJoin. newSub
	// builds its sub-sink field by field and does not carry it, which is what keeps
	// spill replay serial without anyone having to remember to zero it — a replay is
	// one bucket at a time anyway, and its probe side is a spill file read serially.
	threads int

	// buildOrd names the build side's last column, its rows numbered as they arrive,
	// on an ordered Right or Full join (step 314); empty otherwise. orderedProbe
	// sorts a spilled join's unmatched build rows back by it. A sub-sink needs it
	// not: newSub leaves it out.
	buildOrd string

	// deferred is set by planJoin when the build may run partitioned: Consume only
	// retains the batches, and freeze inserts their keys, one table per partition,
	// each on its own goroutine (buildPartitioned). Past half the budget, catchUp
	// turns it back into the streaming build, the one that can spill.
	deferred bool

	ids    *kernel.KeyTable
	counts []int32

	// ints, where set, keys the streaming build by its one integer key in place of
	// ids, never encoded (step 398, B4 of v0.8-scope.md): under a limit the caller
	// set, or once a deferred build catches up, a join encoded every key, where the
	// partitioned build keyed them so since step 159. planJoin sets it; a split routes
	// by the encoded bytes, so escalate and Merge turn it into ids first, each id
	// kept (bytesKeys), and a spill's sub-sinks key by bytes.
	ints       *kernel.IntKeyTable
	intScratch []int64

	rowKey []int32
	parts  []*data.Batch
	nRows  int
	chunk  keyChunk // admit's scratch

	// publish is the slot this join's built keys go to for a RuntimeFilter on its
	// probe side, or nil (step 167). Only integer key tables are published.
	publish *plan.RuntimeSlot
	// built is the partitioned build's tables once built, by publishEarly or freeze.
	built *builtKeys

	// keyParts are a deferred Semi or Anti join's evaluated keys, a batch of them for
	// each build batch, which it keeps in place of the batches (step 161): it reads
	// its build rows for their keys alone. keySchema names their columns.
	keyParts  []*data.Batch
	keySchema *dtype.Schema

	// mem accounts everything this sink holds across batches: the retained build
	// batches, rowKey (one int32 per build row whether or not the rows themselves
	// are kept), counts, and the ids map.
	//
	// ids was missing until step 13, and for a high-cardinality build side it is the
	// DOMINANT term — a map entry plus a copied key string is tens of bytes per
	// distinct key, against eight for an Int64 key column. A budget that cannot see
	// it fires late or not at all, which is exactly wrong for the operator whose
	// whole memory story is "one entry per distinct key".
	mem        *execopt.Account
	stateBytes int64
	tableBytes int64 // the CSR arrays, charged at freeze

	finished bool

	// --- spilling (extjoin.go) ------------------------------------------------
	budget *execopt.Budget // for SpillDir and NoteSpill; the other two sinks carry the same
	level  int             // radix depth; 0 for the sink planJoin builds
	split  splitState      // how much of the build side has gone to disk

	dir      string // spill directory; only the level-0 sink removes it
	dirOwner bool
	prefix   string        // filename prefix, derived from the parent's
	bparts   []*partWriter // one build writer per bucket, opened lazily
	bfiles   []string      // closed build files, indexed BY BUCKET so the probe can pair them
	pend     [][]int32     // scratch: this batch's rows bound for each bucket
	keep     []int32       // scratch: this batch's rows that stay resident
	nBuild   int           // resident build rows, which is what rowKey indexes

	// A cross join past its limit writes its build side here (crossspill.go).
	crossW    *spill.Writer
	crossFile string
}

func (s *joinBuildSink) Schema() *dtype.Schema { return s.out }

func (s *joinBuildSink) Close() error {
	errs := []error{s.closeBuildParts()}
	if s.crossW != nil {
		errs = append(errs, s.crossW.Close())
		s.crossW = nil
	}
	s.mem.Release()
	// Only the level-0 sink owns the directory. joinBreaker closes `out` before the
	// builder, so every reader the replay opened is already shut by the time this
	// runs — the ordering sortSink.Close relies on for the same reason.
	if s.dirOwner && s.dir != "" {
		dir := s.dir
		s.dir, s.bfiles = "", nil
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, uerr.Wrap(err, uerr.KindIO, "join",
				"removing spill directory %s", dir))
		}
	}
	return errors.Join(errs...)
}

func (s *joinBuildSink) Consume(ctx context.Context, in *data.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := in.Rows()
	if n == 0 {
		return nil
	}

	switch {
	case s.deferred && !s.spec.needBuildRows:
		// A Semi or Anti join's keys wait for freeze; its rows are never read.
		kb, err := s.keyBatch(ctx, in)
		if err != nil {
			return err
		}
		s.keyParts = append(s.keyParts, kb)
		s.mem.Retain(kb)
		s.nBuild += n

	case s.deferred:
		// The keys wait for freeze, which inserts them partitioned.
		s.parts = append(s.parts, in)
		s.mem.Retain(in)
		s.nBuild += n

	case s.crossW != nil:
		// A cross join past its limit: its build side goes to disk (crossspill.go).
		if err := s.crossW.Write(in); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "join", "spilling a cross join's build side")
		}
		s.nBuild += n

	case len(s.keys) == 0:
		// Cross join: one key, every row in it. The same shape hashAggSink uses for
		// a global aggregate, which puts every row in group 0 and creates group 0
		// lazily. It never splits; past its limit a pure cross join spills its build
		// side whole instead (crossspill.go), and any other refuses (overBudget).
		if s.ids.Len() == 0 {
			s.ids.GetOrInsert(nil)
			s.counts = append(s.counts, 0)
		}
		if s.spec.tracksRows() {
			s.rowKey = kernel.Extend(s.rowKey, len(s.rowKey)+n, 0)
			s.counts[0] += int32(n)
		}
		if s.spec.needBuildRows {
			s.parts = append(s.parts, in)
			s.mem.Retain(in)
		}
		s.nBuild += n

	case s.split == splitNone:
		// Nothing has overflowed, so every row is resident and this is the path a
		// join with no memory limit has always taken.
		if err := s.admit(ctx, in, s.nRows); err != nil {
			return err
		}

	default:
		if err := s.classify(ctx, in); err != nil {
			return err
		}
	}
	s.nRows += n
	s.reaccount()

	// Only the streaming build can spill, so past half the budget a deferred one
	// becomes it, before anything below could need to split.
	if s.deferred && overHalf(s) {
		if err := s.catchUp(ctx); err != nil {
			return err
		}
	}

	// The split is decided at a BATCH BOUNDARY, so no batch is ever half-split. The
	// cost is that the resident set overshoots by up to one batch; the benefit is
	// that the invariant every proof here rests on — a key is wholly resident or
	// wholly routed — is visible rather than argued.
	if s.split != splitAll && s.budget.Limit() > 0 && len(s.keys) > 0 && s.mem.Over() {
		hold, err := s.oneKeyOver()
		if err != nil {
			return err
		}
		if !hold {
			if err := s.escalate(ctx); err != nil {
				return err
			}
		}
	}
	return s.overBudget()
}

// Merge folds another sink's partial state in.
//
// Unlike hashAggSink.Merge — whose recorded gap is that it cannot remap group ids
// because accumulator state is indexed by them — this remap is exact: the ids map
// is invertible, and a join's per-key state is a row COUNT rather than
// accumulator state. Rows are identified by position in the concatenated build
// batch, so appending other's parts makes its local row j become global row
// nRows+j — a single offset.
func (s *joinBuildSink) Merge(other Sink) error {
	o, ok := other.(*joinBuildSink)
	if !ok {
		return uerrInternal("joinBuildSink", other)
	}
	if s.finished || o.finished {
		// freeze builds the CSR arrays from rowKey and counts, which this rewrites.
		return uerr.Internalf("physical: joinBuildSink.Merge after Finish")
	}
	// Two sinks that have each partitioned to disk hold files this fold would
	// silently drop, and inserting a key into a table that has already split breaks
	// the invariant the whole design rests on — that a key is wholly resident or
	// wholly routed. sortSink.Merge and hashAggSink.Merge both have this guard.
	if s.split != splitNone || o.split != splitNone {
		return uerr.Internalf("physical: cannot merge joinBuildSinks that have spilled")
	}
	if err := s.bytesKeys(); err != nil {
		return err
	}
	if err := o.bytesKeys(); err != nil {
		return err
	}

	// Visited in o's own ID ORDER. Ranging a Go map here made two things vary run
	// to run: the ids this sink assigned to o's new keys, and — when Validate is on
	// — WHICH duplicate key the error below happened to name first.
	nOther := o.ids.Len()
	remap := make([]int32, nOther)
	for oid := range nOther {
		id, inserted := s.ids.GetOrInsert(o.ids.KeyAt(int32(oid)))
		if !inserted && s.spec.validate.RequiresRightUnique() {
			// A key unique within each partial sink can still be duplicated across
			// them. Omitting this makes Validate silently weaker under parallelism.
			return duplicateKeyErr(s.spec, true, s.keys, -1)
		}
		remap[oid] = id
	}

	s.counts = kernel.Extend(s.counts, s.ids.Len(), 0)

	// `other` consumed a LATER portion of the input, so appending preserves build
	// order — which is what makes the emitted match order, and the Right/Full flush
	// order, agree with a single-sink run.
	base := len(s.rowKey)
	s.rowKey = kernel.Extend(s.rowKey, base+len(o.rowKey), 0)
	for j, oid := range o.rowKey {
		if oid == noKey {
			s.rowKey[base+j] = noKey
			continue
		}
		id := remap[oid]
		s.counts[id]++
		s.rowKey[base+j] = id
	}
	s.parts = append(s.parts, o.parts...)
	s.keyParts = append(s.keyParts, o.keyParts...)
	s.nRows += o.nRows

	// Merge used to do no accounting at all: it appended another sink's retained
	// batches without retaining them and left stateBytes stale, so a merged sink
	// under-reported by everything the other one held.
	for _, b := range o.parts {
		s.mem.Retain(b)
	}
	for _, b := range o.keyParts {
		s.mem.Retain(b)
	}
	st := int64(cap(s.rowKey))*4 + int64(cap(s.counts))*4 + s.keyBytes()
	s.mem.RetainBytes(st - s.stateBytes)
	s.stateBytes = st
	return nil
}

// Finish is Probe against an empty stream. See ProbeBuilder.
func (s *joinBuildSink) Finish(ctx context.Context) (Operator, error) {
	return s.Probe(ctx, emptyOperator{schema: s.left})
}

// Probe returns the probe-side operator, serial or parallel.
//
// The parallel path is a fleet of the SAME operator over one frozen table, so the
// choice is made once here and nothing downstream knows which it got. See
// parallelProbe for the four conditions that force the serial one.
func (s *joinBuildSink) Probe(ctx context.Context, probe Operator) (Operator, error) {
	if s.crossW != nil {
		return s.crossProbe(probe)
	}
	t, err := s.freeze(ctx)
	if err != nil {
		return nil, err
	}
	// The probe side is pulled from here on, so a RuntimeFilter in it sees the keys
	// from its first batch (step 167). A join that could be exchanged published them
	// already, before reading its probe side ahead (publishEarly).
	if s.publish != nil && t.ints != nil && s.publish.Published() == nil {
		s.publish.Publish(t.ints)
	}

	pad, err := emptyBatch(s.left)
	if err != nil {
		return nil, err
	}

	if n := s.parallelProbe(); n > 1 {
		workers := make([]*joinProbeOp, n)
		for i := range n {
			// Each worker gets its own cursor and selection arrays and shares only the
			// table. probe is nil on a worker: it is HANDED batches by parProbeOp
			// rather than pulling them, which is the whole difference.
			workers[i] = s.newProbeOp(t, nil, pad)
		}
		return newParProbeOp(s.out, probe, workers), nil
	}

	op := s.newProbeOp(t, probe, pad)
	op.account()
	return op, nil
}

// parallelProbe gives the worker count, or 1 when this join must stay serial.
//
// Three kinds of state make a join unparallelisable, and each is a real dependency
// rather than caution:
//
//   - Right and Full write `matched` during the probe and read it during the flush.
//     Per-worker arrays ORed before the flush would work, and needs a flush phase
//     that runs after every worker drains — a second mechanism, not in this step.
//
//   - A one-to-one or one-to-many validation keeps `seen`, a map over the WHOLE
//     probe stream that detects a duplicate left key across batches. Sharding it
//     would change what it detects, not just how fast it detects it.
//
//   - A spilled build side means the probe writes rows to per-bucket files through
//     routeProbe. Concurrent writers would corrupt them, and replay is one bucket at
//     a time regardless.
//
// What is left — Inner, Left, Semi, Anti, unvalidated, resident — is every join in
// h2o and every join in PDS-H.
func (s *joinBuildSink) parallelProbe() int {
	switch {
	case s.threads <= 1,
		s.spec.emitBuildUnmatched,
		s.spec.validate.RequiresLeftUnique(),
		s.spilled():
		return 1
	}
	return s.threads
}

func (s *joinBuildSink) newProbeOp(t *joinTable, probe Operator, pad *data.Batch) *joinProbeOp {
	op := &joinProbeOp{
		schema:  s.out,
		layout:  s.layout,
		probe:   probe,
		t:       t,
		spec:    s.spec,
		keys:    s.leftKeys,
		leftPad: pad,
		// Per worker, so nothing here is shared: the residual's scratch is exactly
		// as private as lsel/rsel, which the parallel probe's doc says is the whole
		// reason N workers can share one frozen table with no lock.
		pairLayout: s.pairLayout,
	}
	op.mem = s.mem
	op.sink, op.level, op.routes = s, s.level, s.spilled()
	op.pparts = make([]*partWriter, nBuckets)
	op.pfiles = make([]string, nBuckets)
	op.ppend = make([][]int32, nBuckets)
	if s.spec.emitBuildUnmatched {
		// Per KEY id, not per build row: if a key is matched at all, every build row
		// under it is emitted. Smaller by the average fan-out factor, and identical
		// in meaning.
		//
		// That equivalence holds only WITHOUT a residual. With one, "matched" is a
		// per-PAIR verdict — two build rows can share a key and disagree — so a
		// per-key bool cannot express it. Right and Full are the only kinds that
		// allocate this and resolveJoin refuses a residual on both, so the two never
		// meet; if that ever changes, this array has to become per build row.
		op.matched = make([]bool, t.nKeys)
	}
	if s.spec.validate.RequiresLeftUnique() {
		op.seen = make(map[string]struct{})
	}
	return op
}

// freeze scatters the CSR arrays and concatenates the build side.
func (s *joinBuildSink) freeze(ctx context.Context) (*joinTable, error) {
	s.finished = true
	if err := s.closeBuildParts(); err != nil {
		return nil, err
	}
	nKeys := s.ids.Len()
	t := &joinTable{ids: s.ids, nKeys: nKeys}
	if s.ints != nil {
		nKeys = s.ints.Len()
		t = &joinTable{ints: kernel.NewIntKeyParts([]*kernel.IntKeyTable{s.ints}), nKeys: nKeys}
	}
	if s.deferred {
		keys, err := s.partitionedKeys(ctx)
		if err != nil {
			return nil, err
		}
		nKeys = keys.Len()
		t = &joinTable{nKeys: nKeys}
		switch {
		case keys.ints != nil:
			t.ints = keys.ints
		case keys.words != nil:
			t.words, t.pack = keys.words, keys.pack
		default:
			t.ids = keys.bytes
		}
		// A Semi or Anti join's keys are in the tables now; the rebase below drops
		// their charge.
		s.keyParts = nil
	}

	// Rebase UNCONDITIONALLY. This release used to sit inside the needBuildRows
	// block below, so Semi and Anti never reached it: their stateBytes stayed
	// charged for the whole probe phase even though the arrays it counted were set
	// to nil three lines from the end of this function. Phantom bytes in a shared
	// ledger delay every other operator's spill decision, not just this one's.
	s.mem.Release()
	s.stateBytes = 0

	if s.spec.needBuildRows {
		switch len(s.parts) {
		case 0:
			// Concat over ZERO batches yields a zero-COLUMN batch, which is the trap
			// hashAggSink.Finish documents. A build side legitimately sees no rows,
			// and a Left join over it must still emit one null column per right field.
			b, err := emptyBatch(s.right)
			if err != nil {
				return nil, err
			}
			t.build = b
		default:
			// Owned: the parts are let go column by column as each is copied, so the
			// build side is not held twice at the moment this operator is largest.
			b, err := kernel.ConcatOwned(s.right, s.parts)
			if err != nil {
				return nil, err
			}
			t.build = b
		}
		s.parts = nil
		// The concat builds a second copy of the build side, overlapping the parts
		// by one column now (step 128), and the retained source batches are dropped
		// in exchange. Re-basing the account on the table rather than on the parts is
		// what stops MemoryStats.Peak under-reporting a join by one whole copy of
		// its input.
		s.mem.Retain(t.build)

		t.off = make([]int32, nKeys+1)
		for id, c := range s.counts {
			t.off[id+1] = t.off[id] + c
		}
		// off[nKeys], not nRows: rows whose key was null are absent from the table.
		t.rows = make([]int32, t.off[nKeys])
		cursor := append([]int32(nil), t.off[:nKeys]...)
		for j, id := range s.rowKey {
			if id == noKey {
				continue
			}
			t.rows[cursor[id]] = int32(j)
			cursor[id]++
		}
	}

	if s.spec.emitBuildUnmatched {
		t.rowKey = s.rowKey
	}

	// Charge the table's own arrays, which the rebase above just dropped and which
	// nothing charged before step 13. For a narrow build side they are not a
	// rounding error: one Int64 key column is 8 bytes a row, and off + rows + rowKey
	// are 8 to 12 — so the account was under-reporting by more than half, in the
	// operator budget.go cites as its motivating example for RetainBytes existing.
	s.tableBytes = int64(cap(t.off))*4 + int64(cap(t.rows))*4 + int64(cap(t.rowKey))*4
	// And the keys the probe looks up: hash tables, a direct index (step 203), or a
	// dense build's index alone (step 204). The rebase dropped their charge, and the
	// probe holds them to its end.
	switch {
	case t.ints != nil:
		s.tableBytes += t.ints.NBytes()
	case t.words != nil:
		s.tableBytes += t.words.NBytes()
	case t.ids != nil:
		s.tableBytes += t.ids.NBytes()
	}
	s.mem.RetainBytes(s.tableBytes)
	s.rowKey, s.counts = nil, nil
	return t, nil
}
