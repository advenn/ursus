package physical

import (
	"context"
	"errors"
	"io"
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

// Spilling hash join: partition the build keys that will not fit, route the probe
// rows that cannot match anything resident, and replay the pairs.
//
// # Why this is NOT step 12's residency freeze
//
// The aggregation freezes its resident KEY SET: a key already in the table stays, a
// new key is routed. That bounds it because a group's accumulator state is O(1) —
// extagg.go's overBudget spells it out: "Reserve(nGroups) is a no-op once nGroups is
// fixed, so every O(groups) accumulator's NBytes goes flat at the freeze."
//
// A join retains build ROWS, and a resident key keeps collecting them for as long as
// the build side streams. So the same freeze bounds nothing: the keys admitted before
// it go on growing after it. Measured, on data with NO SKEW — 500 keys, 60,000 build
// rows, a 256 KiB limit — step 12's rule dropped in here peaks at 1,276,628 bytes and
// spills ZERO times, because the first batch admits every key and every later row
// belongs to one of them. TestSpillingJoinIsBounded is that measurement; its fixture
// is high-fan-out for exactly this reason, and with one row per key it would not
// distinguish the two rules at all.
//
// Two changes fix it, and it is worth being precise about which does what.
//
// RESIDENCY BY HASH BUCKET is what makes the hybrid stage useful. The resident set is
// then a fixed FRACTION of the key space rather than "whatever arrived first", so it
// is bounded by construction and the sink can STAY there — bucket 0 is never written,
// never read back, and its probe rows come out in probe order. That is a hybrid hash
// join, and it is the textbook answer.
//
// splitAll is what makes it BOUNDED. Giving up residency entirely is the escape hatch
// for a bucket 0 that is itself too large, and it is the state the guarantee actually
// rests on — measured, the bucket rule without it is bounded on the fixtures here,
// but nothing makes it bounded in general, because one bucket can hold one key.
//
// The two together give: resident work stays in memory and in order while it fits,
// and the floor is one batch plus the write buffers when it does not.
//
// # Except for Semi and Anti, where step 12's rule is exactly right
//
// Those two never emit a right column, so they retain no rows at all: their state is
// the ids map, O(distinct keys), and it goes FLAT at the freeze for precisely the
// reason an accumulator's does. They also cannot use the bucket rule, because
// repartitioning an existing key set needs the rows it came from and they do not
// have them.
//
// Both rules satisfy the SAME invariant, which is what lets everything downstream —
// the probe, the replay, the recursion — be one code path:
//
//	A key is either resident for its whole life or routed for its whole life.
//
// residentKey is where the two differ, and it is four lines.
const (
	// nullBucket holds build rows whose key contained a null while NullsEqual is
	// off. They have NO HASH — Consume skips the encoder for them entirely — so they
	// cannot be routed with the others, and Right and Full must still emit them
	// unmatched. Without a bucket of their own they are silently dropped.
	nullBucket = nParts
	nBuckets   = nParts + 1
)

// splitState is how much of the build side has been pushed to disk.
type splitState int8

const (
	// splitNone: nothing overflowed. Behaviour is byte-identical to a build with no
	// memory limit at all, which is what keeps every existing join test meaningful.
	splitNone splitState = iota
	// splitHybrid: bucket 0 is resident (or, for Semi/Anti, the keys seen so far);
	// everything else is routed.
	splitHybrid
	// splitAll: nothing is resident. The escape hatch for a bucket 0 that is itself
	// too large — one more state rather than a second mechanism.
	splitAll
)

// residentKey reports whether a key stays in memory. See the file header.
func (s *joinBuildSink) residentKey(k []byte, seen bool) bool {
	switch s.split {
	case splitNone:
		return true
	case splitAll:
		return false
	default:
		if !s.spec.needBuildRows {
			return seen // Semi/Anti: O(keys), so arrival order bounds it
		}
		return partitionOf(k, s.level) == 0
	}
}

// --- writers ----------------------------------------------------------------------

// spillPath makes this sink's directory on first use and returns a file name.
//
// Level 0 owns the directory and removes it; a sub-join is handed the same one and
// prefixes its files with its parent's, so the recursion is visible in the name.
func (s *joinBuildSink) spillPath(tag string, p int) (string, error) {
	if s.dir == "" {
		dir, err := os.MkdirTemp(s.budget.SpillDir(), "ursus-join-")
		if err != nil {
			return "", uerr.Wrap(err, uerr.KindResource, "join",
				"creating a spill directory").
				Hint("choose a writable location with WithSpillDir")
		}
		s.dir, s.dirOwner = dir, true
		s.prefix = filepath.Join(dir, "j")
	}
	return s.prefix + tag + itoa(p) + ".ursspill", nil
}

// buildWriter opens bucket p's build file on first use.
func (s *joinBuildSink) buildWriter(p int) (*partWriter, error) {
	if w := s.bparts[p]; w != nil {
		return w, nil
	}
	path, err := s.spillPath("b", p)
	if err != nil {
		return nil, err
	}
	w, err := spill.CreateSized(path, s.right, partBufBytes)
	if err != nil {
		return nil, err
	}
	s.bparts[p] = &partWriter{w: w, path: path}
	s.mem.RetainBytes(partBufBytes)
	s.budget.NoteSpill()
	return s.bparts[p], nil
}

// routeBuild writes this batch's non-resident rows out, one slice per bucket.
//
// Rows go out in ascending index order and batches arrive in input order, so each
// file is in INPUT ORDER — which is what makes the Right/Full flush of a replayed
// partition come out in right-input order, exactly as flushStep's doc requires.
func (s *joinBuildSink) routeBuild(in *data.Batch) error {
	for p, rows := range s.pend {
		if len(rows) == 0 {
			continue
		}
		w, err := s.buildWriter(p)
		if err != nil {
			return err
		}
		b, err := takeBatch(s.right, in, rows)
		if err != nil {
			return err
		}
		if err := w.w.Write(b); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "join",
				"writing spill partition %s", w.path)
		}
		s.pend[p] = rows[:0]
	}
	return nil
}

