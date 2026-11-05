package kernel

import (
	"math"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Accumulator computes one aggregate across all groups.
//
// # Vectorized, not per-group
//
// There is ONE Accumulator per aggregate expression, holding per-group state in a
// slice indexed by group ordinal — not one object per group per aggregate. With a
// million groups the latter would mean a million interface values and a virtual
// call per row; here the call is per BATCH per aggregate.
//
// # Merge is not decoration
//
// Merge combines the state of two accumulators that saw disjoint subsets of the
// same input. It is what makes PARALLEL aggregation possible in v0.3: N workers
// accumulate independently, then merge. It is implemented and TESTED now, even
// though nothing calls it concurrently yet, because a Merge written later against
// forgotten invariants is a Merge that is wrong.
//
// Spilling aggregation does NOT use it, and that is a design choice rather than an
// omission. Step 12 partitions by key rather than evicting groups, so one sink sees
// every row of a group and Merge is never reached — which is what makes First,
// Last, ArgMin and ArgMax correct on a replayed partition with no position shift.
// A design that DID merge partial groups would need each accumulator's `other` to
// have seen a strictly later portion of the input, a precondition eviction cannot
// supply; argExtremumAcc.Merge carries an explicit index shift for exactly that
// reason. See hashAggSink for the invariant.
type Accumulator interface {
	// Reserve ensures per-group state exists for n groups.
	Reserve(n int)

	// AddBatch accumulates col into the groups named by groups, where groups[i] is
	// the group ordinal of row i.
	AddBatch(groups []int32, col *data.Column) error

	// Merge folds another accumulator's state into this one. Both must be for the
	// same aggregate.
	//
	// remap carries the group NUMBERING: remap[i] is this accumulator's group id
	// for other's group i, and nil means the two already agree. Two sinks that
	// consumed different portions of the input assign ids independently — the
	// first key each one sees becomes its group 0 — so without a remap the
	// positional fold below adds one group's total to another, silently and with
	// no length mismatch to catch it. That is the piece parallel aggregation
	// needs; see physical.hashAggSink.Merge for where the mapping is built.
	//
	// A remap may instead be sparse, as SparseRemap builds it: only the groups it
	// lists are merged. A group-by's partitioned fold merges each worker into
	// several partitions at once that way, and so Merge only reads other.
	Merge(other Accumulator, remap []int32) error

	// Finish emits the result, one row per group.
	Finish(name string, nGroups int) (*data.Column, error)

	// NBytes reports the per-group state currently held, so a group-by can be
	// measured against a memory budget. Implementations must be O(groups): it is
	// called once per batch.
	NBytes() int64
}

// Extend returns s at length n, the new elements set to fill, doubling its capacity
// when it must grow.
//
// Every accumulator's Reserve grows its per-group arrays here. They appended a
// group at a time, so past 256 groups each grew by append's quarter steps, and the
// allocations on the way to a final size summed to about five times it: in h2o gb10
// the sum and the count were 12% of everything the query allocated (step 132).
// Doubling sums to about twice. It can leave half an array unused, which is why
// each NBytes counts capacity, not length (step 134).
func Extend[T any](s []T, n int, fill T) []T {
	l := len(s)
	if n <= l {
		return s
	}
	if n > cap(s) {
		g := make([]T, l, max(n, 2*cap(s)))
		copy(g, s)
		s = g
	}
	s = s[:n]
	for i := l; i < n; i++ {
		s[i] = fill
	}
	return s
}

// NewAccumulator builds the accumulator for an aggregate over a given input type.
//
// The binding comes from expr.ResolveAggBinding and is OBEYED, not re-derived —
// the same discipline the evaluator follows for binary operators, and for the same
// reason: a second copy of the type rules drifts, and the drift shows up as a
// column whose type does not match the schema the planner promised.
// params carries the aggregate's configuration — ddof, the quantile — and is the
// zero value for every aggregate that takes none.
func NewAccumulator(op expr.AggOp, in dtype.DataType, bind expr.AggBinding,
	params expr.AggParams) (Accumulator, error) {

	switch op {
	case expr.AggCount:
		return &countAcc{mode: countValues}, nil
	case expr.AggLen:
		return &countAcc{mode: countRows}, nil
	case expr.AggNullCount:
		return &countAcc{mode: countNulls}, nil
	case expr.AggNUnique:
		return &nuniqueAcc{}, nil

	case expr.AggFirst:
		return &positionAcc{out: bind.Out, last: false}, nil
	case expr.AggLast:
		return &positionAcc{out: bind.Out, last: true}, nil

	case expr.AggSum:
		return newSumAcc(in, bind)
	case expr.AggMean:
		return newMeanAcc(in, bind)
	case expr.AggMin:
		return newExtremum(in, bind.Out, false)
	case expr.AggNanMin:
		// A minimum whose order puts NaN first, so a NaN wins it, as Polars' nan_min.
		switch in.Physical().ID() {
		case dtype.TypeFloat64:
			return &extremumNum[float64]{out: bind.Out, less: func(a, b float64) bool {
				return math.IsNaN(a) && !math.IsNaN(b) || !math.IsNaN(b) && lessFloat64(a, b)
			}}, nil
		case dtype.TypeFloat32:
			return &extremumNum[float32]{out: bind.Out, less: func(a, b float32) bool {
				x, y := float64(a), float64(b)
				return math.IsNaN(x) && !math.IsNaN(y) || !math.IsNaN(y) && lessFloat32(a, b)
			}}, nil
		}
		return newExtremum(in, bind.Out, false)
	case expr.AggMax:
		return newExtremum(in, bind.Out, true)

	case expr.AggAny, expr.AggAllTrue:
		return newBoolExtremum(op), nil

	case expr.AggSkew, expr.AggKurtosis:
		return &momentAcc{kurtosis: op == expr.AggKurtosis, fisher: params.Fisher, bias: params.Bias}, nil
	case expr.AggBitAnd:
		return &bitAcc{op: bitAnd, in: in}, nil
	case expr.AggBitOr:
		return &bitAcc{op: bitOr, in: in}, nil
	case expr.AggBitXor:
		return &bitAcc{op: bitXor, in: in}, nil
	case expr.AggVar:
		return &varAcc{ddof: params.DDof, std: false}, nil
	case expr.AggStd:
		return &varAcc{ddof: params.DDof, std: true}, nil

	case expr.AggProduct:
		return &productAcc{exact: !in.IsFloat()}, nil

	case expr.AggArgMin:
		return newArgExtremum(in, false)
	case expr.AggArgMax:
		return newArgExtremum(in, true)

	case expr.AggMedian:
		// The median IS the 0.5 quantile, linearly interpolated. Giving it its own
		// accumulator would mean two implementations that must agree on the
		// even-length case forever.
		q := &quantileAcc{q: 0.5, interp: expr.InterpLinear, out: bind.Out}
		if in.ID() == dtype.TypeDate {
			q.scale = microsPerDay
		}
		return q, nil
	case expr.AggQuantile:
		// An instant's or a duration's quantile is one, as its median is (step 300).
		q := &quantileAcc{q: params.Q, interp: params.Interp, out: bind.Out}
		if in.ID() == dtype.TypeDate {
			q.scale = microsPerDay
		}
		return q, nil

	case expr.AggCorr:
		return &coMomentAcc{corr: true}, nil
	case expr.AggSpearman:
		return &spearmanAcc{}, nil
	case expr.AggEntropy:
		return &entropyAcc{base: params.Base, normalize: params.Normalize, out: bind.Out}, nil
	case expr.AggCov:
		return &coMomentAcc{ddof: params.DDof}, nil
	case expr.AggMinBy:
		return newByAcc(in, bind.Out, false)
	case expr.AggMaxBy:
		return newByAcc(in, bind.Out, true)

	case expr.AggImplode:
		return &implodeAcc{elem: in}, nil
	case expr.AggMode:
		return newModeAcc(in, params.MaintainOrder), nil
	case expr.AggTopK:
		return newTopK(in, params.K, true)
	case expr.AggBottomK:
		return newTopK(in, params.K, false)
	case expr.AggTopKBy, expr.AggBottomKBy:
		fs := in.Fields()
		if len(fs) != 2 {
			return nil, uerr.Internalf("kernel: %s over %s, not a pair", op, in)
		}
		return newTopKBy(fs[0].Type, fs[1].Type, params.K, op == expr.AggTopKBy)

	case expr.AggItem:
		return &itemAcc{pos: positionAcc{out: bind.Out}, allowEmpty: params.AllowEmpty}, nil
	case expr.AggLowerBound:
		return &boundAcc{out: bind.Out}, nil
	case expr.AggUpperBound:
		return &boundAcc{out: bind.Out, upper: true}, nil
	case expr.AggIsSorted:
		return newIsSorted(in, params.Descending, params.NullsLast)

	default:
		return nil, uerr.New(uerr.KindUnsupported, "agg",
			"aggregate %s is not implemented", op)
	}
}

// --- merging under a remap ----------------------------------------------------
//
// Ten accumulators share one loop shape: for each of other's groups, find the
// destination ordinal and fold. Writing the nil-remap branch ten times is how ten
// slightly different nil handlings get written, so it is written once here.

// mergeCap returns the number of groups the destination must hold to receive a
// merge of n source groups through remap.
//
// Under the identity it is just n. Under a remap it is one past the largest
// destination, which can exceed n: a merge that introduces keys the destination
// has never seen grows it.
func mergeCap(remap []int32, n int) int {
	if remap == nil {
		return n
	}
	if isSparse(remap) {
		most := 0
		for j := 2; j < len(remap); j += 2 {
			most = max(most, int(remap[j])+1)
		}
		return most
	}
	n = min(n, len(remap))
	most := 0
	for _, d := range remap[:n] {
		most = max(most, int(d)+1)
	}
	return most
}

// sparseMark opens a sparse remap: the pairs after it are a source group and its
// destination, and no other group of the source is merged. No dense remap can
// start with it, since a group id is never negative.
const sparseMark = math.MinInt32

// SparseRemap is a remap that merges only src[j], into dst[j] (Accumulator.Merge).
// A group-by's partitioned fold merges a worker's groups into each partition this
// way, at a cost of the groups that go there, where a dense remap of the others'
// -1s costs every group of the worker for every partition (step 150).
func SparseRemap(src, dst []int32) []int32 {
	r := make([]int32, 1, 1+2*len(src))
	r[0] = sparseMark
	for j, s := range src {
		r = append(r, s, dst[j])
	}
	return r
}

func isSparse(remap []int32) bool { return len(remap) > 0 && remap[0] == sparseMark }

// mergeEach calls fn(dst, src) for every source group.
//
// n is bounded by len(remap) as well as by the caller's count, because Reserve
// over-allocates: an accumulator's slices are usually longer than the number of
// groups actually assigned, and the remap is sized to the latter.
//
// A negative entry skips its group. A group-by's partitioned fold merges each
// worker's accumulators into several partitions' at once, each taking only the
// groups that hash to it, and reading the worker's state, never writing it
// (step 150). Every Merge here must keep to both.
func mergeEach(remap []int32, n int, fn func(dst, src int)) {
	if remap == nil {
		for i := range n {
			fn(i, i)
		}
		return
	}
	if isSparse(remap) {
		for j := 1; j+1 < len(remap); j += 2 {
			if src := int(remap[j]); src < n {
				fn(int(remap[j+1]), src)
			}
		}
		return
	}
	for i := range min(n, len(remap)) {
		if remap[i] >= 0 {
			fn(int(remap[i]), i)
		}
	}
}

// The accumulators are aggcount.go (count, len, null_count, n_unique), aggsum.go
// (sum, mean), aggminmax.go (min, max, any, all) and aggrows.go (first, last,
// implode).

// --- helpers -----------------------------------------------------------------

// seenBitmap turns per-group presence flags into a validity bitmap, dropping the
// buffer entirely when every group saw a value. The flags go in a word at a time,
// where a bit at a time was a fifth of an interpolation over a million rows (step
// 329).
func seenBitmap(seen []bool, n int) bitmap.View {
	seen = seen[:n]
	if !slices.Contains(seen, false) {
		return bitmap.AllSet(n)
	}
	vb := bitmap.NewBuilder(n)
	for len(seen) > 0 {
		k := min(len(seen), 64)
		var w uint64
		for i, s := range seen[:k] {
			if s {
				w |= 1 << uint(i)
			}
		}
		vb.AppendBits(w, k)
		seen = seen[k:]
	}
	return vb.Finish()
}

func widenSigned(c *data.Column) ([]int64, error) { return widenToInt64(c) }

func widenUnsigned(c *data.Column) ([]uint64, error) { return widenToUint64(c) }

// widenFloat reads any numeric column as float64. It is toFloat64: it used to carry
// its own copy of the Int128 arm, which a Decimal matched first — so every
// aggregate reading through it would have got the unscaled integer, 10^scale too
// large, with no error.
func widenFloat(c *data.Column) ([]float64, error) { return toFloat64(c) }

// --- accounting ----------------------------------------------------------------
//
// NBytes reports what an accumulator is holding, so a group-by can be measured
// against a memory budget.
//
// It is on the interface rather than a type switch in the physical layer because
// per-group state is where an aggregation's memory actually goes, and an
// accumulator added later with an unbounded shape — quantile keeps every value —
// would otherwise be silently unaccounted. Every implementation below is O(groups)
// so it can be called once per batch.

// colBytes sums the payload of a per-group column slice, skipping the empty slots
// Reserve leaves behind.
func colBytes(cs []*data.Column) int64 {
	n := int64(cap(cs)) * 8 // the slice of pointers
	for _, c := range cs {
		if c != nil {
			n += c.NBytes()
		}
	}
	return n
}

func (a *countAcc) NBytes() int64 { return int64(cap(a.n)) * 8 }

// NBytes for n_unique approximates the map contents: 8 bytes per group for the
// slice, plus a per-entry constant covering the string header and the bucket slot.
// The key BYTES are not counted, so a group-by over long strings under-reports;
// counting them exactly would mean walking every key on every batch.
// NBytes is EXACT now, where it used to estimate 48 bytes an entry.
//
// On q21 the estimate predicted about 300 MB against roughly 800 MB actually
// resident. That matters more here than for most accumulators: n_unique is one of
// the two that REFUSE to spill rather than spilling, so this number is what decides
// when the refusal fires, and an under-count makes it fire late.
func (a *nuniqueAcc) NBytes() int64 {
	n := a.tab.NBytes() + int64(cap(a.counts))*4 + int64(cap(a.scratch))*8
	if a.ints != nil {
		n += a.ints.NBytes()
	}
	return n
}

func (a *sumAcc) NBytes() int64 {
	return int64(cap(a.i))*16 + int64(cap(a.ni.s))*8 + int64(cap(a.carry))*8 + int64(cap(a.f))*8 +
		int64(cap(a.seen))
}

func (a *meanAcc) NBytes() int64 {
	return int64(cap(a.sum))*8 + int64(cap(a.i))*16 + int64(cap(a.ni.s))*8 + int64(cap(a.carry))*8 +
		int64(cap(a.n))*8
}

func (a *positionAcc) NBytes() int64 {
	return colBytes(a.parts) + int64(cap(a.at))*4 + int64(cap(a.seen))*4
}
