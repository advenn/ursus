package kernel

import (
	"math"
	"math/bits"
	"slices"
	"unsafe"

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
//
// # Shifted by each group's first value
//
// Welford's alone is not enough when the spread is small beside the mean: the
// running mean of 1e9+1, 1e9+2 and 1e9+4 rounds in its eighth digit of the spread,
// and the variance came out 2.333333283662796, where it is 7/3 (step 176). So each
// group's values are shifted by its first, k, and mean is the mean of the shifted
// values. M2 is the same either way, since a shift moves every value and the mean
// alike.
//
// Where the shift itself overflows, two finite values more than float64's range
// apart, the group moves back to no shift: its mean takes k back and k becomes 0.
// The unshifted update then meets the overflow it already handles below.
//
// # A group's state is one struct
//
// One cache line a row, where four slices were four. The shift costs about a tenth
// of the accumulator's time: 17 to 19 ms for a million rows over 200k groups,
// measured alone (step 176). Eight digits are worth it.
type varAcc struct {
	ddof uint8
	std  bool

	st []varState
}

type varState struct {
	n    uint64
	k    float64 // the shift: the group's first value, or 0 once that overflowed
	mean float64
	m2   float64
}

// unshift moves the state to no shift.
func (s *varState) unshift() {
	s.mean += s.k
	s.k = 0
}

func (a *varAcc) Reserve(n int) { a.st = Extend(a.st, n, varState{}) }

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
		s := &a.st[g]
		if s.n == 0 {
			s.k = vals[i]
		}
		x := vals[i] - s.k
		if math.IsInf(x, 0) && !math.IsInf(vals[i], 0) && !math.IsInf(s.k, 0) {
			s.unshift()
			x = vals[i]
		}
		s.n++
		n := float64(s.n)
		delta := x - s.mean
		if math.IsInf(delta, 0) && !math.IsInf(x, 0) && !math.IsInf(s.mean, 0) {
			// The difference of two finite values overflowed. Welford's mean update
			// then made the mean infinite too, and m2 took -Inf*(+Inf): the variance
			// of [1e308, -1e308] came out NEGATIVE. Updated without forming delta,
			// the mean stays finite and m2 takes the +Inf it truly overflows to.
			s.mean += x/n - s.mean/n
		} else {
			s.mean += delta / n
		}
		s.m2 += delta * (x - s.mean)
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
	a.Reserve(mergeCap(remap, len(o.st)))
	mergeEach(remap, len(o.st), func(dst, src int) {
		b, d := o.st[src], &a.st[dst]
		if b.n == 0 {
			return
		}
		if d.n == 0 {
			*d = b
			return
		}
		fa, fb := float64(d.n), float64(b.n)
		total := fa + fb
		// b's mean, moved to this group's shift; both unshifted if the two shifts are
		// too far apart to subtract.
		shift := b.k - d.k
		om := b.mean + shift
		if math.IsInf(shift, 0) && !math.IsInf(b.k, 0) && !math.IsInf(d.k, 0) {
			d.unshift()
			om = b.mean + b.k
		}
		delta := om - d.mean
		d.mean += delta * fb / total
		d.m2 += b.m2 + delta*delta*fa*fb/total
		d.n += b.n
	})
	return nil
}

func (a *varAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]float64, nGroups)
	seen := make([]bool, nGroups)
	for i, s := range a.st[:nGroups] {
		// n − ddof <= 0 is NULL, not zero and not NaN: a sample variance of one
		// observation is undefined, and reporting 0 would claim the data has no
		// spread rather than that the question has no answer.
		if s.n <= uint64(a.ddof) {
			continue
		}
		v := s.m2 / float64(s.n-uint64(a.ddof))
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
	p     []float64
	seen  []bool
	exact bool // the input is an integer or a Decimal, which have no −0
}

