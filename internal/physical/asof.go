package physical

import (
	"context"
	"errors"
	"io"
	"sort"
	"time"

	"ursus/dtype"
	"ursus/internal/data"
	"ursus/internal/execopt"
	"ursus/internal/expr"
	"ursus/internal/kernel"
	"ursus/internal/plan"
	"ursus/internal/uerr"
)

// The as-of join: for each left row, the NEAREST right row rather than an equal one.
//
// # It reuses the equi-join's output machinery and replaces only the match rule
//
// gatherOut, the JoinLayout, evalKeys and joinBreaker are all the ordinary join's, so
// what is new here is one binary search. An unmatched left row is rsel[i] =
// kernel.NullIndex, which gatherOut has handled since step 13 built it for the
// spilling join's null bucket — so "left join semantics" costs nothing.
//
// # Sorted input makes the search a binary search
//
// The right side is bucketed by the `by` key and each bucket is in `on` order,
// because the whole input is and bucketing preserves relative order. So finding the
// last right key at or before a left key is sort.Search over one bucket — the same
// move step 14's temporalSink.rangeOf makes, for the same reason.
//
// It is O(n log m) rather than the O(n + m) a true merge walk would give. The right
// side is buffered either way, and a binary search per left row cannot drift out of
// step with a cursor the way a hand-rolled merge can.

// asOfBuildSink buffers the right side and answers nearest-key queries against it.
type asOfBuildSink struct {
	out    *dtype.Schema
	layout *plan.JoinLayout
	left   *dtype.Schema
	right  *dtype.Schema

	leftOn, rightOn expr.Node
	leftBy, rightBy []expr.Node

	strategy   plan.AsOfStrategy
	tolerance  dtype.Interval
	allowExact bool
	keyType    dtype.DataType
	loc        *time.Location
	batch      int

	mem   *execopt.Account
	parts []*data.Batch
	keys  []int64 // the ordering key, whole stream, in input order
	nRows int

	sorted bool
	badRow int
}

func (s *asOfBuildSink) Schema() *dtype.Schema { return s.out }
func (s *asOfBuildSink) Close() error          { s.mem.Release(); return nil }

func (s *asOfBuildSink) Merge(Sink) error {
	// Two partial sinks cannot be reconciled without knowing the global key order,
	// which is exactly what a partial sink does not have. windowSink and temporalSink
	// refuse for the same reason.
	return uerr.Internalf("physical: asOfBuildSink cannot be merged")
}

func (s *asOfBuildSink) Consume(ctx context.Context, in *data.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := in.Rows()
	if n == 0 {
		return nil
	}
	ks, err := asOfKeys(ctx, s.rightOn, in, s.keyType)
	if err != nil {
		return err
	}
	if err := s.checkSorted(ks, in, "right"); err != nil {
		return err
	}
	s.keys = append(s.keys, ks...)
	s.parts = append(s.parts, in)
	s.mem.Retain(in)
	s.mem.RetainBytes(int64(n) * 8)
	s.nRows += n
	return s.mem.Check()
}

// checkSorted records the first backwards step rather than failing here, so the error
// can name the side and be raised once, at Finish, alongside the left side's.
func (s *asOfBuildSink) checkSorted(ks []int64, in *data.Batch, side string) error {
	_ = side
	for i, k := range ks {
		if s.sorted && (len(s.keys) > 0 || i > 0) {
			var prev int64
			if i > 0 {
				prev = ks[i-1]
			} else {
				prev = s.keys[len(s.keys)-1]
			}
			if k < prev {
				s.sorted, s.badRow = false, s.nRows+i
			}
		}
	}
	return nil
}

func (s *asOfBuildSink) Finish(ctx context.Context) (Operator, error) {
	return s.Probe(ctx, emptyOperator{schema: s.left})
}

func (s *asOfBuildSink) Probe(ctx context.Context, probe Operator) (Operator, error) {
	if !s.sorted {
		return nil, unsortedErr("right", s.badRow)
	}
	right, err := kernel.Concat(s.right, s.parts)
	if err != nil {
		return nil, err
	}
	buckets, err := s.bucket(ctx, right)
	if err != nil {
		return nil, err
	}
	pad, err := emptyBatch(s.right)
	if err != nil {
		return nil, err
	}
	return &asOfProbeOp{
		schema: s.out, layout: s.layout, sink: s,
		probe: probe, right: right, buckets: buckets, pad: pad,
	}, nil
}