// closeBuildParts finishes every open build writer and records its path by bucket.
func (s *joinBuildSink) closeBuildParts() error {
	var errs []error
	for p, w := range s.bparts {
		if w == nil {
			continue
		}
		if err := w.w.Close(); err != nil {
			errs = append(errs, err)
		} else {
			s.bfiles[p] = w.path
		}
		s.bparts[p] = nil
		s.mem.RetainBytes(-partBufBytes)
	}
	return errors.Join(errs...)
}

// --- the split --------------------------------------------------------------------

// escalate moves one step further out of memory.
//
// splitNone -> splitHybrid keeps bucket 0 and repartitions everything retained so
// far. splitHybrid -> splitAll gives up bucket 0 too, which is what stops a single
// oversized bucket from being unbounded. Semi and Anti never reach splitAll: their
// resident state is flat after the freeze, so a second overflow is somebody else's.
func (s *joinBuildSink) escalate(ctx context.Context) error {
	switch {
	case s.split == splitNone:
		s.split = splitHybrid
		if !s.spec.needBuildRows {
			return nil // nothing retained to repartition; the freeze is the whole move
		}
		return s.repartition(ctx)
	case s.split == splitHybrid && s.spec.needBuildRows:
		s.split = splitAll
		return s.repartition(ctx)
	}
	return nil
}

// repartition pushes every retained build row that is no longer resident to disk and
// rebuilds the table over what is left.
//
// It runs at most twice per sink and is bounded by construction: the rows it walks
// are exactly the ones that fit in the budget, which is what triggered it.
//
// The table is rebuilt from scratch rather than patched. Ids are dense and assigned
// in first-appearance order, so removing keys from the middle would renumber
// everything anyway — and nothing has been emitted yet, so the numbering is free to
// change.
func (s *joinBuildSink) repartition(ctx context.Context) error {
	old := s.parts
	s.parts, s.ids = nil, make(map[string]int32)
	s.counts, s.rowKey = nil, nil
	s.keyBytes, s.nBuild = 0, 0
	s.mem.Release()
	s.stateBytes = 0

	for _, b := range old {
		if err := s.classify(ctx, b); err != nil {
			return err
		}
	}
	s.reaccount()
	return nil
}

