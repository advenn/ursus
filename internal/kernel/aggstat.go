package kernel

import (
	"math"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Statistical and positional accumulators: var, std, product, arg_min, arg_max,
// median and quantile.
//
// Every one follows the shape agg.go established — per-group state in a slice
// indexed by group ordinal, Reserve grows and never shrinks, AddBatch gates on
// validity, Merge folds positionally, Finish slices to nGroups — so the notes here
// are only about what is genuinely different.

// --- variance and standard deviation -------------------------------------------

// varAcc implements Var and Std with Welford's online algorithm.
//
// # Not sum-of-squares
//
// The textbook E[x²] − E[x]² needs one pass and two running sums, and loses
// catastrophic precision when the mean is large relative to the spread: the two
// terms are nearly equal and their difference is almost all rounding error.
// var([1e9, 1e9+1, 1e9+2]) comes out as 0, or negative. Welford keeps the mean and
// the sum of squared deviations FROM that mean, so the cancellation never happens.
//
// # Merge is Chan's parallel formula, not addition
//
// Two partial (n, mean, M2) triples do not combine by adding: the deviations were
// measured from two different means. The correction term is
// delta²·nA·nB/(nA+nB), which is exactly the between-group contribution the pooled
// mean introduces. Getting this wrong is invisible in a single-threaded run and
// wrong under every partition, which is what TestMergeEquivalence exists to catch.
type varAcc struct {
	ddof uint8
	std  bool

	n    []uint64
	mean []float64
	m2   []float64
}

func (a *varAcc) Reserve(n int) {
	for len(a.n) < n {
		a.n = append(a.n, 0)
		a.mean = append(a.mean, 0)
		a.m2 = append(a.m2, 0)
	}
}

func (a *varAcc) AddBatch(groups []int32, col *data.Column) error {
	vals, err := widenFloat(col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		x := vals[i]
		a.n[g]++
		delta := x - a.mean[g]
		a.mean[g] += delta / float64(a.n[g])
		a.m2[g] += delta * (x - a.mean[g])
	}
	return nil
}

// Merge is Chan's parallel variance update, which is exactly why var and std can
// run N-ways at all: two independently-computed (n, mean, m2) triples combine
// without revisiting a single value.
func (a *varAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*varAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into varAcc", other)
	}
	if a.ddof != o.ddof || a.std != o.std {
		return uerr.Internalf("kernel: cannot merge a var/std accumulator with " +
			"different parameters")
	}
	a.Reserve(mergeCap(remap, len(o.n)))
	mergeEach(remap, len(o.n), func(dst, src int) {
		nb := o.n[src]
		if nb == 0 {
			return
		}
		na := a.n[dst]
		if na == 0 {
			a.n[dst], a.mean[dst], a.m2[dst] = nb, o.mean[src], o.m2[src]
			return
		}
		fa, fb := float64(na), float64(nb)
		total := fa + fb
		delta := o.mean[src] - a.mean[dst]
		a.mean[dst] += delta * fb / total
		a.m2[dst] += o.m2[src] + delta*delta*fa*fb/total
		a.n[dst] = na + nb
	})
	return nil
}

func (a *varAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]float64, nGroups)
	seen := make([]bool, nGroups)
	for i := range nGroups {
		// n − ddof <= 0 is NULL, not zero and not NaN: a sample variance of one
		// observation is undefined, and reporting 0 would claim the data has no
		// spread rather than that the question has no answer.
		if a.n[i] <= uint64(a.ddof) {
			continue
		}
		v := a.m2[i] / float64(a.n[i]-uint64(a.ddof))
		if a.std {
			v = math.Sqrt(v)
		}
		out[i] = v
		seen[i] = true
	}
	return data.NewFixed(name, dtype.Float64, out, seenBitmap(seen, nGroups)), nil
}

// --- product -------------------------------------------------------------------