func (a *productAcc) Reserve(n int) {
	a.p = Extend(a.p, n, 1) // the multiplicative identity, not zero
	a.seen = Extend(a.seen, n, false)
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
//
// An integer or Decimal product of zero is +0. The float accumulator turns
// 3 × 0 × −5 into −0, which an integer product cannot be, and which reads back as
// a different value: it sorts before +0 in a total order, divides into −Inf, and
// prints as -0.
func (a *productAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	if a.exact {
		for g, v := range a.p[:nGroups] {
			if v == 0 {
				a.p[g] = 0
			}
		}
	}
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
//
// # Typed keys a group, not a one-row column (step 176)
//
// It kept each group's winning value as a one-row column, and compared a batch's
// candidate with it by concatenating the two: the shape step 168 removed from First
// and Last. Over 200k groups it took 7.8 s where MinBy, on the same data, took
// 111 ms. It now keeps MinBy's typed key a group (byKeys), and finds each group's
// best row of a batch before comparing it with the kept key.
type argExtremumAcc struct {
	max  bool
	keys byOrder

	n     []uint64 // rows seen per group, including nulls
	bestI []uint64 // the winner's position within the group
	has   []bool   // whether the group has a winner: a non-null value

	// cand[g] and candI[g] are group g's best row of the batch seen[g] names, and
	// its position.
	cand  []int32
	candI []uint64
	seen  []int32
	batch int32
}

func newArgExtremum(in dtype.DataType, max bool) (Accumulator, error) {
	keys, err := newByOrder(in)
	if err != nil {
		return nil, err
	}
	return &argExtremumAcc{max: max, keys: keys}, nil
}

func (a *argExtremumAcc) Reserve(n int) {
	a.n = Extend(a.n, n, 0)
	a.bestI = Extend(a.bestI, n, 0)
	a.has = Extend(a.has, n, false)
	a.cand = Extend(a.cand, n, 0)
	a.candI = Extend(a.candI, n, 0)
	a.seen = Extend(a.seen, n, 0)
	a.keys.reserve(n)
}

// better reports whether c, a candidate's comparison with the incumbent, wins.
// Strictly: a tie keeps the earlier row, matching min and max and making the
// answer independent of batch boundaries.
func (a *argExtremumAcc) better(c int) bool {
	if a.max {
		return c > 0
	}
	return c < 0
}

func (a *argExtremumAcc) AddBatch(groups []int32, col *data.Column) error {
	if err := a.keys.load(col); err != nil {
		return err
	}
	a.batch++
	valid := col.Validity()
	var touched []int32
	for i, g := range groups {
		pos := a.n[g]
		a.n[g]++
		if !valid.Get(i) {
			continue
		}
		if a.seen[g] != a.batch {
			a.seen[g], a.cand[g], a.candI[g] = a.batch, int32(i), pos
			touched = append(touched, g)
		} else if a.better(a.keys.rows(i, int(a.cand[g]))) {
			a.cand[g], a.candI[g] = int32(i), pos
		}
	}
	for _, g := range touched {
		i := int(a.cand[g])
		if !a.has[g] || a.better(a.keys.vsKept(i, g)) {
			a.keys.keep(i, g)
			a.bestI[g], a.has[g] = a.candI[g], true
		}
	}
	return nil
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
		if o.has[src] {
			win := !a.has[dst]
			if !win {
				c, err := a.keys.merged(o.keys, src, dst)
				if err != nil {
					ferr = err
					return
				}
				win = a.better(c)
			}
			if win {
				// The shift: o's positions are relative to the later portion it saw.
				a.keys.adopt(o.keys, src, dst)
				a.bestI[dst], a.has[dst] = a.n[dst]+o.bestI[src], true
			}
		}
		a.n[dst] += o.n[src]
	})
	return ferr
}

