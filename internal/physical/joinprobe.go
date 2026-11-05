package physical

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
)

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

	cur   *data.Batch
	curOK bitmap.View
	enc   *kernel.GroupKeyEncoder
	row   int
	// found is each row's id in the table, looked up for the whole batch in
	// startBatch a chunk at a time, or -1 when the key is not resident, or noLookup
	// for a null key that matches nothing (step 126).
	found   []int32
	chunk   keyChunk
	intKeys []int64 // a narrower integer key, widened (kernel.IntKeys)
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

	level int // radix depth; must match the sink's, or the two route apart
	next  int // the next build bucket to replay
	// routes is whether any bucket has a build file, so a key the table misses could
	// belong to one. Without it, a miss is only a miss (enter).
	routes bool
	rep    Operator // the live sub-join's output, or the null bucket's streamer
	sub    *joinBuildSink

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
	p.room(min(p.cur.Rows(), p.spec.batchSize))
	if p.plain() {
		p.stepPlain()
	}
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

// plain reports whether a probe row's output is its match list alone: a join of keys
// that emits pairs, nothing spilled to route a row to and no Validate to police. Then
// stepPlain does enter's and appendMatches' work in one loop.
func (p *joinProbeOp) plain() bool {
	return !p.spec.maskOnly && len(p.keys) > 0 && !p.routes && p.seen == nil && p.t.off != nil
}

// stepPlain is stepCurrent's loop for a plain probe (step 398, B4 of v0.8-scope.md):
// the same pairs, in the same order, cut at the same batch size, without a call a row.
// A 10,000-row join to a 100-row dimension spent a third of its own time in enter and
// appendMatches.
func (p *joinProbeOp) stepPlain() {
	n, limit := p.cur.Rows(), p.spec.batchSize
	off, rows, found := p.t.off, p.t.rows, p.found
	unmatched := p.spec.emitProbeUnmatched
	for p.row < n && len(p.lsel) < limit {
		if !p.entered {
			p.entered, p.routed = true, false
			p.hit, p.nHit = 0, 0
			if id := found[p.row]; id >= 0 {
				p.hit, p.nHit = off[id], off[id+1]
				if p.matched != nil && p.nHit > p.hit {
					p.matched[id] = true
				}
			}
			if p.hit >= p.nHit {
				if unmatched {
					p.lsel = append(p.lsel, int32(p.row))
					p.rsel = append(p.rsel, kernel.NullIndex)
				}
				p.row++
				p.entered = false
				continue
			}
		}
		r := int32(p.row)
		for p.hit < p.nHit && len(p.lsel) < limit {
			p.lsel = append(p.lsel, r)
			p.rsel = append(p.rsel, rows[p.hit])
			p.hit++
		}
		if p.hit >= p.nHit {
			p.row++
			p.entered = false
		}
	}
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
	keyCols, err := evalKeys(ctx, "join", p.keys, in, p.layout.KeyTypes)
	if err != nil {
		return err
	}
	p.curOK = keyValidity(keyCols)
	if p.enc, err = kernel.NewGroupKeyEncoder("join", keyCols); err != nil {
		return err
	}
	// Every row's key looked up now, a chunk at a time, so the table's slots are
	// read many at once rather than one per row as enter reaches it. Get, not
	// GetOrInsert: the probe must never add a key, and a frozen table read this way
	// is still safe to share between probe workers.
	n := in.Rows()
	p.found = slices.Grow(p.found[:0], n)[:n]
	if p.t.ints != nil {
		// An integer key is its own value, read from the column: nothing to encode.
		// A null row is looked up like the rest, for whatever its slot holds, and
		// then marked; a chunk without a branch per row is the cheaper of the two.
		keys := kernel.IntKeys(keyCols[0], &p.intKeys)
		for i := 0; i < n; i += kernel.ManyChunk {
			j := min(i+kernel.ManyChunk, n)
			p.t.ints.GetMany(keys[i:j], p.found[i:j])
		}
		if !p.curOK.IsAllSet() {
			for i := range n {
				if !p.curOK.Get(i) {
					p.found[i] = noLookup
				}
			}
		}
		return nil
	}
	if p.t.words != nil {
		// Several integer keys, packed as the build packed them (step 391), a chunk at
		// a time. A null row is packed and looked up like the rest, and then marked
		// where nulls match nothing; where they match, its null bits are its key.
		var a, b, c [kernel.ManyChunk]uint64
		var cs []uint64
		for i := 0; i < n; i += kernel.ManyChunk {
			m := min(kernel.ManyChunk, n-i)
			if p.t.pack.Words() == 3 {
				cs = c[:m]
			}
			p.t.pack.Pack(keyCols, i, a[:m], b[:m], cs)
			p.t.words.GetMany(a[:m], b[:m], cs, p.found[i:i+m])
		}
		if !p.spec.nullsEqual && !p.curOK.IsAllSet() {
			for i := range n {
				if !p.curOK.Get(i) {
					p.found[i] = noLookup
				}
			}
		}
		return nil
	}
	ch := &p.chunk
	ch.reset()
	flush := func() {
		ids, ok := ch.lookup(p.t.ids)
		for j, id := range ids {
			if !ok[j] {
				id = -1
			}
			p.found[ch.rows[j]] = id
		}
		ch.reset()
	}
	for i := range n {
		if !p.spec.nullsEqual && !p.curOK.Get(i) {
			p.found[i] = noLookup
			continue
		}
		ch.add(p.enc, i)
		if ch.full() {
			flush()
		}
	}
	flush()
	return nil
}
