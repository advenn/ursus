package physical

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// ProbeBuilder is the BUILD half of a two-input pipeline breaker.
//
// It is a Sink — the whole Consume/Merge/Finish contract, unchanged — plus one
// method. Sink is deliberately not widened: a one-input breaker genuinely has one
// child, and making every sink carry a second would be the cost without the
// benefit.
//
// Finish and Probe are two views of one result:
//
//	Finish(ctx)  ==  Probe(ctx, an operator that yields no rows)
//
// That is not a stub. It is the join of the build side against an empty probe
// side: zero rows for Inner/Left/Semi/Anti/Cross, and every build row
// null-padded for Right/Full. Keeping Finish total rather than
// panic("use Probe") keeps Sink's contract honest and gives the suite a free
// equivalence — see TestJoinFinishEqualsProbeOfEmpty.
type ProbeBuilder interface {
	Sink

	// Probe returns the operator that streams the probe side and emits the join.
	// Called ONCE, after the last Consume, INSTEAD of Finish.
	//
	// OWNERSHIP: the returned Operator does NOT own probe. joinBreaker owns both
	// children and closes both; the returned operator's Close releases only its
	// own state. An operator that also closed probe would double-close it.
	Probe(ctx context.Context, probe Operator) (Operator, error)
}

// joinBreaker is breaker's two-input sibling.
//
// breaker cuts a linear pipeline: consume everything, Finish, delegate. A join
// cuts a DAG — the build side must reach EOF before the probe side is pulled at
// all, and then the probe side streams. That asymmetry has to live somewhere, and
// here is better than a `children []Operator` where index 0 secretly means "drain
// me" and index 1 means "stream me".
type joinBreaker struct {
	build   Operator
	probe   Operator
	builder ProbeBuilder
	out     Operator
	done    bool
}

func (j *joinBreaker) Schema() *dtype.Schema { return j.builder.Schema() }

func (j *joinBreaker) Next(ctx context.Context) (*data.Batch, error) {
	if !j.done {
		if err := j.drain(ctx); err != nil {
			return nil, err
		}
		j.done = true
	}
	return j.out.Next(ctx)
}

func (j *joinBreaker) drain(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		in, err := j.build.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := j.builder.Consume(ctx, in); err != nil {
			return err
		}
	}
	out, err := j.builder.Probe(ctx, j.probe)
	if err != nil {
		return err
	}
	j.out = out
	return nil
}

// Close releases all three, unconditionally.
//
// Unlike breaker's `out` — a batchOperator with no child — this `out` WRAPS the
// probe side. If it also closed the probe, the probe would be closed twice.
// Sources are idempotent about that today, but relying on every future source
// staying idempotent is how a double-close ships. One flat rule, no conditional:
// joinBreaker owns all three, `out` owns nothing.
func (j *joinBreaker) Close() error {
	var errs []error
	if j.out != nil {
		errs = append(errs, j.out.Close())
	}
	return errors.Join(append(errs,
		j.builder.Close(), j.build.Close(), j.probe.Close())...)
}

// --- the table ------------------------------------------------------------------

// seenBytesPerKey is what one entry in the probe side's map[string]struct{} costs
// beyond the key bytes: a 16-byte string header, a tophash byte, and Go's
// load-factor slack, which keeps a map roughly a third empty.
//
// An estimate, and deliberately so. Measuring exactly would mean walking every key
// on every batch, which is the trade nuniqueAcc.NBytes documents making in the other
// direction. Being approximately right is the point; being ZERO — which is what this
// was until step 13 — is the bug.
//
// It used to cover the build side's ids map as well, at 48 bytes for the extra
// 4-byte value. That map is now a kernel.KeyTable, which owns four slices and
// reports NBytes EXACTLY, so no estimate is involved on that side any more.
const seenBytesPerKey = 44

// noKey marks a build row whose key contained a null while NullsEqual is off. It
// is in no bucket, so it matches nothing — and Right/Full still emit it unmatched.
const noKey = int32(-1)

// joinTable is what the build side produces and the probe side reads.
//
// # Why CSR and not map[string][]int32
//
// A join key repeats where a group-by key does not, so the table maps a key to a
// LIST of build rows. A map to a slice costs a slice header plus a backing array
// per distinct key: at 5M keys that is ~120 MB of headers, 5M allocations, and 5M
// pointers for the GC to trace on every cycle.
//
// Compressed sparse row instead: two flat, pointer-free slices scattered once at
// freeze. A probe is one map lookup and then a CONTIGUOUS slice, already in
// ascending build order — no chain to walk, no reversal pass.
//
// This doc used to add "it also makes the Validate check off[id+1]-off[id] > 1,
// which is O(1) per key". THAT CHECK DOES NOT EXIST and never did: right-side
// uniqueness is checked incrementally in Consume, where a duplicate is a map hit,
// and it has to be — the CSR is not built until freeze, which is after the last
// build row. A justification for a data structure should name a use the code makes.
//
// Nothing mutates it after freeze, which is what would let a future morsel
// scheduler share one table across N probe workers with no lock.
type joinTable struct {
	ids   *kernel.KeyTable // encoded key -> key id
	off   []int32          // len nKeys+1; rows[off[id]:off[id+1]] are id's build rows
	rows  []int32          // build row indices, ascending within each key
	nKeys int

	// build holds every build row, addressable by one index. nil for Semi/Anti,
	// which never emit a right column.
	build *data.Batch

	// rowKey[j] is build row j's key id, or noKey. Kept only for Right/Full, whose
	// flush walks build rows in INPUT order.
	rowKey []int32
}