// productAcc multiplies the non-null values of each group.
//
// It accumulates in Float64 even for integer input, which is where product
// knowingly gives a weaker guarantee than sum. Sum widened to Int128 so that
// overflow became unreachable; product cannot follow, because i128 has no
// multiplication. Float64 is exact to 2^53 and approximate above it — whereas an
// Int64 accumulator would WRAP, silently, after roughly twenty ordinary factors.
// An approximate large answer beats a precise wrong one.
type productAcc struct {
	p    []float64
	seen []bool
}

func (a *productAcc) Reserve(n int) {
	for len(a.p) < n {
		a.p = append(a.p, 1) // the multiplicative identity, not zero
		a.seen = append(a.seen, false)
	}
}

func (a *productAcc) AddBatch(groups []int32, col *data.Column) error {
	vals, err := widenFloat(col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		a.p[g] *= vals[i]
		a.seen[g] = true
	}
	return nil
}

func (a *productAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*productAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into productAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.p)))
	mergeEach(remap, len(o.p), func(dst, src int) {
		if o.seen[src] {
			a.p[dst] *= o.p[src]
			a.seen[dst] = true
		}
	})
	return nil
}

// Finish returns NULL for a group with no non-null values, not 1.
//
// The empty product is 1 mathematically, but this follows sum: an all-null group
// returns NULL so that "nothing to multiply" stays distinguishable from "the
// factors multiplied to one".
func (a *productAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return data.NewFixed(name, dtype.Float64, a.p[:nGroups],
		seenBitmap(a.seen, nGroups)), nil
}

// --- arg_min / arg_max ----------------------------------------------------------

// argExtremumAcc reports the POSITION of a group's smallest or largest value.
//
// # The position is within the group, and counts every row
//
// arg_min over group [null, 5, 3] is 2, not 1: the index is into the group's rows
// as they arrived, nulls included, so it can be used to index anything else
// gathered from the same group. Nulls are skipped as CANDIDATES — they can never
// win — but they still occupy a position.
//
// AddBatch is never told its batch's offset in the stream, and there is nowhere to
// get one: hashAggSink does not track it. So the accumulator counts rows per group
// itself, which is also what makes the index group-relative rather than
// frame-relative.
//
// # Merge has to shift
//
// `other` consumed a LATER portion of the input, so its recorded positions are
// relative to the start of that portion. Folding it in means adding this
// accumulator's own row count for the group. That shift is why the merge-equivalence
// test folds both left-to-right and right-to-left: a left fold alone would let a
// missing shift pass whenever the receiver happened to be the whole prefix.
type argExtremumAcc struct {
	max bool

	n     []uint64       // rows seen per group, including nulls
	best  []*data.Column // the winning value, as a single-row column
	bestI []uint64       // its position within the group
}

func (a *argExtremumAcc) Reserve(n int) {
	for len(a.n) < n {
		a.n = append(a.n, 0)
		a.best = append(a.best, nil)
		a.bestI = append(a.bestI, 0)
	}
}

func (a *argExtremumAcc) AddBatch(groups []int32, col *data.Column) error {
	cmp, err := valueComparator(col)
	if err != nil {
		return err
	}
	valid := col.Validity()

	// Best row within this batch first, exactly as extremumAcc does, so the
	// cross-batch comparison — which needs a materialised single-row column — runs
	// once per group per batch rather than once per row.
	bestRow := map[int32]int{}
	bestPos := map[int32]uint64{}
	for i, g := range groups {
		pos := a.n[g]
		a.n[g]++
		if !valid.Get(i) {
			continue
		}
		if prev, ok := bestRow[g]; !ok {
			bestRow[g], bestPos[g] = i, pos
		} else if c := cmp(i, prev); (a.max && c > 0) || (!a.max && c < 0) {
			// Strict: a tie keeps the earlier row, matching min/max and making the
			// answer independent of batch boundaries.
			bestRow[g], bestPos[g] = i, pos
		}
	}

	for g, row := range bestRow {
		cand, err := Take(col, []int32{int32(row)})
		if err != nil {
			return err
		}
		better, err := a.better(a.best[g], cand)
		if err != nil {
			return err
		}
		if better {
			a.best[g], a.bestI[g] = cand, bestPos[g]
		}
	}
	return nil
}

