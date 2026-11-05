package physical

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
)

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
		p.csel, p.rcand, keyFromLeft)
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
		p.csel, p.rcand, keyFromLeft)
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
	// The table is released for the replay only after the last rows are emitted from
	// it. Released first, as it was from the first commit, the last flush read a nil
	// build side whenever a level both kept rows resident and spilled others (step
	// 133).
	release := false
	if p.flushJ >= n {
		p.phase = p.afterFlush()
		release = p.phase == phaseReplay
	}
	var out *data.Batch
	if p.outLen() > 0 {
		var err error
		if out, err = p.emit(); err != nil {
			return nil, err
		}
	}
	if release {
		p.releaseTable()
	}
	return out, nil
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

// room gives lsel, and rsel where pairs are emitted, room for n rows, a probe
// batch's up to the batch size: they grew from nothing by doubling, in every query,
// where a join of keys emits about a row for each it probes (step 400, B3 of
// v0.8-scope.md).
func (p *joinProbeOp) room(n int) {
	if cap(p.lsel) < n {
		p.lsel = make([]int32, 0, n)
	}
	if !p.spec.maskOnly && cap(p.rsel) < n {
		p.rsel = make([]int32, 0, n)
	}
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
		// path used to do for free; and one that selects a run of them shares it
		// (step 170).
		if p.cur != nil && len(p.lsel) == p.cur.Rows() {
			return p.cur, nil
		}
		if start, ok := runOf(p.lsel, p.cur); ok {
			return p.cur.Slice(start, len(p.lsel)), nil
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
		p.lsel, p.rsel, p.keyFrom())
}

// gatherOut materialises one output batch from a pair of selection vectors.
//
// Extracted from emit so the null bucket's streamer can reuse it. A spilled
// null-keyed build row is emitted with every left column null — which is exactly
// what emit already does during the Right/Full flush, and duplicating that column
// walk is how the layout's "single authority" property would be lost.
func gatherOut(schema *dtype.Schema, layout *plan.JoinLayout, left, right *data.Batch,
	lsel, rsel []int32, from keyFrom,
) (*data.Batch, error) {

	n := len(lsel)
	// When every probe row of a run matched once, in order, as a fact table probing
	// a dimension on its key does, the left side's rows are a run of its batch, and
	// its columns are shared rather than gathered (step 170).
	start, run := runOf(lsel, left)
	cols := make([]*data.Column, schema.Len())
	for i, jc := range layout.Columns {
		var (
			c   *data.Column
			err error
		)
		switch {
		case jc.Side == plan.FromRight:
			c, err = kernel.Take(right.Column(jc.Index), rsel)
		case jc.CoalesceWith >= 0 && from == keyFromRight:
			// A merged key on a Right join takes the RIGHT side's value: the left is
			// null on an unmatched right row, and on a matched row the two are equal.
			c, err = kernel.Take(right.Column(jc.CoalesceWith), rsel)
		case jc.CoalesceWith >= 0 && from == keyFromEither:
			c, err = eitherKey(schema.Field(i).Type, left.Column(jc.Index), lsel,
				right.Column(jc.CoalesceWith), rsel)
		case jc.CoalesceWith >= 0 && from == keyFromEitherRight:
			c, err = eitherKey(schema.Field(i).Type, right.Column(jc.CoalesceWith), rsel,
				left.Column(jc.Index), lsel)
		case run:
			c = left.Column(jc.Index).Slice(start, n)
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
		// Strict, as castKey is: a value the output type cannot hold has already
		// been refused there, so this cannot fail on a key that matched or was
		// padded — and if it ever did, a null in its place would be a lie.
		if f := schema.Field(i); c.DType() != f.Type {
			if c, err = castKey("join", c, f.Type); err != nil {
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

// runOf reports whether sel is start, start+1, and so on, all rows of b: a run a
// Slice shares rather than a Take gathers. A NullIndex is never part of one.
func runOf(sel []int32, b *data.Batch) (int, bool) {
	if len(sel) == 0 || b == nil {
		return 0, false
	}
	start := sel[0]
	if start < 0 || int(start)+len(sel) > b.Rows() {
		return 0, false
	}
	for i, r := range sel {
		if r != start+int32(i) {
			return 0, false
		}
	}
	return int(start), true
}

// keyFrom says which side a merged key is read from, by join kind.
func (p *joinProbeOp) keyFrom() keyFrom {
	switch p.spec.kind {
	case plan.JoinRight:
		return keyFromRight
	case plan.JoinFull:
		if p.spec.swapped {
			return keyFromEitherRight
		}
		return keyFromEither
	case plan.JoinInner:
		if p.spec.swapped {
			// The plan's inner join reads its left key, which is the table's here.
			return keyFromRight
		}
	}
	return keyFromLeft
}

// keyFrom is the side a merged join key is gathered from.
type keyFrom uint8

const (
	// keyFromLeft: every output row has a left row, so the left key is there.
	keyFromLeft keyFrom = iota
	// keyFromRight: a right join's unmatched right rows have no left row.
	keyFromRight
	// keyFromEither: a full join can lack either side, so the key is the left's
	// where there is one and the right's where there is not — coalesce(l, r).
	keyFromEither
	// keyFromEitherRight is keyFromEither preferring the right: a full join run with
	// its inputs exchanged, whose plan's left is the table (step 314).
	keyFromEitherRight
)

// eitherKey is a full join's merged key: the left value where the row has one, the
// right value where it does not, both in the promoted type the layout gives it. On a
// matched row the two are equal; the right is read only where the left is absent.
//
// It is what audit.md J9 asked for. The resolver refused JoinCoalesce(true) on a
// full join, saying ursus had no coalesce expression to merge the two with — and
// ursus.Coalesce existed, as a conditional over kernel.Select, which this is.
func eitherKey(out dtype.DataType, l *data.Column, lsel []int32, r *data.Column, rsel []int32) (*data.Column, error) {
	lk, err := kernel.Take(l, lsel)
	if err != nil {
		return nil, err
	}
	rk, err := kernel.Take(r, rsel)
	if err != nil {
		return nil, err
	}
	if lk.DType() != out {
		if lk, err = castKey("join", lk, out); err != nil {
			return nil, err
		}
	}
	if rk.DType() != out {
		if rk, err = castKey("join", rk, out); err != nil {
			return nil, err
		}
	}
	present := data.NewBool("present", lk.Validity(), bitmap.AllSet(lk.Len()))
	return kernel.Select(l.Name(), present, lk, rk)
}