// classify routes one batch's non-resident rows and admits the rest.
//
// It reads residentKey, which escalate has already updated — so the pass that
// repartitions what is retained and the pass that handles every later batch are the
// SAME code with the same predicate, rather than two that have to agree.
func (s *joinBuildSink) classify(ctx context.Context, in *data.Batch) error {
	n := in.Rows()
	keyCols, err := evalKeys(ctx, s.keys, in, s.layout.KeyTypes)
	if err != nil {
		return err
	}
	ok := keyValidity(keyCols)
	enc, err := kernel.NewGroupKeyEncoder("join", keyCols)
	if err != nil {
		return err
	}

	sel := s.keep[:0]
	routed := 0
	for i := range n {
		if !s.spec.nullsEqual && !ok.Get(i) {
			// No hash, so no bucket. Right and Full must still emit it; for every
			// other kind it can never reach the output, so nobody pays to write it.
			if s.spec.emitBuildUnmatched {
				s.pend[nullBucket] = append(s.pend[nullBucket], int32(i))
				routed++
			}
			continue
		}
		k := enc.Encode(i)
		_, seen := s.ids[string(k)]
		if !s.residentKey(k, seen) {
			p := partitionOf(k, s.level)
			s.pend[p] = append(s.pend[p], int32(i))
			routed++
			continue
		}
		sel = append(sel, int32(i))
	}
	s.keep = sel

	if routed > 0 {
		if err := s.routeBuild(in); err != nil {
			return err
		}
	}
	if len(sel) == 0 {
		return nil
	}
	src := in
	if len(sel) != n {
		if src, err = takeBatch(s.right, in, sel); err != nil {
			return err
		}
	}
	return s.admit(ctx, src, -1)
}

// admit adds an already-classified, all-resident batch to the table.
//
// rowBase is the input row number of in[0], or -1 when there is not one. It exists
// only for the RequiresRightUnique message: after a split, admit is fed a compacted
// batch or one replayed from a spill file, and neither position names a row of the
// user's input. The check still fires — see the Validate proof below — it just
// declines to guess where.
//
// The check is INSIDE this loop rather than a pass before it, because a duplicate
// can be two rows of the same batch and neither is in ids until the loop puts it
// there. Splitting the two passes silently stopped catching intra-batch duplicates.
//
// # Validate survives partitioning, and the proof is two lines
//
// A key is wholly resident or wholly routed, and equal keys hash to the same bucket.
// So a duplicate right key is caught here if the key is resident, and by the
// sub-sink's own admit if it is not; a duplicate spanning two buckets cannot exist,
// and one spanning the resident set and a bucket cannot either.
func (s *joinBuildSink) admit(ctx context.Context, in *data.Batch, rowBase int) error {
	keyCols, err := evalKeys(ctx, s.keys, in, s.layout.KeyTypes)
	if err != nil {
		return err
	}
	ok := keyValidity(keyCols)
	enc, err := kernel.NewGroupKeyEncoder("join", keyCols)
	if err != nil {
		return err
	}
	track := s.spec.tracksRows()
	for i := range in.Rows() {
		if !s.spec.nullsEqual && !ok.Get(i) {
			if track {
				s.rowKey = append(s.rowKey, noKey)
			}
			continue
		}
		k := enc.Encode(i)
		id, seen := s.ids[string(k)]
		if !seen {
			id = int32(len(s.ids))
			s.ids[string(k)] = id
			s.counts = append(s.counts, 0)
			s.keyBytes += int64(len(k)) + idsBytesPerKey
		} else if s.spec.validate.RequiresRightUnique() {
			row := -1
			if rowBase >= 0 {
				row = rowBase + i
			}
			return duplicateKeyErr(s.spec.validate, "right", s.right, s.keys, row)
		}
		if track {
			s.counts[id]++
			s.rowKey = append(s.rowKey, id)
		}
	}
	if s.spec.needBuildRows {
		s.parts = append(s.parts, in)
		s.mem.Retain(in)
	}
	s.nBuild += in.Rows()
	return nil
}