// bucket groups right row indices by the `by` key, preserving key order inside each.
func (s *asOfBuildSink) bucket(ctx context.Context, right *data.Batch) (map[string][]int32, error) {
	if len(s.rightBy) == 0 {
		rows := make([]int32, right.Rows())
		for i := range rows {
			rows[i] = int32(i)
		}
		return map[string][]int32{"": rows}, nil
	}
	cols, err := evalKeys(ctx, s.rightBy, right, nil)
	if err != nil {
		return nil, err
	}
	enc, err := kernel.NewGroupKeyEncoder("join_asof", cols)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]int32, 16)
	for i := range right.Rows() {
		k := string(enc.Encode(i))
		out[k] = append(out[k], int32(i))
	}
	return out, nil
}

func unsortedErr(side string, row int) error {
	return uerr.New(uerr.KindValue, "join_asof",
		"the %s side is not sorted on the as-of key: row %d goes backwards", side, row).
		Hint("an as-of join searches a sorted run, so both sides must be ordered by " +
			"the key it matches on").
		Hint("sort first, e.g. .Sort(ursus.Asc(ursus.Col(...))) on both inputs")
}

// asOfKeys evaluates one side's ordering key and reads it as int64 ticks or integers.
func asOfKeys(ctx context.Context, on expr.Node, in *data.Batch, to dtype.DataType) ([]int64, error) {
	c, err := evalColumn(ctx, on, in)
	if err != nil {
		return nil, err
	}
	if c.DType() != to {
		if c, err = kernel.Cast(c.Name(), to, false, c); err != nil {
			return nil, err
		}
	}
	for i := range c.Len() {
		if !c.IsValid(i) {
			// A null key has no position on the line, so it can be neither found nor
			// searched for. Dropping it would change the left height silently.
			return nil, uerr.New(uerr.KindValue, "join_asof",
				"the as-of key %q is null at row %d", c.Name(), i).
				Hint("filter the nulls out before joining")
		}
	}
	return temporalTicks(c)
}

// --- the probe --------------------------------------------------------------------

type asOfProbeOp struct {
	schema  *dtype.Schema
	layout  *plan.JoinLayout
	sink    *asOfBuildSink
	probe   Operator
	right   *data.Batch
	buckets map[string][]int32
	pad     *data.Batch

	nRows    int
	lastSeen int64 // the previous batch's final key, so the sort check spans batches
	done     bool
}

func (p *asOfProbeOp) Schema() *dtype.Schema { return p.schema }
func (p *asOfProbeOp) Close() error          { return nil }

func (p *asOfProbeOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.done {
			return nil, io.EOF
		}
		in, err := p.probe.Next(ctx)
		if errors.Is(err, io.EOF) {
			p.done = true
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}
		if in.Rows() == 0 {
			continue // an empty batch is not end-of-stream
		}
		out, err := p.match(ctx, in)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
}

// match emits one output batch: every left row, with its nearest right row or nulls.
func (p *asOfProbeOp) match(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	s := p.sink
	lk, err := asOfKeys(ctx, s.leftOn, in, s.keyType)
	if err != nil {
		return nil, err
	}
	// The LEFT side must be sorted too, and it is checked as it streams.
	for i, k := range lk {
		if p.nRows+i > 0 && k < p.lastKey(lk, i) {
			return nil, unsortedErr("left", p.nRows+i)
		}
	}

	var byEnc *kernel.GroupKeyEncoder
	if len(s.leftBy) > 0 {
		cols, err := evalKeys(ctx, s.leftBy, in, nil)
		if err != nil {
			return nil, err
		}
		if byEnc, err = kernel.NewGroupKeyEncoder("join_asof", cols); err != nil {
			return nil, err
		}
	}

	n := in.Rows()
	lsel := make([]int32, n)
	rsel := make([]int32, n)
	for i := range n {
		lsel[i] = int32(i)
		key := ""
		if byEnc != nil {
			key = string(byEnc.Encode(i))
		}
		rsel[i] = s.nearest(p.buckets[key], lk[i])
	}
	p.nRows += n
	p.lastSeen = lk[n-1]
	return gatherOut(p.schema, p.layout, in, p.right, lsel, rsel, false)
}

// lastSeen is the previous batch's final key, so the sortedness check spans batches.
func (p *asOfProbeOp) lastKey(lk []int64, i int) int64 {
	if i > 0 {
		return lk[i-1]
	}
	return p.lastSeen
}