// --- the spec -------------------------------------------------------------------

// joinSpec is the per-kind behaviour reduced to flags, so one probe loop serves
// all seven kinds.
//
// needBuildRows's "their build memory is O(keys)" was FALSE by an O(rows) term
// until step 13: rowKey was appended for every build row and counts for every key,
// and Semi/Anti read neither — freeze hands rowKey to the table only when
// emitBuildUnmatched, and consumes counts only when needBuildRows. Four bytes a row
// of charged, retained, unread state. See tracksRows.
//
// Read the flags as identities: Right is Inner plus a build-unmatched flush, and
// Full is Left plus the same flush. That is why there is no input swap — swapping
// would put the LEFT input on the build side, so a right join of a large fact
// table against a small dimension would silently buffer the large one.
type joinSpec struct {
	kind       plan.JoinKind
	nullsEqual bool
	validate   plan.JoinValidation
	batchSize  int

	emitProbeUnmatched bool // Left, Full, Anti
	emitBuildUnmatched bool // Right, Full
	needBuildRows      bool // false for Semi/Anti, UNLESS they carry a residual
	maskOnly           bool // Semi/Anti emit probe columns only, at most one per row
	emitMatched        bool // false for Anti

	// residual is evaluated per candidate PAIR before the match verdict, and is
	// what makes a semi join answer "does a partner satisfying p exist" rather than
	// "does a partner exist".
	//
	// It is why needBuildRows is no longer a pure function of the kind. Reading a
	// right-hand value requires the rows to be there, so a residual semi join
	// retains its build side and its memory becomes O(build rows) like every other
	// kind's. That is a real cost and it is the price of the feature: without the
	// rows there is nothing for the predicate to look at.
	residual []expr.Node
}

// tracksRows reports whether this kind needs the per-build-row rowKey array.
//
// Only Right and Full read it — flushStep walks it — and only the needBuildRows
// kinds read counts, which freeze prefix-sums into the CSR. Semi and Anti are
// neither, so they used to build BOTH for every build row and discard them unread,
// which is what made needBuildRows's doc ("their build memory is O(keys)") false by
// an O(rows) term. The bytes were charged, so they also decided when the budget
// fired for exactly the two kinds whose memory is smallest.
func (s joinSpec) tracksRows() bool { return s.needBuildRows || s.emitBuildUnmatched }

func newJoinSpec(j *plan.Join, batchSize int) joinSpec {
	k := j.Kind
	filters := k == plan.JoinSemi || k == plan.JoinAnti
	return joinSpec{
		kind:               k,
		nullsEqual:         j.NullsEqual,
		validate:           j.Validate,
		batchSize:          batchSize,
		emitProbeUnmatched: k == plan.JoinLeft || k == plan.JoinFull || k == plan.JoinAnti,
		emitBuildUnmatched: k == plan.JoinRight || k == plan.JoinFull,
		needBuildRows:      !filters || j.HasResidual(),
		maskOnly:           filters,
		emitMatched:        k != plan.JoinAnti,
		residual:           j.Residual,
	}
}

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

	ids    *kernel.KeyTable
	counts []int32
	rowKey []int32
	parts  []*data.Batch
	nRows  int

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
}

func (s *joinBuildSink) Schema() *dtype.Schema { return s.out }