// reaccount re-charges the sink's plain-Go state after it has been rebuilt.
func (s *joinBuildSink) reaccount() {
	st := int64(cap(s.rowKey))*4 + int64(cap(s.counts))*4 + s.keyBytes
	s.mem.RetainBytes(st - s.stateBytes)
	s.stateBytes = st
}

// spilled reports whether anything actually reached disk.
//
// The test is "were files written", NOT "did we split". A sink can split and route
// zero rows — every key happened to land in bucket 0 — and then the table is
// complete and the probe must behave exactly as it does with no limit at all.
func (s *joinBuildSink) spilled() bool {
	for _, f := range s.bfiles {
		if f != "" {
			return true
		}
	}
	return false
}

// --- the refusals -----------------------------------------------------------------

// overBudget is Check, minus the cases splitting has already answered.
//
// Before the first split this is Account.Check verbatim. Afterwards the query is
// over budget BY CONSTRUCTION — that is what caused the split — so a bare Check
// would fail every spilling join, which is step 12's overBudget lesson in a second
// place.
//
// What is left is the shape partitioning cannot help. Radix partitioning divides the
// KEY SPACE, so it cannot divide a cross join, which has exactly one key by
// construction; and it cannot divide one key's build rows, which is caught by
// maxSpillDepth in newSub rather than here, because only the recursion can tell
// "this bucket did not narrow" from "this bucket is merely large".
func (s *joinBuildSink) overBudget() error {
	if s.split != splitNone {
		return nil
	}
	if s.budget.Limit() > 0 && len(s.keys) == 0 && s.mem.Over() {
		return uerr.New(uerr.KindResource, "join",
			"memory limit of %s exceeded: a cross join buffers its whole build side "+
				"and cannot spill", execopt.Bytes(s.budget.Limit())).
			Hint("a cross join has NO join key, and partitioning divides work across " +
				"keys — so there is nothing to divide").
			Hint("raise the limit with WithMemoryLimit, or add a join key")
	}
	return s.mem.Check()
}

// --- replay -----------------------------------------------------------------------

// newSub builds the sink that re-joins one spilled bucket.
//
// It shares everything static — schemas, key expressions, spec, spill directory,
// budget — and gets fresh state at the next radix depth. The recursion is not extra
// machinery: this returns the same type, so a sub-join splits, routes and recurses
// by the same code.
func (s *joinBuildSink) newSub() (*joinBuildSink, error) {
	if s.level+1 > maxSpillDepth {
		return nil, uerr.New(uerr.KindResource, "join",
			"memory limit exceeded: a single join key has more build rows than the limit").
			Hint("partitioning divides work across KEYS, so %d levels of it have not "+
				"split this one — one key's rows do not fit", maxSpillDepth).
			Hint("raise the limit with WithMemoryLimit")
	}
	return &joinBuildSink{
		out: s.out, layout: s.layout, left: s.left, right: s.right,
		keys: s.keys, leftKeys: s.leftKeys, spec: s.spec,
		ids: make(map[string]int32),
		mem: s.budget.Account("join"),

		budget: s.budget, level: s.level + 1,
		dir: s.dir, dirOwner: false, prefix: s.prefix + "s" + itoa(s.level+1),
		bparts: make([]*partWriter, nBuckets),
		bfiles: make([]string, nBuckets),
		pend:   make([][]int32, nBuckets),
	}, nil
}