// nearest finds the right row this left key matches, or NullIndex.
//
// This is the whole of the as-of rule, in one place, so the three strategies and the
// exact-match flag cannot disagree with each other about what "nearest" means.
func (s *asOfBuildSink) nearest(bucket []int32, k int64) int32 {
	if len(bucket) == 0 {
		return kernel.NullIndex
	}
	keys := s.keys

	// back: the last position whose key is <= k (or < k when exact matches are off).
	back := sort.Search(len(bucket), func(i int) bool {
		if s.allowExact {
			return keys[bucket[i]] > k
		}
		return keys[bucket[i]] >= k
	}) - 1
	// fwd: the first position whose key is >= k (or > k).
	fwd := sort.Search(len(bucket), func(i int) bool {
		if s.allowExact {
			return keys[bucket[i]] >= k
		}
		return keys[bucket[i]] > k
	})

	pick := -1
	switch s.strategy {
	case plan.AsOfForward:
		if fwd < len(bucket) {
			pick = fwd
		}
	case plan.AsOfNearest:
		switch {
		case back < 0:
			if fwd < len(bucket) {
				pick = fwd
			}
		case fwd >= len(bucket):
			pick = back
		default:
			// Ties go BACKWARD, matching the default strategy: with an exact match
			// available both distances are zero, and "the most recent" is the answer
			// the operator is named for.
			if k-keys[bucket[back]] <= keys[bucket[fwd]]-k {
				pick = back
			} else {
				pick = fwd
			}
		}
	default: // AsOfBackward
		if back >= 0 {
			pick = back
		}
	}
	if pick < 0 {
		return kernel.NullIndex
	}
	row := bucket[pick]
	if !s.withinTolerance(k, keys[row]) {
		return kernel.NullIndex
	}
	return row
}

// withinTolerance reports whether a candidate is close enough to be a match.
//
// A calendar tolerance is applied FROM THE LEFT KEY rather than as a fixed tick
// count, because "one month" is not a number of ticks — the same reason Interval has
// three fields. So the bound is recomputed per row, which is what makes
// Every("1mo") differ from Every("30d") on a February.
func (s *asOfBuildSink) withinTolerance(left, right int64) bool {
	if s.tolerance.IsZero() {
		return true
	}
	if !s.tolerance.IsCalendar() {
		d := left - right
		if d < 0 {
			d = -d
		}
		npt, _ := s.keyType.NanosPerTick()
		if npt <= 0 {
			return true
		}
		return d*npt <= s.tolerance.Nanos()
	}
	lt, ok := s.keyType.ToTime(left)
	if !ok {
		return true
	}
	if s.loc != nil {
		lt = lt.In(s.loc)
	}
	lo, hiOK := s.keyType.FromTime(s.tolerance.Neg().AddTo(lt))
	hi, loOK := s.keyType.FromTime(s.tolerance.AddTo(lt))
	if !hiOK || !loOK {
		return true
	}
	return right >= lo && right <= hi
}

// --- planning ---------------------------------------------------------------------

func planAsOfJoin(ctx context.Context, a *plan.AsOfJoin, opts Options) (Operator, error) {
	left, err := planPipelined(ctx, a.Left, opts)
	if err != nil {
		return nil, err
	}
	right, err := planPipelined(ctx, a.Right, opts)
	if err != nil {
		return nil, err
	}
	layout, err := a.Layout()
	if err != nil {
		return nil, err
	}
	ls, err := a.Left.Schema()
	if err != nil {
		return nil, err
	}
	rs, err := a.Right.Schema()
	if err != nil {
		return nil, err
	}

	sink := &asOfBuildSink{
		out: layout.Schema, layout: layout, left: ls, right: rs,
		leftOn: a.LeftOn, rightOn: a.RightOn,
		leftBy: a.LeftBy, rightBy: a.RightBy,
		strategy: a.Strategy, tolerance: a.Tolerance,
		allowExact: a.AllowExactMatches,
		// KeyTypes[0] is the ordering key's promoted type: asJoin puts On first.
		keyType: layout.KeyTypes[0],
		batch:   opts.batchSize(),
		mem:     opts.Budget.Account("join_asof"),
		sorted:  true,
	}
	sink.loc = temporalLocation(sink.keyType)
	return &joinBreaker{build: right, probe: left, builder: sink}, nil
}