func (a *argExtremumAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	// A group with no non-null value has no position to report.
	return data.NewFixed(name, dtype.Uint64, slices.Clone(a.bestI[:nGroups]),
		seenBitmap(a.has, nGroups)), nil
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
// A symmetry that USED to name the next step, and no longer holds: quantileAcc.Merge
// is an append and nuniqueAcc.Merge is a set union, so for two steps the accumulators
// the freeze could not bound were exactly the ones whose Merge is ORDER-INDEPENDENT,
// and spilling within a hot key looked possible for precisely the aggregates that
// need it.
//
// Step 46 added a third. implodeAcc is O(rows) for the same reason these two are, and
// its Merge is order-DEPENDENT — a list's element order is its data, so folding two
// partial groups requires the second to have seen a strictly later portion. The
// biconditional is gone: within-key spilling would cover quantile and n_unique and
// would have to refuse implode, which makes it a narrower prize than it looked.
//
// An approximate variant (t-digest, or a sampling sketch) would bound this, at the
// cost of an error term that has to be specified and defended. Nothing in the
// design notes commits to one, so exact is what ships.
type quantileAcc struct {
	q      float64
	interp expr.Interpolation
	vals   [][]float64

	// out is the answer's type: Float64, or for a median of instants or durations
	// their own, the float answer truncated toward zero, as Polars' (step 173). scale
	// multiplies each value as it is added: a Date's days are microseconds in its
	// Datetime(us) answer, and every other value is itself, 0 meaning 1.
	out   dtype.DataType
	scale float64

	// Past its sink's budget its values go to sp, and cnt counts a group's values
	// there (quantspill.go, step 220).
	sp  ValueSpill
	cnt []int
}

// microsPerDay is a Date's day in a Datetime(us).
const microsPerDay = 86_400_000_000

func (a *quantileAcc) Reserve(n int) { a.vals = Extend(a.vals, n, nil) }

func (a *quantileAcc) AddBatch(groups []int32, col *data.Column) error {
	vals, err := widenFloat(col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	scale := a.scale
	if scale == 0 {
		scale = 1
	}
	for i, g := range groups {
		if !valid.Get(i) {
			continue // nulls are skipped, as in every other aggregate
		}
		a.vals[g] = append(a.vals[g], vals[i]*scale)
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
	if a.sp != nil || o.sp != nil {
		// Only a serial sink spills its values: parallel workers go serial first.
		return uerr.Internalf("kernel: cannot merge a quantile whose values spilled")
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
	if a.sp != nil {
		if err := a.finishSpilled(out, seen); err != nil {
			return nil, err
		}
	} else {
		for i := range nGroups {
			v := a.vals[i]
			if len(v) == 0 {
				continue
			}
			out[i] = quantileOf(v, a.q, a.interp)
			seen[i] = true
		}
	}
	if a.out.IsTemporal() {
		ticks := make([]int64, nGroups)
		for i, v := range out {
			ticks[i] = int64(v) // toward zero, as Polars casts its float median
		}
		return data.NewFixed(name, a.out, ticks, seenBitmap(seen, nGroups)), nil
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
// lerp interpolates a fraction t of the way from a to b, a <= b.
//
// At an exact rank (t == 0) or between equal neighbours it is the value itself. The
// plain a + t*(b-a) was not: at q = 0 over [-inf, 1, 2] it is -inf + 0*inf, NaN,
// where Polars and DuckDB answer -inf. Between values of opposite sign b-a can
// overflow — the median of [-1.7e308, 1.7e308] was +Inf — so there the weights are
// applied to each end instead, which cannot (DuckDB's form). Of one sign, b-a is
// representable and the plain form is the more accurate.
func lerp(a, b, t float64) float64 {
	switch {
	case t == 0 || a == b:
		return a
	case t == 1:
		return b
	case (a < 0) != (b < 0):
		return (1-t)*a + t*b
	}
	return a + t*(b-a)
}

// midpoint is (a+b)/2 without overflowing: a+b overflows for two huge values of one
// sign — the midpoint of [1e308, 1.5e308] was +Inf — and b-a does not.
func midpoint(a, b float64) float64 {
	switch {
	case a == b:
		return a
	case (a < 0) != (b < 0):
		return (a + b) / 2
	}
	return a + (b-a)/2
}

// quantileOf is the q-quantile of v, which it reorders: the values at the two ranks
// around q·(n-1), interpolated.
//
// # Selection, not a sort
//
// It needs two order statistics, adjacent ones, and used to sort the whole group to
// read them: O(n log n) per group, serially, in the group-by's Finish — 18% of h2o
// gb6's CPU at 2M rows (step 105). selectNth puts the lower one in place in O(n) on
// average and leaves everything above it to its right, so the upper one is the
// least of that. The answer is the sort's: the order statistics of a total order do
// not depend on how they are found.
func quantileOf(v []float64, q float64, interp expr.Interpolation) float64 {
	n := len(v)
	if n == 1 {
		return v[0]
	}
	lo, hi, frac := ranks(n, q)
	selectNth(v, lo)
	a, b := v[lo], v[lo]
	if hi > lo {
		b = minTotalF64(v[lo+1:])
	}
	return interpolate(a, b, frac, interp)
}

// selectNth reorders v so that v[k] is what sorting v under compareTotalF64 would
// put there, with nothing after it smaller and nothing before it larger.
//
// Quickselect with a median-of-three pivot and a three-way partition, so a run of
// equal values — common in real data — is settled in one pass rather than
// recursed into. After 2·log2(n) rounds it sorts what is left, so an input built to
// defeat the pivot costs O(n log n), not O(n²).
func selectNth(v []float64, k int) {
	lo, hi := 0, len(v)-1
	for rounds := 2 * bits.Len(uint(len(v))); hi > lo; rounds-- {
		if rounds == 0 {
			slices.SortFunc(v[lo:hi+1], compareTotalF64)
			return
		}
		pivot := medianOfThree(v[lo], v[lo+(hi-lo)/2], v[hi])
		// v[lo:lt] < pivot, v[lt:i] == pivot, v[gt+1:hi+1] > pivot.
		lt, i, gt := lo, lo, hi
		for i <= gt {
			switch c := compareTotalF64(v[i], pivot); {
			case c < 0:
				v[lt], v[i] = v[i], v[lt]
				lt++
				i++
			case c > 0:
				v[i], v[gt] = v[gt], v[i]
				gt--
			default:
				i++
			}
		}
		switch {
		case k < lt:
			hi = lt - 1
		case k > gt:
			lo = gt + 1
		default:
			return
		}
	}
}

// medianOfThree is the middle of three values under compareTotalF64.
func medianOfThree(a, b, c float64) float64 {
	if compareTotalF64(a, b) > 0 {
		a, b = b, a
	}
	if compareTotalF64(b, c) > 0 {
		b = c
		if compareTotalF64(a, b) > 0 {
			b = a
		}
	}
	return b
}

// minTotalF64 is the least of v under compareTotalF64; v is not empty.
func minTotalF64(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		if compareTotalF64(x, m) < 0 {
			m = x
		}
	}
	return m
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

func (a *varAcc) NBytes() int64 { return int64(cap(a.st)) * int64(unsafe.Sizeof(varState{})) }

func (a *productAcc) NBytes() int64 { return int64(cap(a.p))*8 + int64(cap(a.seen)) }

func (a *argExtremumAcc) NBytes() int64 {
	return int64(cap(a.n)+cap(a.bestI)+cap(a.candI))*8 + int64(cap(a.has)) +
		int64(cap(a.cand)+cap(a.seen))*4 + a.keys.nbytes()
}

// NBytes for quantile is the one that matters. It keeps EVERY value it has seen,
// per group, so its state is O(rows) rather than O(groups) — the single place
// where an aggregation's memory is not bounded by cardinality.
func (a *quantileAcc) NBytes() int64 {
	n := int64(cap(a.vals))*24 + int64(cap(a.cnt))*8 // a slice header and a count per group
	for _, v := range a.vals {
		n += int64(cap(v)) * 8
	}
	return n
}