// nullPadOp streams the null bucket and emits every row unmatched.
//
// The null bucket is NOT replayed through a sub-join, and that is not an
// optimisation. Its rows have no key, so a sub-sink would classify them noKey again,
// route them to ITS null bucket, and repeat until maxSpillDepth — reporting "a
// single join key has more build rows than the limit" about data that has no key at
// all. Step 12 shipped that exact class of confident-and-wrong diagnosis once.
//
// Streaming is also all these rows need: they match nothing by definition, so the
// answer is one null-padded output row each, in file order, in O(1) memory.
type nullPadOp struct {
	schema  *dtype.Schema
	layout  *plan.JoinLayout
	leftPad *data.Batch
	run     *fileRun
	n       int
}

func (o *nullPadOp) Schema() *dtype.Schema { return o.schema }

func (o *nullPadOp) Close() error {
	if o.run == nil {
		return nil
	}
	r := o.run
	o.run = nil
	return r.Close()
}

func (o *nullPadOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if o.run == nil {
			return nil, io.EOF
		}
		b, err := o.run.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.Join(o.Close(), io.EOF)
		}
		if err != nil {
			return nil, err
		}
		if b.Rows() == 0 {
			continue // an empty batch is not end-of-stream
		}
		lsel := make([]int32, b.Rows())
		rsel := make([]int32, b.Rows())
		for i := range b.Rows() {
			lsel[i] = kernel.NullIndex
			rsel[i] = int32(i)
		}
		// coalesceRight is false: a null-keyed build row has a NULL key, so taking
		// the merged column from the right would write null either way, and Full —
		// the only kind that could ask — refuses Coalesce at plan time.
		return gatherOut(o.schema, o.layout, o.leftPad, b, lsel, rsel, false)
	}
}

// --- the probe side ---------------------------------------------------------------

// buildFile is bucket b's build file, or "" if no build row landed there.
func (p *joinProbeOp) buildFile(b int) string {
	if p.sink == nil || b >= len(p.sink.bfiles) {
		return ""
	}
	return p.sink.bfiles[b]
}

// probeWriter opens bucket b's probe file on first use.
//
// Only ever reached for a bucket that HAS a build file, so an empty probe partition
// beside an absent build one cannot exist — see enter.
func (p *joinProbeOp) probeWriter(b int) (*partWriter, error) {
	if w := p.pparts[b]; w != nil {
		return w, nil
	}
	path, err := p.sink.spillPath("p", b)
	if err != nil {
		return nil, err
	}
	w, err := spill.CreateSized(path, p.sink.left, partBufBytes)
	if err != nil {
		return nil, err
	}
	p.pparts[b] = &partWriter{w: w, path: path}
	p.mem.RetainBytes(partBufBytes)
	p.sink.budget.NoteSpill()
	return p.pparts[b], nil
}

// routeProbe writes this batch's routed rows out, one slice per bucket.
//
// It MUST run while p.cur is still live. hashAggSink.route is called from inside
// Consume with the batch in scope; the probe loop spans many Next calls per batch,
// so the deadline here is the moment probeStep clears p.cur — and routing lazily
// instead would drop the last partial batch's rows with no error anywhere.
func (p *joinProbeOp) routeProbe() error {
	for b, rows := range p.ppend {
		if len(rows) == 0 {
			continue
		}
		w, err := p.probeWriter(b)
		if err != nil {
			return err
		}
		out, err := takeBatch(p.sink.left, p.cur, rows)
		if err != nil {
			return err
		}
		if err := w.w.Write(out); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "join",
				"writing spill partition %s", w.path)
		}
		p.ppend[b] = rows[:0]
	}
	return nil
}

// closeProbeParts finishes every open probe writer and records its path by bucket.
func (p *joinProbeOp) closeProbeParts() error {
	var errs []error
	for b, w := range p.pparts {
		if w == nil {
			continue
		}
		if err := w.w.Close(); err != nil {
			errs = append(errs, err)
		} else {
			p.pfiles[b] = w.path
		}
		p.pparts[b] = nil
		p.mem.RetainBytes(-partBufBytes)
	}
	return errors.Join(errs...)
}

// afterProbe picks the phase that follows the probe stream.
func (p *joinProbeOp) afterProbe() int8 {
	if p.spec.emitBuildUnmatched {
		return phaseFlushing
	}
	return p.afterFlush()
}