func (s *joinBuildSink) Close() error {
	errs := []error{s.closeBuildParts()}
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
	case len(s.keys) == 0:
		// Cross join: one key, every row in it. The same shape hashAggSink uses for
		// a global aggregate, which puts every row in group 0 and creates group 0
		// lazily. It never splits — see overBudget.
		if s.ids.Len() == 0 {
			s.ids.GetOrInsert(nil)
			s.counts = append(s.counts, 0)
		}
		if s.spec.tracksRows() {
			for range n {
				s.rowKey = append(s.rowKey, 0)
			}
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

	// The split is decided at a BATCH BOUNDARY, so no batch is ever half-split. The
	// cost is that the resident set overshoots by up to one batch; the benefit is
	// that the invariant every proof here rests on — a key is wholly resident or
	// wholly routed — is visible rather than argued.
	if s.split != splitAll && s.budget.Limit() > 0 && len(s.keys) > 0 && s.mem.Over() {
		if err := s.escalate(ctx); err != nil {
			return err
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

	// Visited in o's own ID ORDER. Ranging a Go map here made two things vary run
	// to run: the ids this sink assigned to o's new keys, and — when Validate is on
	// — WHICH duplicate key the error below happened to name first.
	nOther := o.ids.Len()
	remap := make([]int32, nOther)
	for oid := range nOther {
		id, inserted := s.ids.GetOrInsert(o.ids.KeyAt(int32(oid)))
		if inserted {
			s.counts = append(s.counts, 0)
		} else if s.spec.validate.RequiresRightUnique() {
			// A key unique within each partial sink can still be duplicated across
			// them. Omitting this makes Validate silently weaker under parallelism.
			return duplicateKeyErr(s.spec.validate, "right", s.right, s.keys, -1)
		}
		remap[oid] = id
	}

	// `other` consumed a LATER portion of the input, so appending preserves build
	// order — which is what makes the emitted match order, and the Right/Full flush
	// order, agree with a single-sink run.
	for _, oid := range o.rowKey {
		if oid == noKey {
			s.rowKey = append(s.rowKey, noKey)
			continue
		}
		id := remap[oid]
		s.counts[id]++
		s.rowKey = append(s.rowKey, id)
	}
	s.parts = append(s.parts, o.parts...)
	s.nRows += o.nRows

	// Merge used to do no accounting at all: it appended another sink's retained
	// batches without retaining them and left stateBytes stale, so a merged sink
	// under-reported by everything the other one held.
	for _, b := range o.parts {
		s.mem.Retain(b)
	}
	st := int64(cap(s.rowKey))*4 + int64(cap(s.counts))*4 + s.ids.NBytes()
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
	t, err := s.freeze()
	if err != nil {
		return nil, err
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
	op.sink, op.level = s, s.level
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
func (s *joinBuildSink) freeze() (*joinTable, error) {
	s.finished = true
	if err := s.closeBuildParts(); err != nil {
		return nil, err
	}
	nKeys := s.ids.Len()
	t := &joinTable{ids: s.ids, nKeys: nKeys}

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
			b, err := kernel.Concat(s.right, s.parts)
			if err != nil {
				return nil, err
			}
			t.build = b
		}
		s.parts = nil
		// The concat allocated a SECOND full copy of the build side, at the moment
		// this operator is largest, and the retained source batches are dropped in
		// exchange. Re-basing the account on the table rather than on the parts is
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
	s.mem.RetainBytes(s.tableBytes)
	s.rowKey, s.counts = nil, nil
	return t, nil
}

// --- the probe side ---------------------------------------------------------------

// joinProbeOp streams the probe side and emits the join.
//
// # The output is a sequence; BatchSize only chooses the cuts
//
//	[ (i, r) for i in probe order, for r in t.rows[off[id_i] : off[id_i+1]] ]
//	  ++ [ (NullIndex, j) for j in build order, unmatched ]
//
// Next returns the next <= BatchSize elements of that sequence. The sequence is a
// function of (probe row order, build row order) only — neither depends on input
// batching — so results are batch-size invariant by construction.
//
// That is why row, hit and entered exist: one probe row can match thousands of
// build rows, so a single row's match list has to resume across Next calls rather
// than being materialised whole.
type joinProbeOp struct {
	schema *dtype.Schema
	layout *plan.JoinLayout
	probe  Operator
	t      *joinTable
	spec   joinSpec
	keys   []expr.Node // LEFT-side key expressions

	mu sync.Mutex

	cur     *data.Batch
	curOK   bitmap.View
	enc     *kernel.GroupKeyEncoder
	row     int
	hit     int32
	nHit    int32
	entered bool

	lsel, rsel []int32

	// csel/rcand are the residual's candidate pair vectors, kept separate from
	// lsel/rsel because those two carry the OUTPUT and these carry a question. Per
	// worker, like lsel/rsel, since each worker resolves its own probe rows.
	csel, rcand []int32
	// pend, rowAny and alive are stepResidual's scratch: the probe rows whose
	// verdict this chunk decides, their folded answers, and the per-pair AND of the
	// conjuncts. Reused across chunks so the loop allocates nothing steady-state.
	pend       []int32
	rowAny     []bool
	alive      []bool
	pairLayout *plan.JoinLayout

	matched []bool
	seen    map[string]struct{}

	// mem is the BUILD sink's account, shared rather than separate: the probe side
	// holds no batches of its own, only two arrays whose size is a function of the
	// build table, and splitting them across two accounts would report one operator
	// as two. Neither was charged at all before step 13 — and seen is the only
	// structure in the join that grows with the PROBE side, one entry per distinct
	// left key, held for the whole probe phase.
	mem        *execopt.Account
	probeBytes int64

	// --- spilling (extjoin.go) ------------------------------------------------
	sink   *joinBuildSink // for newSub, the spill directory and the build files
	routed bool           // this probe row went to a file; emit nothing for it
	pparts []*partWriter  // one probe writer per bucket, opened lazily
	pfiles []string       // closed probe files, indexed by bucket
	ppend  [][]int32      // scratch: this batch's rows bound for each bucket

	level int      // radix depth; must match the sink's, or the two route apart
	next  int      // the next build bucket to replay
	rep   Operator // the live sub-join's output, or the null bucket's streamer
	sub   *joinBuildSink

	leftPad *data.Batch
	flushJ  int
	phase   int8
}

const (
	phaseProbing int8 = iota
	phaseFlushing
	// phaseReplay re-joins the spilled buckets, one at a time. Reached only when the
	// build side actually wrote files — a sink can split and route nothing, and then
	// the table is complete and there is nothing to replay.
	phaseReplay
	phaseDone
)

func (p *joinProbeOp) Schema() *dtype.Schema { return p.schema }

// Close releases this operator's state, including anything the replay opened.
//
// joinBreaker closes the probe child, so that is not here. What IS here is new: a
// spilling probe holds partition writers, a spill reader and a live sub-join, and a
// leaked reader means os.RemoveAll runs over an open file. The rule is the flat one
// mergeOperator and concatOp use — close everything, including what was never
// reached — because a conditional close is a leak waiting for an early break.
func (p *joinProbeOp) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	errs := []error{p.closeProbeParts()}
	if p.rep != nil {
		errs = append(errs, p.rep.Close())
	}
	errs = append(errs, closeSink(p.sub))
	p.rep, p.sub = nil, nil
	return errors.Join(errs...)
}

func (p *joinProbeOp) Next(ctx context.Context) (*data.Batch, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch p.phase {
		case phaseProbing:
			b, err := p.probeStep(ctx)
			if err != nil {
				return nil, err
			}
			if b != nil {
				return b, nil
			}
		case phaseFlushing:
			b, err := p.flushStep()
			if err != nil {
				return nil, err
			}
			if b != nil {
				return b, nil
			}
		case phaseReplay:
			b, err := p.replayStep(ctx)
			if err != nil {
				return nil, err
			}
			if b != nil {
				return b, nil
			}
		default:
			return nil, io.EOF
		}
	}
}

// probeStep fills one output batch from the probe stream, or returns (nil, nil)
// when it needs another input batch or the stream ended.
//
// It is fillCurrent then stepCurrent, split at the seam between "get a batch" and
// "drain the batch I have". A parallel probe worker is handed its batch rather than
// pulling one, so it calls only the second half — which is what keeps the resume
// logic below written once. Two copies of hit/nHit would drift, and the symptom
// would be output that changes with batch size.
func (p *joinProbeOp) probeStep(ctx context.Context) (*data.Batch, error) {
	if p.cur == nil {
		ok, err := p.fillCurrent(ctx)
		if err != nil || !ok {
			return nil, err
		}
	}
	return p.stepCurrent(ctx)
}

// fillCurrent pulls the next probe batch. false means the stream ended, and the
// phase has already moved on.
func (p *joinProbeOp) fillCurrent(ctx context.Context) (bool, error) {
	in, err := p.probe.Next(ctx)
	if errors.Is(err, io.EOF) {
		if err := p.closeProbeParts(); err != nil {
			return false, err
		}
		p.phase = p.afterProbe()
		if p.phase == phaseReplay {
			p.releaseTable()
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := p.startBatch(ctx, in); err != nil {
		return false, err
	}
	return true, nil
}

// stepCurrent drains the CURRENT batch by at most one output batch, and clears it
// when the batch is spent. (nil, nil) means this batch produced nothing more.
func (p *joinProbeOp) stepCurrent(ctx context.Context) (*data.Batch, error) {
	if p.spec.maskOnly && len(p.spec.residual) > 0 {
		return p.stepResidual(ctx)
	}
	p.reset()
	for p.row < p.cur.Rows() && p.outLen() < p.spec.batchSize {
		if !p.entered {
			if err := p.enter(); err != nil {
				return nil, err
			}
		}
		if err := p.appendMatches(ctx); err != nil {
			return nil, err
		}
		if p.hit >= p.nHit {
			p.row++
			p.entered = false
		}
	}

	done := p.row >= p.cur.Rows()
	out, err := p.emit()
	if done {
		// Route BEFORE p.cur goes: routeProbe gathers out of it, and this is the last
		// moment it is live.
		if rerr := p.routeProbe(); rerr != nil {
			return nil, rerr
		}
		p.cur, p.curOK, p.enc = nil, bitmap.View{}, nil
		p.row = 0
		p.account()
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// stepResidual is stepCurrent for a semi or anti join carrying a residual.
//
// It is a separate loop rather than another branch inside stepCurrent because the
// two resume differently, and mixing them is how hit/nHit/entered would come to
// mean two things. The ordinary loop cuts its OUTPUT at batchSize and resumes
// mid-row; this one cuts its CANDIDATES at batchSize, because the output is at most
// one row per probe row and the expensive thing is the predicate.
//
// A probe row is never split across chunks here: its candidates are gathered whole,
// so the verdict is complete the moment its chunk is evaluated and nothing has to
// survive a Next boundary. The exception is a row whose fan-out alone exceeds the
// chunk — that one is resolved by residualMatch, which chunks within the row and
// keeps memory bounded. Two paths, each doing the thing it is good at: batching
// across rows when fan-out is small, and within a row when it is enormous.
func (p *joinProbeOp) stepResidual(ctx context.Context) (*data.Batch, error) {
	p.reset()
	for p.row < p.cur.Rows() && p.outLen() < p.spec.batchSize {
		p.csel = p.csel[:0]
		p.rcand = p.rcand[:0]
		p.pend = p.pend[:0]
		base := p.row

		for p.row < p.cur.Rows() && len(p.csel) < p.spec.batchSize {
			if !p.entered {
				if err := p.enter(); err != nil {
					return nil, err
				}
			}
			if p.routed {
				// The sub-join owns this row's verdict. Emitting one here as well
				// would duplicate it, which is appendMatches' third-outcome rule.
				p.hit, p.row, p.entered = p.nHit, p.row+1, false
				continue
			}

			need := int(p.nHit - p.hit)
			if need > p.spec.batchSize {
				// One key with more partners than a chunk holds. Resolving it here
				// would make the pair batch as large as the fan-out; residualMatch
				// walks it in bounded pieces instead.
				if len(p.csel) > 0 {
					break // evaluate what is already gathered first
				}
				any, err := p.residualMatch(ctx)
				if err != nil {
					return nil, err
				}
				if any == p.spec.emitMatched {
					p.lsel = append(p.lsel, int32(p.row))
				}
				p.row, p.entered = p.row+1, false
				continue
			}
			// A SIZE bound, not the row-completeness invariant. That invariant is
			// enforced by the unbounded loop below, which always drains a row whole
			// — removing this break changes how big a chunk gets and no answer at
			// all, which a tooth confirmed. Its job is to keep the pair batch near
			// batchSize rather than overshooting it by one row's fan-out.
			if len(p.csel) > 0 && len(p.csel)+need > p.spec.batchSize {
				break
			}

			for p.hit < p.nHit {
				p.csel = append(p.csel, int32(p.row))
				p.rcand = append(p.rcand, p.t.rows[p.hit])
				p.hit++
			}
			p.pend = append(p.pend, int32(p.row))
			p.row, p.entered = p.row+1, false
		}

		if len(p.pend) == 0 {
			continue
		}
		// rowAny spans base..p.row, which is at most one chunk of probe rows.
		p.rowAny = p.rowAny[:0]
		for range p.row - base {
			p.rowAny = append(p.rowAny, false)
		}
		if len(p.csel) > 0 {
			if err := p.residualFold(ctx, base, p.rowAny); err != nil {
				return nil, err
			}
		}
		for _, r := range p.pend {
			// A row with no candidates at all folds to false, which is exactly what
			// "no partner" means — and is why WhereNotExists emits it.
			if p.rowAny[int(r)-base] == p.spec.emitMatched {
				p.lsel = append(p.lsel, r)
			}
		}
	}

	done := p.row >= p.cur.Rows()
	out, err := p.emit()
	if done {
		if rerr := p.routeProbe(); rerr != nil {
			return nil, rerr
		}
		p.cur, p.curOK, p.enc = nil, bitmap.View{}, nil
		p.row = 0
		p.account()
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// startBatch evaluates the probe batch's key columns and prepares the encoder.
func (p *joinProbeOp) startBatch(ctx context.Context, in *data.Batch) error {
	p.cur = in
	p.row = 0
	p.entered = false
	if len(p.keys) == 0 {
		return nil // cross join: no keys to encode
	}
	keyCols, err := evalKeys(ctx, p.keys, in, p.layout.KeyTypes)
	if err != nil {
		return err
	}
	p.curOK = keyValidity(keyCols)
	p.enc, err = kernel.NewGroupKeyEncoder("join", keyCols)
	return err
}

// enter looks up the current probe row's match list.
func (p *joinProbeOp) enter() error {
	p.entered = true
	p.hit, p.nHit = 0, 0
	p.routed = false

	var id int32
	if len(p.keys) == 0 {
		id = 0
		if p.t.nKeys == 0 {
			return nil // cross join against an empty build side
		}
	} else {
		if !p.spec.nullsEqual && !p.curOK.Get(p.row) {
			// A null key matches nothing — without a lookup, so the two NullsEqual
			// settings cannot leak into each other even if a null-keyed build row is
			// somehow present.
			return nil
		}
		k := p.enc.Encode(p.row)
		var ok bool
		// Get, not GetOrInsert: the probe must never add a key. It also mutates
		// nothing, which is what keeps the frozen table safe to read from several
		// probe workers at once.
		id, ok = p.t.ids.Get(k)
		if !ok {
			// Not resident. Three lines decide the rest, and they need no split-state
			// branch:
			//
			//   - a key in ids has ALL its build rows resident, by the invariant;
			//   - a key not in ids has its build rows in bucket b, or nowhere at all;
			//   - so if bucket b has no build file, this key matches nothing.
			//
			// The third case is why no probe file is ever written for a bucket with no
			// build rows — which removes, by construction, the silent row loss of a
			// replay that walked build files and dropped the probe rows sitting beside
			// an absent one. Left, Full and Anti emit those rows here, inline, IN PROBE
			// ORDER, rather than at the end.
			if b := partitionOf(k, p.level); p.buildFile(b) != "" {
				p.ppend[b] = append(p.ppend[b], int32(p.row))
				p.routed = true
			}
			return nil
		}
		// seen is checked only for RESIDENT keys, and only after the lookup. A routed
		// key is the sub-join's to police, so inserting it here would make this map
		// O(all distinct probe keys) instead of O(resident ones) — the unbounded
		// structure this whole step exists to bound.
		if p.seen != nil {
			if _, dup := p.seen[string(k)]; dup {
				return duplicateKeyErr(p.spec.validate, "left", nil, nil, p.row)
			}
			p.seen[string(k)] = struct{}{}
		}
	}
	if p.t.off == nil {
		// Semi/Anti keep no row lists; presence in the table is the whole answer.
		// With a residual they DO keep row lists — needBuildRows is true — so this
		// arm is not taken and the real range below is used instead.
		p.nHit = 1
		return nil
	}
	p.hit, p.nHit = p.t.off[id], p.t.off[id+1]
	if p.matched != nil && p.nHit > p.hit {
		p.matched[id] = true
	}
	return nil
}

// appendMatches emits as much of the current row's match list as fits.
func (p *joinProbeOp) appendMatches(ctx context.Context) error {
	if p.routed {
		// A THIRD outcome, and it has to be explicit. Without it a routed row falls
		// into the !matched arm below, which emits (row, NullIndex) whenever the kind
		// wants unmatched probe rows — so Left, Full and Anti would emit every routed
		// row here AND again from its sub-join. Duplicates, no error, no length
		// mismatch.
		p.hit = p.nHit
		return nil
	}
	matched := p.nHit > p.hit

	// The residual turns "a partner exists" into "a partner satisfying p exists",
	// and it has to be decided HERE, before the verdict below, rather than by
	// filtering the output afterwards. Filtering the output filters left rows that
	// already survived, which answers a different question.
	if p.spec.maskOnly && len(p.spec.residual) > 0 {
		var err error
		if matched, err = p.residualMatch(ctx); err != nil {
			return err
		}
	}

	if p.spec.maskOnly {
		// Semi and Anti emit at most ONE row per probe row and no right column. A
		// semi join implemented by reusing the inner loop below returns THREE rows
		// for one left row against three matching right rows — see
		// TestSemiJoinDoesNotDuplicate.
		//
		// A SELECTION VECTOR, not a mask. The mask this used to build was positionally
		// tied to a CONTIGUOUS run of the probe batch — emit sliced p.cur[p.row-n :
		// p.row] and filtered it — and routing punches holes in that run: a routed row
		// contributes no bit, the window slides left by however many were routed, and
		// the filter lands on the WRONG ROWS. The row count stays right, which is what
		// made it invisible until a spilling fixture compared values past row ten.
		//
		// Appending a bit for routed rows does not fix it either: Anti has emitMatched
		// false, so an unmatched routed row would be selected here AND again by its
		// sub-join.
		if matched == p.spec.emitMatched {
			p.lsel = append(p.lsel, int32(p.row))
		}
		p.hit = p.nHit // consume the row
		return nil
	}

	if !matched {
		if p.spec.emitProbeUnmatched {
			p.lsel = append(p.lsel, int32(p.row))
			p.rsel = append(p.rsel, kernel.NullIndex)
		}
		p.hit = p.nHit
		return nil
	}
	for p.hit < p.nHit && p.outLen() < p.spec.batchSize {
		p.lsel = append(p.lsel, int32(p.row))
		p.rsel = append(p.rsel, p.t.rows[p.hit])
		p.hit++
	}
	return nil
}

// residualMatch answers "does a partner satisfying the residual exist" for the
// current probe row, consuming its whole match list.
//
// # Why the row is resolved in one call rather than resumed across Next
//
// The rest of the probe resumes mid-row because one probe row can produce
// thousands of OUTPUT rows. A semi join produces at most one, so there is nothing
// to cut: the output is bounded by probe rows, not by candidates. That removes the
// entire class of bug hit/nHit/entered exist to prevent — a verdict cannot be
// half-formed at a batch boundary because it never crosses one.
//
// What is bounded instead is MEMORY: candidates are materialised a batch at a time,
// so a probe row with a million partners costs one batch of pairs, not a million.
// The work in one call is unbounded, which is a latency property rather than a
// correctness one, and the kernels check ctx.
//
// # Both kinds stop at the first satisfying pair, which is not what it looks like
//
// The obvious story is that semi can early-exit and anti cannot, because proving
// that NO partner satisfies the predicate means testing every one. A tooth
// disproved it: one satisfying partner decides both verdicts — the row is kept by
// WhereExists and excluded by WhereNotExists — so the loop breaks on the same
// condition for both, and the emitMatched guard that used to be here bought
// nothing.
//
// The real asymmetry is in the OTHER case, and it is a property of the data rather
// than of the loop: a row with no satisfying partner is only known to have none
// after every candidate has been tested. That is the common case for
// WhereNotExists and the rare one for WhereExists, which is why an anti join
// against a high-fan-out key is the expensive shape.
func (p *joinProbeOp) residualMatch(ctx context.Context) (bool, error) {
	any := false
	for p.hit < p.nHit {
		p.csel = p.csel[:0]
		p.rcand = p.rcand[:0]
		for p.hit < p.nHit && len(p.csel) < p.spec.batchSize {
			p.csel = append(p.csel, int32(p.row))
			p.rcand = append(p.rcand, p.t.rows[p.hit])
			p.hit++
		}
		ok, err := p.residualHolds(ctx)
		if err != nil {
			return false, err
		}
		if ok {
			any = true
			break // One satisfying partner decides both verdicts.
		}
	}
	p.hit = p.nHit
	return any, nil
}

// residualHolds reports whether any pair in the current candidate chunk satisfies
// every conjunct.
//
// The body is filterOp.Apply's, and deliberately so: conjuncts applied in sequence,
// each shrinking the batch, with an early exit at zero rows. Because every pair in
// the chunk belongs to the SAME probe row, "any pair satisfies" is exactly "the
// filtered batch is non-empty" — so no mask survives the call and there is nothing
// to reduce per row.
func (p *joinProbeOp) residualHolds(ctx context.Context) (bool, error) {
	pairs, err := gatherOut(p.pairLayout.Schema, p.pairLayout, p.cur, p.t.build,
		p.csel, p.rcand, false)
	if err != nil {
		return false, err
	}
	for _, e := range p.spec.residual {
		mask, err := evalColumn(ctx, e, pairs)
		if err != nil {
			return false, err
		}
		pairs, err = kernel.FilterBatch(pairs, mask)
		if err != nil {
			return false, err
		}
		if pairs.Rows() == 0 {
			return false, nil
		}
	}
	return pairs.Rows() > 0, nil
}

// residualFold evaluates one chunk holding candidates from MANY probe rows and
// records, per row, whether any of its pairs satisfied every conjunct.
//
// # Why this exists rather than just calling residualMatch per row
//
// Evaluating one probe row at a time is correct, simpler, and was measured **6.8x
// slower** on PDS-H q21: 333 ms became 2,450. The fan-out there is about four rows
// per l_orderkey, so every gather and every expression call was operating on a
// four-row batch — a vectorised engine running at scalar granularity, paying batch
// setup 3.8 million times.
//
// So candidates from as many probe rows as fit go into one pair batch. rowAny is
// indexed by (probe row - base), and csel says which row each pair belongs to.
//
// Conjuncts are ANDed over the WHOLE batch rather than filtered in sequence, which
// is the one place this differs from filterOp: filtering discards the mapping from
// surviving pairs back to probe rows, and that mapping is the entire output.
func (p *joinProbeOp) residualFold(ctx context.Context, base int, rowAny []bool) error {
	pairs, err := gatherOut(p.pairLayout.Schema, p.pairLayout, p.cur, p.t.build,
		p.csel, p.rcand, false)
	if err != nil {
		return err
	}
	n := len(p.csel)

	p.alive = p.alive[:0]
	for range n {
		p.alive = append(p.alive, true)
	}
	for _, e := range p.spec.residual {
		mask, err := evalColumn(ctx, e, pairs)
		if err != nil {
			return err
		}
		bits, valid := mask.Bools(), mask.Validity()
		for i := range n {
			// A null predicate is not a match, which is Filter's rule and SQL's.
			if !valid.Get(i) || !bits.Get(i) {
				p.alive[i] = false
			}
		}
	}
	for i := range n {
		if p.alive[i] {
			rowAny[int(p.csel[i])-base] = true
		}
	}
	return nil
}

// flushStep emits build rows no probe row matched. Right and Full only.
func (p *joinProbeOp) flushStep() (*data.Batch, error) {
	p.reset()
	n := 0
	if p.t.build != nil {
		n = p.t.build.Rows()
	}

	// Walk build rows in INPUT order, not key-id order. Two independent reasons:
	// it gives right-input order, which is the presentable answer; and Go map
	// iteration is randomised, so after a Merge the id numbering of merged-in keys
	// varies run to run — a flush that walked ids would be nondeterministic.
	for p.flushJ < n && p.outLen() < p.spec.batchSize {
		j := p.flushJ
		p.flushJ++
		if id := p.t.rowKey[j]; id != noKey && p.matched[id] {
			continue
		}
		p.lsel = append(p.lsel, kernel.NullIndex)
		p.rsel = append(p.rsel, int32(j))
	}
	if p.flushJ >= n {
		p.phase = p.afterFlush()
		if p.phase == phaseReplay {
			p.releaseTable()
		}
	}
	if p.outLen() == 0 {
		return nil, nil
	}
	return p.emit()
}

// account charges what the probe side holds, as a delta.
//
// Called once at construction and once per probe batch, not per row: seen only
// grows, so a per-batch delta is exact at every batch boundary and costs one map
// length read instead of one ledger update per row.
func (p *joinProbeOp) account() {
	b := int64(cap(p.matched))
	if p.seen != nil {
		b += int64(len(p.seen)) * seenBytesPerKey
	}
	p.mem.RetainBytes(b - p.probeBytes)
	p.probeBytes = b
}

func (p *joinProbeOp) reset() {
	p.lsel = p.lsel[:0]
	p.rsel = p.rsel[:0]
}

func (p *joinProbeOp) outLen() int { return len(p.lsel) }

// emit materialises the pending selections.
func (p *joinProbeOp) emit() (*data.Batch, error) {
	if p.spec.maskOnly {
		if len(p.lsel) == 0 {
			return nil, nil
		}
		// The zero-copy case is worth keeping: a semi join that selects every row it
		// scanned hands the probe batch straight through, which is what the filter
		// path used to do for free.
		if p.cur != nil && len(p.lsel) == p.cur.Rows() {
			return p.cur, nil
		}
		return takeBatch(p.schema, p.cur, p.lsel)
	}

	n := len(p.lsel)
	if n == 0 {
		return nil, nil
	}
	src := p.cur
	if src == nil {
		src = p.leftPad
	}
	return gatherOut(p.schema, p.layout, src, p.t.build,
		p.lsel, p.rsel, p.coalesceFromRight())
}

// gatherOut materialises one output batch from a pair of selection vectors.
//
// Extracted from emit so the null bucket's streamer can reuse it. A spilled
// null-keyed build row is emitted with every left column null — which is exactly
// what emit already does during the Right/Full flush, and duplicating that column
// walk is how the layout's "single authority" property would be lost.
func gatherOut(schema *dtype.Schema, layout *plan.JoinLayout, left, right *data.Batch,
	lsel, rsel []int32, coalesceRight bool,
) (*data.Batch, error) {

	n := len(lsel)
	cols := make([]*data.Column, schema.Len())
	for i, jc := range layout.Columns {
		var (
			c   *data.Column
			err error
		)
		switch {
		case jc.Side == plan.FromRight:
			c, err = kernel.Take(right.Column(jc.Index), rsel)
		case jc.CoalesceWith >= 0 && coalesceRight:
			// A merged key on a Right join takes the RIGHT side's value: the left is
			// null on an unmatched right row, and on a matched row the two are equal.
			c, err = kernel.Take(right.Column(jc.CoalesceWith), rsel)
		default:
			c, err = kernel.Take(left.Column(jc.Index), lsel)
		}
		if err != nil {
			return nil, err
		}
		// A COALESCED key column takes the promoted type, so the source column may
		// be narrower than the output: joining Int32 to Int64 gives an Int64 key.
		// The keys are already cast for hashing, but that cast produced a temporary;
		// this is the output column, gathered from the original.
		//
		// A cast rather than WithDType, deliberately. WithDType would relabel the
		// bytes and hand NewBatch something that passes its check while holding
		// Int32 values in an Int64 column.
		if f := schema.Field(i); c.DType() != f.Type {
			if c, err = kernel.Cast(c.Name(), f.Type, false, c); err != nil {
				return nil, err
			}
		}
		// Rename is where Suffix is honoured, for free: the plan already computed
		// the output name.
		cols[i] = c.Rename(schema.Field(i).Name)
	}
	if len(cols) == 0 {
		return data.NewBatchRows(schema, nil, n), nil
	}
	return data.NewBatch(schema, cols)
}

func (p *joinProbeOp) coalesceFromRight() bool { return p.spec.kind == plan.JoinRight }

// --- helpers ----------------------------------------------------------------------

// emptyOperator yields no rows. It is what Finish probes against.
type emptyOperator struct{ schema *dtype.Schema }

func (e emptyOperator) Schema() *dtype.Schema                     { return e.schema }
func (e emptyOperator) Next(context.Context) (*data.Batch, error) { return nil, io.EOF }
func (e emptyOperator) Close() error                              { return nil }

// emptyBatch builds a zero-row batch with REAL payload buffers.
//
// Not emptyColumns: that uses data.NewNull, which carries no payload, and this
// batch is gathered from on every unmatched row.
func emptyBatch(s *dtype.Schema) (*data.Batch, error) {
	cols := make([]*data.Column, s.Len())
	for i, f := range s.All() {
		c, err := kernel.NullColumn(f.Name, f.Type, 0)
		if err != nil {
			return nil, err
		}
		cols[i] = c
	}
	return data.NewBatch(s, cols)
}

// evalKeys evaluates the key expressions and casts each to its comparison type.
//
// The cast is not optional. Parquet-style Int32 and Int64 keys encode to 4 and 8
// bytes with different sign biasing, so an uncast pair NEVER matches and the join
// silently returns zero rows — the worst kind of wrong answer. The plan layer has
// already proven a common type exists; this applies it.
func evalKeys(ctx context.Context, keys []expr.Node, in *data.Batch, types []dtype.DataType) ([]*data.Column, error) {
	out := make([]*data.Column, len(keys))
	for i, k := range keys {
		c, err := evalColumn(ctx, k, in)
		if err != nil {
			return nil, err
		}
		if i < len(types) && c.DType() != types[i] {
			if c, err = kernel.Cast(c.Name(), types[i], false, c); err != nil {
				return nil, err
			}
		}
		out[i] = c
	}
	return out, nil
}

// keyValidity reports, per row, whether EVERY key column is valid.
//
// SQL's rule is that a key containing ANY null matches nothing — not "all null".
// bitmap.AndMulti is exactly that fold, and it is O(n/64) through the Words
// iterator, so honouring NullsEqual costs one pass and one branch per row rather
// than a second encoder.
func keyValidity(cols []*data.Column) bitmap.View {
	vs := make([]bitmap.View, len(cols))
	for i, c := range cols {
		vs[i] = c.Validity()
	}
	return bitmap.AndMulti(vs...)
}

func duplicateKeyErr(v plan.JoinValidation, side string, s *dtype.Schema, keys []expr.Node, row int) error {
	e := uerr.New(uerr.KindValue, "join",
		"validate=%s requires unique keys on the %s side, but a key is duplicated",
		v, side)
	if row >= 0 {
		e.Hint("first duplicate at %s row %d", side, row)
	}
	if len(keys) > 0 {
		e.Hint("key: %s", expr.StringAll(keys))
	}
	e.Hint("each duplicate multiplies every matching row on the other side")
	e.Hint("de-duplicate with .Unique(...), or pass JoinValidate(ursus.ValidateManyToMany)")
	return e
}

// --- planning -----------------------------------------------------------------------

// planJoin lowers a plan.Join.
//
// The RIGHT side always builds. Not cost-based: nothing in the plan layer carries
// a cardinality estimate — plan.Source exposes Name, Describe, Schema and Caps and
// nothing about size — so a heuristic here would be a fabricated number. The seam
// is named: a future Source.Rows() plus a swap decision here, and nothing below
// this function changes.
func planJoin(ctx context.Context, j *plan.Join, opts Options) (Operator, error) {
	left, err := planPipelined(ctx, j.Left, opts)
	if err != nil {
		return nil, err
	}
	right, err := planPipelined(ctx, j.Right, opts)
	if err != nil {
		return nil, err
	}

	layout, err := j.Layout()
	if err != nil {
		return nil, err
	}
	ls, err := j.Left.Schema()
	if err != nil {
		return nil, err
	}
	rs, err := j.Right.Schema()
	if err != nil {
		return nil, err
	}

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultOptions().BatchSize
	}

	// The residual's namespace, computed once here rather than per probe row. Nil
	// when there is no residual, which is every join but a WhereExists.
	var pairLayout *plan.JoinLayout
	if j.HasResidual() {
		if pairLayout, err = j.PairLayout(); err != nil {
			return nil, err
		}
	}

	sink := &joinBuildSink{
		out:        layout.Schema,
		layout:     layout,
		left:       ls,
		right:      rs,
		keys:       j.RightOn,
		leftKeys:   j.LeftOn,
		spec:       newJoinSpec(j, batchSize),
		threads:    opts.Threads,
		pairLayout: pairLayout,
		ids:        kernel.NewKeyTable(),
		mem:        opts.Budget.Account("join"),

		budget: opts.Budget,
		bparts: make([]*partWriter, nBuckets),
		bfiles: make([]string, nBuckets),
		pend:   make([][]int32, nBuckets),
	}
	return &joinBreaker{build: right, probe: left, builder: sink}, nil
}