// better reports whether cand beats the incumbent. A nil incumbent always loses; a
// tie always keeps the incumbent, which is what makes the earliest position win.
func (a *argExtremumAcc) better(incumbent, cand *data.Column) (bool, error) {
	if incumbent == nil {
		return true, nil
	}
	pair, err := concatColumn([]*data.Column{incumbent, cand}, 2)
	if err != nil {
		return false, err
	}
	cmp, err := valueComparator(pair)
	if err != nil {
		return false, err
	}
	c := cmp(1, 0) // cand versus incumbent
	if a.max {
		return c > 0, nil
	}
	return c < 0, nil
}

// Merge shifts o's positions by this accumulator's row count, which only makes
// sense if o saw a strictly LATER contiguous portion of the input. That is what
// makes arg_min and arg_max order-dependent, and why the parallel aggregation
// driver refuses them — see expr.AggOp.IsOrderDependent.
func (a *argExtremumAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*argExtremumAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into argExtremumAcc", other)
	}
	if a.max != o.max {
		return uerr.Internalf("kernel: cannot merge arg_min and arg_max")
	}
	a.Reserve(mergeCap(remap, len(o.n)))
	var ferr error
	mergeEach(remap, len(o.n), func(dst, src int) {
		if ferr != nil {
			return
		}
		if o.best[src] != nil {
			better, err := a.better(a.best[dst], o.best[src])
			if err != nil {
				ferr = err
				return
			}
			if better {
				// The shift: o's positions are relative to the later portion it saw.
				a.best[dst], a.bestI[dst] = o.best[src], a.n[dst]+o.bestI[src]
			}
		}
		a.n[dst] += o.n[src]
	})
	return ferr
}

func (a *argExtremumAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]uint64, nGroups)
	seen := make([]bool, nGroups)
	for i := range nGroups {
		if a.best[i] == nil {
			continue // no non-null value, so no position to report
		}
		out[i] = a.bestI[i]
		seen[i] = true
	}
	return data.NewFixed(name, dtype.Uint64, out, seenBitmap(seen, nGroups)), nil
}

// --- median and quantile ---------------------------------------------------------

// quantileAcc implements Median and Quantile.
//
// # The one accumulator that retains its input
//
// A quantile is HOLISTIC: no bounded state answers it, because the value at rank
// 0.99 is not determined until the last row has been seen. So this keeps every
// non-null value of every group, and its memory is O(rows), not O(groups).
//
// That makes the aggregate sink behave like sortSink and joinBuildSink, which both
// retain their input and say so, rather than like hashAggSink, whose memory is
// otherwise O(distinct keys). The difference is per-query — a group-by with no
// quantile in it is unaffected.
//
// And it is the one thing spilling did NOT fix. Step 12 partitions the key space to
// disk, which bounds a group-by whose problem is CARDINALITY; this accumulator's
// problem is inside one key, and no depth of partitioning divides a key. So a
// spilling group-by refuses exactly when an accumulator's state is O(rows) — see
// hashAggSink.overBudget, which states that as a property rather than a heuristic.
//
// The symmetry that names the next step: quantileAcc.Merge is an append and
// nuniqueAcc.Merge is a set union, so the two accumulators the freeze cannot bound
// are exactly the two whose Merge is ORDER-INDEPENDENT. Spilling within a hot key
// is therefore possible for precisely the two aggregates that need it.
//
// An approximate variant (t-digest, or a sampling sketch) would bound this, at the
// cost of an error term that has to be specified and defended. Nothing in the
// design notes commits to one, so exact is what ships.
type quantileAcc struct {
	q      float64
	interp expr.Interpolation
	vals   [][]float64
}

func (a *quantileAcc) Reserve(n int) {
	for len(a.vals) < n {
		a.vals = append(a.vals, nil)
	}
}

func (a *quantileAcc) AddBatch(groups []int32, col *data.Column) error {
	vals, err := widenFloat(col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue // nulls are skipped, as in every other aggregate
		}
		a.vals[g] = append(a.vals[g], vals[i])
	}
	return nil
}