// afterFlush picks the phase that follows the resident build-unmatched flush.
func (p *joinProbeOp) afterFlush() int8 {
	if p.sink != nil && p.sink.spilled() {
		return phaseReplay
	}
	return phaseDone
}

// releaseTable drops the resident join table before the first partition is replayed.
//
// This is step 12's releaseState, in the place a join puts it. t.build is a whole
// concatenated copy of the resident build side and t.ids is one entry per resident
// key; both are dead the moment the resident flush ends, and holding them while
// sub-joins run would leave every one of them a fraction of the budget. Step 12
// shipped exactly that bug and its symptom was an error blaming skew on uniform data.
//
// The account belongs to the SINK and the release point is in the probe operator,
// which is why the two share one account rather than having one each.
func (p *joinProbeOp) releaseTable() {
	p.t = &joinTable{}
	p.matched, p.seen = nil, nil
	p.probeBytes = 0
	p.sink.mem.Release()
	p.sink.stateBytes, p.sink.tableBytes, p.sink.keyBytes = 0, 0, 0
}

// replayStep re-joins one spilled bucket.
//
// It holds ONE sub-join at a time, which is what keeps the replay bounded: the
// sub-join gets the whole budget, splits on the same rule, and spills its own
// sub-buckets if it has to. The recursion is not extra machinery — newSub returns
// the same type.
//
// Buckets are walked in bucket order, and the probe side of each is whatever was
// routed there, or NOTHING. An absent probe file is not a special case: it is
// Probe(ctx, an operator that yields no rows), which is Finish, which is the
// equivalence ProbeBuilder has always documented.
func (p *joinProbeOp) replayStep(ctx context.Context) (*data.Batch, error) {
	if p.rep == nil {
		for p.next < nBuckets && p.buildFile(p.next) == "" {
			p.next++
		}
		if p.next >= nBuckets {
			p.phase = phaseDone
			return nil, nil
		}
		b := p.next
		p.next++
		if err := p.startBucket(ctx, b); err != nil {
			return nil, err
		}
	}
	out, err := p.rep.Next(ctx)
	if errors.Is(err, io.EOF) {
		// Drop the whole sub-join before opening the next bucket: that is what keeps
		// this to one live sub-join rather than one per bucket.
		err = errors.Join(p.rep.Close(), closeSink(p.sub))
		p.rep, p.sub = nil, nil
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if out.Rows() == 0 {
		return nil, nil // an empty batch is not end-of-stream
	}
	return out, nil
}

func closeSink(s *joinBuildSink) error {
	if s == nil {
		return nil
	}
	return s.Close()
}

// startBucket installs the operator for one spilled bucket.
func (p *joinProbeOp) startBucket(ctx context.Context, b int) error {
	buildPath := p.buildFile(b)

	if b == nullBucket {
		// Streamed and null-padded, never re-joined. See nullPadOp.
		r, err := spill.Open(buildPath)
		if err != nil {
			return err
		}
		p.rep = &nullPadOp{schema: p.schema, layout: p.layout,
			leftPad: p.leftPad, run: &fileRun{r: r}}
		return nil
	}

	sub, err := p.sink.newSub()
	if err != nil {
		return err
	}
	if err := drainJoin(ctx, sub, buildPath); err != nil {
		sub.Close()
		return err
	}
	var probe Operator = emptyOperator{schema: p.sink.left}
	if path := p.pfiles[b]; path != "" {
		r, err := spill.Open(path)
		if err != nil {
			sub.Close()
			return err
		}
		probe = &runOperator{schema: p.sink.left, src: &fileRun{r: r}}
	}
	op, err := sub.Probe(ctx, probe)
	if err != nil {
		probe.Close()
		sub.Close()
		return err
	}
	p.rep, p.sub = op, sub
	return nil
}

// drainJoin feeds every batch of a spilled bucket to a sub-join's build side.
func drainJoin(ctx context.Context, sink *joinBuildSink, path string) error {
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