func (a *quantileAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*quantileAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into quantileAcc", other)
	}
	if a.q != o.q || a.interp != o.interp {
		return uerr.Internalf("kernel: cannot merge quantile accumulators with " +
			"different parameters")
	}
	a.Reserve(mergeCap(remap, len(o.vals)))
	mergeEach(remap, len(o.vals), func(dst, src int) {
		a.vals[dst] = append(a.vals[dst], o.vals[src]...)
	})
	// Order within a group does not matter — Finish sorts — so no shift is needed
	// and this merge is genuinely commutative, unlike arg_min's.
	return nil
}

func (a *quantileAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]float64, nGroups)
	seen := make([]bool, nGroups)
	for i := range nGroups {
		v := a.vals[i]
		if len(v) == 0 {
			continue
		}
		slices.SortFunc(v, compareTotalF64)
		out[i] = quantileOf(v, a.q, a.interp)
		seen[i] = true
	}
	return data.NewFixed(name, dtype.Float64, out, seenBitmap(seen, nGroups)), nil
}

// compareTotalF64 is ursus's total order over floats, not IEEE's partial one: NaN
// equals NaN and sorts above everything, and -0.0 equals +0.0.
//
// Sorting with plain `<` would place NaNs unpredictably and make the median of a
// column depend on the input order. Using the same order Sort and GroupBy use is
// what lets `median(x)` and `sort(x)` agree about where the NaNs went.
func compareTotalF64(a, b float64) int {
	an, bn := math.IsNaN(a), math.IsNaN(b)
	switch {
	case an && bn:
		return 0
	case an:
		return 1
	case bn:
		return -1
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0 // covers -0.0 == +0.0
	}
}

// quantileOf evaluates q over an already-sorted slice.
//
// The rank is q·(n−1), the convention numpy, Polars and DuckDB share: q=0 is the
// minimum and q=1 the maximum, with no extrapolation past either end.
func quantileOf(v []float64, q float64, interp expr.Interpolation) float64 {
	n := len(v)
	if n == 1 {
		return v[0]
	}
	pos := q * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	// Guard the ends against a float rank landing a hair outside the slice.
	lo = max(0, min(lo, n-1))
	hi = max(0, min(hi, n-1))
	frac := pos - float64(lo)

	switch interp {
	case expr.InterpLower:
		return v[lo]
	case expr.InterpHigher:
		return v[hi]
	case expr.InterpNearest:
		if frac < 0.5 {
			return v[lo]
		}
		return v[hi]
	case expr.InterpMidpoint:
		return (v[lo] + v[hi]) / 2
	default: // InterpLinear
		return v[lo] + frac*(v[hi]-v[lo])
	}
}

// --- any / all_true -------------------------------------------------------------

// newBoolExtremum builds Any and AllTrue.
//
// On a Boolean column false sorts below true, so the largest value is "any is true"
// and the smallest is "all are true" — extremumBool computes both already, with
// nulls skipped and an all-null group returning NULL. There is no second
// implementation, only a second name.
//
// They are separate AggOps rather than sugar for Min and Max so that Explain prints
// any() and the type error for a non-Boolean operand names the function the user
// actually called.
func newBoolExtremum(op expr.AggOp) Accumulator {
	return &extremumBool{out: dtype.Bool, max: op == expr.AggAny}
}

func (a *varAcc) NBytes() int64 {
	return int64(len(a.n))*8 + int64(len(a.mean))*8 + int64(len(a.m2))*8
}

func (a *productAcc) NBytes() int64 { return int64(len(a.p))*8 + int64(len(a.seen)) }

func (a *argExtremumAcc) NBytes() int64 {
	return int64(len(a.n))*8 + int64(len(a.bestI))*8 + colBytes(a.best)
}

// NBytes for quantile is the one that matters. It keeps EVERY value it has seen,
// per group, so its state is O(rows) rather than O(groups) — the single place
// where an aggregation's memory is not bounded by cardinality.
func (a *quantileAcc) NBytes() int64 {
	n := int64(len(a.vals)) * 24 // one slice header per group
	for _, v := range a.vals {
		n += int64(cap(v)) * 8
	}
	return n
}
