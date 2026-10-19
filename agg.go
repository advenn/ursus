package ursus

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
	"math"
)

// Interpolation selects how Quantile resolves a rank falling between two values.
//
// Aliased from the expression IR rather than redeclared, so the kernel and the
// public API cannot drift — the same arrangement JoinKind uses.
type Interpolation = expr.Interpolation

// Closed says which endpoints of an interval belong to it: [lo, hi), (lo, hi],
// [lo, hi] or (lo, hi). Used by IsBetween and by the temporal group-by's windows.
type Closed = expr.Closed

// The four window boundary conventions. ClosedLeft is the default for a window grid
// — an instant exactly on a boundary starts the new window — because it is the only
// one under which consecutive windows tile the line exactly.
const (
	// ClosedDefault is the zero value and means "this operation's own convention":
	// both for IsBetween, left for GroupByDynamic, right for Rolling.
	ClosedDefault = expr.ClosedDefault
	ClosedLeft    = expr.ClosedLeft
	ClosedRight   = expr.ClosedRight
	ClosedBoth    = expr.ClosedBoth
	ClosedNone    = expr.ClosedNone
)

const (
	// InterpLinear interpolates proportionally between the neighbours. It is the
	// conventional default and the only option whose result need not be an input
	// value.
	InterpLinear = expr.InterpLinear

	// InterpLower and InterpHigher take the neighbour below or above the rank.
	InterpLower  = expr.InterpLower
	InterpHigher = expr.InterpHigher

	// InterpNearest takes the closer neighbour, rounding halves upward.
	InterpNearest = expr.InterpNearest

	// InterpMidpoint averages the two neighbours regardless of the rank's position
	// between them.
	InterpMidpoint = expr.InterpMidpoint
)

// --- aggregate expressions ----------------------------------------------------
//
// Every aggregate skips nulls except Len, First and Last. That is the SQL rule and
// it is applied uniformly: an aggregate that treated nulls differently from its
// neighbours would be a permanent source of quiet mistakes.

// Sum adds the non-null values.
//
// An INTEGER sum accumulates and returns Int128, following DuckDB. Summing a
// thousand Int8 values overflows an Int8 immediately, and a wrapped sum is a
// plausible-looking wrong number that nothing downstream can detect. At 128 bits
// overflow needs 2^63 maximum-magnitude rows, i.e. it cannot happen.
//
// A FLOAT sum accumulates in Float64 even when it returns Float32, because naive
// float32 accumulation stops making progress past ~2^24 elements.
//
// A BOOL sums as 0 and 1, so its sum counts the trues, as an Int128. Count counts
// the non-null rows, falses included.
//
// Sum of a group with no non-null values is NULL, not 0 — ursus follows SQL here
// rather than Polars. `0` cannot be distinguished afterwards from a genuine zero;
// NULL can be turned into 0 with FillNull if that is what you want.
func (e Expr) Sum() Expr { return e.agg(expr.AggSum) }

// Mean is the sum of non-null values divided by the COUNT of non-null values —
// never by the group size, which would silently bias the result downward whenever
// nulls are present.
//
// Mean of a group with no non-null values is NULL, not NaN: there was nothing to
// compute, as opposed to a computation that came out undefined.
//
// The mean of an instant or a duration is one, exact and truncated toward zero, as
// Polars' (step 173): a Datetime, Time or Duration keeps its type, and a Date's mean
// is a Datetime(us), keeping the fraction of a day.
func (e Expr) Mean() Expr { return e.agg(expr.AggMean) }

// Min and Max use ursus's TOTAL order, the same one Sort uses: NaN sorts above
// everything and -0.0 equals +0.0. They therefore always agree with
// Sort(...).First() / .Last(), which IEEE comparison would not.
func (e Expr) Min() Expr { return e.agg(expr.AggMin) }
func (e Expr) Max() Expr { return e.agg(expr.AggMax) }

// Count counts NON-NULL values — SQL's COUNT(col).
func (e Expr) Count() Expr { return e.agg(expr.AggCount) }

// Len counts ROWS, including nulls — SQL's COUNT(*).
//
// Count and Len are deliberately separate and are never aliased: over a column
// with nulls they give different answers, and which one was meant is not
// recoverable after the fact.
func (e Expr) Len() Expr { return e.agg(expr.AggLen) }

// NUnique counts distinct non-null values.
//
// Nulls are SKIPPED, so NUnique over an all-null group is 0. Polars counts null as
// a distinct value; ursus follows SQL and, more importantly, follows its own other
// aggregates.
func (e Expr) NUnique() Expr { return e.agg(expr.AggNUnique) }

// First and Last are POSITIONAL and may return null.
//
// They are a selection rather than a reduction: skipping nulls would mean
// First(a) and First(b) could come from different rows, which is exactly what
// people use them together for.
func (e Expr) First() Expr { return e.agg(expr.AggFirst) }
func (e Expr) Last() Expr  { return e.agg(expr.AggLast) }

// NullCount counts the NULLS in each group — the complement of Count, and like the
// other counters it is never itself null.
func (e Expr) NullCount() Expr { return e.agg(expr.AggNullCount) }

// Any and AllTrue reduce a Boolean column.
//
// AllTrue rather than All, because All is already the top-level selector for every
// column and one name cannot mean both.
//
// Nulls are SKIPPED, so a group with no non-null value gives NULL rather than
// false (for Any) or true (for AllTrue). Polars returns the vacuous answer; ursus
// returns null, because every other aggregate here does and an inconsistent one is
// a permanent trap. FillNull makes either convention available.
func (e Expr) Any() Expr     { return e.agg(expr.AggAny) }
func (e Expr) AllTrue() Expr { return e.agg(expr.AggAllTrue) }

// Product multiplies the non-null values.
//
// Unlike Sum it returns FLOAT64 for every numeric input, including integers. Sum
// widened to Int128 so that overflow became unreachable; product cannot, because
// i128 has no multiplication — and an Int64 product wraps silently after roughly
// twenty ordinary factors. Float64 is exact to 2^53 and approximate above it, which
// is the weaker guarantee, stated rather than hidden.
//
// Product of a group with no non-null values is NULL, not 1, following Sum.
func (e Expr) Product() Expr { return e.agg(expr.AggProduct) }

// ArgMin and ArgMax give the POSITION of the smallest or largest value within the
// group, as a Uint64.
//
// The index counts every row of the group, nulls included, so it lines up with
// anything else gathered from the same group — but nulls can never win, and a group
// with no non-null value has no position and returns NULL.
//
// Ties go to the earliest row, which is what makes the answer independent of batch
// size and thread count.
func (e Expr) ArgMin() Expr { return e.agg(expr.AggArgMin) }
func (e Expr) ArgMax() Expr { return e.agg(expr.AggArgMax) }

// MinBy is the value of e at the row where by is the least, and MaxBy where it is
// the greatest: Polars' min_by and max_by. The answer is e's type, and by may be of
// any ordered type.
//
//	GroupBy(Col("store")).Agg(Col("product").MaxBy(Col("sales")))
//
// A row whose by is null is skipped, and a group where every by is null answers
// null; a null value at the chosen row is null too. A tie goes to the earliest row.
//
// Where Polars differs: by is ordered as Max and ArgMax order it, so NaN is the
// greatest value and MaxBy chooses a NaN's row, where Polars skips the NaN. Then
// v.MaxBy(by) is always v at by.ArgMax().
func (e Expr) MinBy(by Expr) Expr { return pairAgg(expr.AggMinBy, e, by, expr.AggParams{}) }
func (e Expr) MaxBy(by Expr) Expr { return pairAgg(expr.AggMaxBy, e, by, expr.AggParams{}) }

// Corr is Pearson's correlation of a and b over the rows where both are non-null:
// Polars' pl.corr. It is a Float64, named after a, and NaN, never null, where it is
// undefined: no such rows, one, or a variable that does not vary. A NaN in either
// makes it NaN.
//
//	GroupBy(Col("g")).Agg(Corr(Col("x"), Col("y")))
//
// It runs on every worker and merges by Chan's formulas, as Var does. Spearman's
// rank correlation, Polars' method="spearman", is not built.
func Corr(a, b Expr) Expr { return pairAgg(expr.AggCorr, a, b, expr.AggParams{}) }

// CorrSpearman is Spearman's rank correlation of a and b over the rows where both
// are non-null: Pearson's of their ranks, a tie the mean of its ranks and NaN above
// every number, Polars' pl.corr(method="spearman"). Fewer than two such rows, or
// values all alike, are NaN, as Polars answers. It keeps every pair of a group, as
// Median keeps its values. Polars ranks text too; this takes numbers.
func CorrSpearman(a, b Expr) Expr { return pairAgg(expr.AggSpearman, a, b, expr.AggParams{}) }

// Cov is the covariance of a and b over the rows where both are non-null: Polars'
// pl.cov. ddof is Var's: 1 for the sample covariance, Polars' default, and 0 for the
// population's. A group of ddof or fewer such rows is null, as Var is.
//
// Where Polars differs: over a single row with ddof 1, it answers 0.0 when neither
// input has a null, and null when nulls left the one row; here both are null.
func Cov(a, b Expr, ddof int) Expr {
	if ddof < 0 || ddof > 255 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "cov",
			"ddof must be between 0 and 255, got %d", ddof).
			Hint("0 is the population statistic and 1 the sample one")})
	}
	return pairAgg(expr.AggCov, a, b, expr.AggParams{DDof: uint8(ddof)})
}

// pairAgg is an aggregate of two inputs, carried as expr.PairOf's struct.
func pairAgg(op expr.AggOp, a, b Expr, p expr.AggParams) Expr {
	return wrap(&expr.Agg{Op: op, Child: expr.PairOf(a.node(), b.node()), Params: p})
}

// Var and Std are the variance and standard deviation of the non-null values.
//
// ddof is the delta degrees of freedom: 0 for the population statistic, 1 for the
// sample one. It has no default because there is no defensible default — Polars and
// pandas use 1, numpy uses 0, and silently picking either produces a number that is
// wrong for half its readers.
//
// A group with ddof or fewer values returns NULL: the sample variance of a single
// observation is undefined, and 0 would claim the data has no spread rather than
// that the question has no answer.
//
// Computed with Welford's algorithm, so a column of large near-equal values gives
// the right answer rather than the zero (or negative) that E[x²]−E[x]² produces.
//
// A temporal column, a Duration included, is refused for now; Cast(Int64) gives
// its ticks.
func (e Expr) Var(ddof int) Expr { return e.varStd(expr.AggVar, ddof) }
func (e Expr) Std(ddof int) Expr { return e.varStd(expr.AggStd, ddof) }

func (e Expr) varStd(op expr.AggOp, ddof int) Expr {
	if ddof < 0 || ddof > 255 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, op.String(),
			"ddof must be between 0 and 255, got %d", ddof).
			Hint("0 is the population statistic and 1 the sample one")})
	}
	return e.aggP(op, expr.AggParams{DDof: uint8(ddof)})
}

// Median is the 0.5 quantile with linear interpolation, so an even-sized group
// gives the mean of the two middle values.
//
// It returns Float64 for every numeric input, and skips nulls. The median of
// instants or durations is one, truncated toward zero, as Polars' (step 173): its
// type kept, but a Date's, which is a Datetime(us). Quantile still refuses a temporal
// column; Cast(Int64) gives its ticks.
func (e Expr) Median() Expr { return e.agg(expr.AggMedian) }

// Skew is the skewness of the non-null values, a Float64: Polars' skew. bias true is
// the biased estimator, m3/m2^1.5, Polars' default; false corrects it, and is null
// below 3 values. Kurtosis is m4/m2², less 3 with fisher, Fisher's excess and Polars'
// default; bias false corrects it, and is null below 4 values. Over values all alike
// either is NaN, as Polars'.
//
// The moments are kept online and merged pairwise, so a group-by runs N-ways; Polars
// computes them in two passes, so the two agree to the last few bits.
func (e Expr) Skew(bias bool) Expr { return e.aggP(expr.AggSkew, expr.AggParams{Bias: bias}) }
func (e Expr) Kurtosis(fisher, bias bool) Expr {
	return e.aggP(expr.AggKurtosis, expr.AggParams{Fisher: fisher, Bias: bias})
}

// BitwiseAnd, BitwiseOr and BitwiseXor fold the non-null values by their bits, an
// integer or a Boolean, of the input's type: Polars' bitwise_and and its kin. A group
// of no value is null.
func (e Expr) BitwiseAnd() Expr { return e.agg(expr.AggBitAnd) }
func (e Expr) BitwiseOr() Expr  { return e.agg(expr.AggBitOr) }
func (e Expr) BitwiseXor() Expr { return e.agg(expr.AggBitXor) }

// NanMin is the minimum, NaN where a value is NaN: Polars' nan_min. NanMax is Max,
// whose order puts NaN above every number, so it is NaN where a value is: Polars'
// nan_max. Min skips a NaN as Polars' min does; Max does not, as feature-gaps.md §9
// records.
func (e Expr) NanMin() Expr { return e.agg(expr.AggNanMin) }
func (e Expr) NanMax() Expr { return e.Max() }

// HasNulls is whether a value is null, and IsEmpty whether there are no rows, nulls
// counted: Polars' has_nulls and is_empty, a Boolean each.
func (e Expr) HasNulls() Expr { return e.NullCount().Gt(0) }
func (e Expr) IsEmpty() Expr  { return e.Len().Eq(0) }

// ApproxNUnique is the distinct values, a null among them, as Polars'
// approx_n_unique counts it. It is exact: Polars estimates, and an exact count is an
// estimate's best case (v0.6.6-scope.md §6.3). NUnique does not count a null.
func (e Expr) ApproxNUnique() Expr {
	return e.NUnique().Add(e.NullCount().Gt(0).Cast(Uint64))
}

// Entropy is the entropy of the non-null values over the logarithm of base: Polars'
// entropy. With normalize the values are a distribution, each over their sum, and it
// is −Σ p·ln p; without, −Σ x·ln x. A zero or negative value makes it NaN, as Polars'
// 0·ln 0 is. A group of no value is null, as Sum's is, where Polars' is −0. A
// Float32's is a Float32, any other
// number's a Float64. It merges, as ln S − (Σ x·ln x)/S, so it agrees with Polars'
// to the last few bits.
func (e Expr) Entropy(base float64, normalize bool) Expr {
	return e.aggP(expr.AggEntropy, expr.AggParams{Base: base, Normalize: normalize})
}

// Mode is the most frequent values, each once, a null among them where nulls are:
// Polars' mode, a column of its own length in a Select and a list a group in an
// aggregation. With maintainOrder they come in the order they first appear; without,
// ascending, nulls first, where Polars' order is its hash table's.
func (e Expr) Mode(maintainOrder bool) Expr {
	m := int64(0)
	if maintainOrder {
		m = 1
	}
	return e.reshape(expr.ReshapeMode, m)
}

// Append is the values, then other's: Polars' append, a column of its own length in
// a Select and a list a group in an aggregation. With upcast the two meet at their
// common type, as Polars' default does; without, they must be of one type. Polars
// meets text with anything as text; a common type here is a numeric one.
func (e Expr) Append(other Expr, upcast bool) Expr {
	mode := expr.AppendStrict
	if upcast {
		mode = expr.AppendUpcast
	}
	return wrap(&expr.Reshape{Op: expr.ReshapeAppend, Child: e.node(), By: other.node(), Args: []int64{mode}})
}

// ExtendConstant is the values, then value n times: Polars' extend_constant. value is
// a Go value or nil, cast to the column's type, a float truncated toward zero to an
// integer and one out of its range null, as Polars casts it; n is an int or an Expr.
func (e Expr) ExtendConstant[N int | Expr](value any, n N) Expr {
	var v expr.Node = &expr.Lit{DT: dtype.Null}
	if value != nil {
		v = litNode(value)
	}
	var k expr.Node
	if x, ok := any(n).(Expr); ok {
		k = x.node()
	} else {
		k = litNode(int64(any(n).(int)))
	}
	repeat := explodeOf(horizontal(expr.FnRepeatBy, v, k))
	return wrap(&expr.Reshape{Op: expr.ReshapeAppend, Child: e.node(), By: repeat.node(), Args: []int64{expr.AppendCast}})
}

// SampleN is n of the values drawn, and SampleFraction a fraction of them, as Polars'
// sample: with replacement or without, a sample of more than there are refused then;
// in their order, or shuffled. Each draw is seeded, by the seed and the values, so a
// group's sample does not depend on how the rows came in batches. The generator is
// ursus's, so the values are not Polars' (v0.6.6-scope.md §6.3). A column of its own
// length in a Select and a list a group in an aggregation.
func (e Expr) SampleN(n int, withReplacement, shuffle bool, seed uint64) Expr {
	if n < 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "sample", "sample takes %d values", n)})
	}
	return e.sample(int64(n), 0, withReplacement, shuffle, seed)
}
func (e Expr) SampleFraction(fraction float64, withReplacement, shuffle bool, seed uint64) Expr {
	if fraction < 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "sample", "sample takes a fraction of %g", fraction)})
	}
	return e.sample(-1, fraction, withReplacement, shuffle, seed)
}

// Shuffle is the values in an order drawn by seed: Polars' shuffle.
func (e Expr) Shuffle(seed uint64) Expr { return e.SampleFraction(1, false, true, seed) }

func (e Expr) sample(n int64, fraction float64, withReplacement, shuffle bool, seed uint64) Expr {
	b := func(x bool) int64 {
		if x {
			return 1
		}
		return 0
	}
	return wrap(&expr.Reshape{Op: expr.ReshapeSample, Child: e.node(),
		Args: []int64{n, int64(math.Float64bits(fraction)), b(withReplacement), b(shuffle), int64(seed)}})
}

// HistOption configures Hist.
type HistOption func(*histOptions)

type histOptions struct {
	bins                 []float64
	hasBins, hasCount    bool
	count                int
	category, breakpoint bool
}

// HistBins counts in the bins between edges, which must increase: Polars' bins. The
// first bin holds its left edge too, and a value outside the edges is in none; fewer
// than two edges make no bin.
func HistBins(edges ...float64) HistOption {
	return func(o *histOptions) { o.bins, o.hasBins = edges, true }
}

// HistBinCount counts in n bins of equal width, from the least value to the
// greatest: Polars' bin_count, 10 by default.
func HistBinCount(n int) HistOption {
	return func(o *histOptions) { o.count, o.hasCount = n, true }
}

// HistBreakpoint and HistCategory make each bin a Struct of its right edge, a
// Float64, and its interval as text, "[1.0, 3.333333]" then "(3.333333, 5.666667]",
// beside its count: Polars' include_breakpoint and include_category. Polars' category
// is a Categorical; here it is text, as Cut's labels are.
func HistBreakpoint() HistOption { return func(o *histOptions) { o.breakpoint = true } }
func HistCategory() HistOption   { return func(o *histOptions) { o.category = true } }

// Hist counts the values in bins: Polars' hist (step 286), each bin's count a UInt32,
// a column of its own length in a Select and a list a group in an aggregation. By
// default the bins are 10 of equal width over the values' least to greatest, NaN
// aside, the last edge the greatest itself; one value, or values all alike, are the
// bins over the value ± 0.5, and none the bins over [0, 1]. A null or NaN is in no
// bin. It is Polars' algorithm, edges and counts alike. It counts numbers.
func (e Expr) Hist(opts ...HistOption) Expr {
	var o histOptions
	for _, f := range opts {
		f(&o)
	}
	bad := func(format string, a ...any) Expr {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "hist", format, a...)})
	}
	count := int64(10)
	switch {
	case o.hasBins && o.hasCount:
		// Polars: "can only provide one of `bin_count` or `bins`".
		return bad("hist takes bins or a bin count, not both")
	case o.hasBins:
		count = -1
	case o.hasCount && o.count < 0:
		return bad("hist takes %d bins", o.count)
	case o.hasCount:
		count = int64(o.count)
	}
	args := []int64{count, b2i(o.category), b2i(o.breakpoint)}
	for i, x := range o.bins {
		if i > 0 && !(x > o.bins[i-1]) {
			return bad("the bins' edges must increase, and %v follows %v", x, o.bins[i-1])
		}
		args = append(args, int64(math.Float64bits(x)))
	}
	return wrap(&expr.Reshape{Op: expr.ReshapeHist, Child: e.node(), Args: args})
}

func b2i(x bool) int64 {
	if x {
		return 1
	}
	return 0
}

// UniqueCounts is each distinct value's count, in the order the values first appear,
// a null among them, a UInt32: Polars' unique_counts, a column of its own length in a
// Select and a list a group in an aggregation.
func (e Expr) UniqueCounts() Expr { return e.reshape(expr.ReshapeUniqueCounts) }

// ValueCounts is each distinct value beside its count, a Struct of the value, named
// as the column, and the count, a UInt32: Polars' value_counts, a column of its own
// length in a Select and a list a group in an aggregation. With sort the most
// frequent come first; without, the values come in the order they first appear,
// where Polars' order is its hash table's. With normalize the count is its share of
// the rows, nulls among them, a Float64. The count is named name if given, else
// "count", or "proportion" with normalize, as Polars names it.
func (e Expr) ValueCounts(sort, normalize bool, name ...string) Expr {
	label := "count"
	if normalize {
		label = "proportion"
	}
	switch len(name) {
	case 0:
	case 1:
		label = name[0]
	default:
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "value_counts", "value_counts takes at most one name")})
	}
	var s, n int64
	if sort {
		s = 1
	}
	if normalize {
		n = 1
	}
	return wrap(&expr.Reshape{Op: expr.ReshapeValueCounts, Child: e.node(), Args: []int64{s, n}, Label: label})
}

// Dot is the sum of the products of e and other over the rows where both are
// non-null: Polars' dot. It is Sum's, so an integer dot product is an Int128, and one
// of no such row null, where Polars' is 0, as feature-gaps.md §9 records of Sum.
func (e Expr) Dot(other Expr) Expr { return e.Mul(other).Sum() }

// Quantile returns the value at rank q, where q runs from 0 (the minimum) to 1 (the
// maximum). interp decides what happens when the rank falls between two values.
//
// The rank is q·(n−1) over the non-null values — the convention numpy, Polars and
// DuckDB share — and the result is Float64 even for InterpLower and InterpHigher,
// which do return an actual input value.
//
// Values are ordered by ursus's TOTAL order, so NaN sorts above everything and a
// high quantile of a column containing NaN is NaN, exactly as Sort would place it.
//
// A temporal column, a Duration included, is refused for now; Cast(Int64) gives
// its ticks.
func (e Expr) Quantile(q float64, interp Interpolation) Expr {
	if !(q >= 0 && q <= 1) { // written this way so NaN is rejected too
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "quantile",
			"q must be between 0 and 1, got %v", q)})
	}
	return e.aggP(expr.AggQuantile, expr.AggParams{Q: q, Interp: interp})
}

// Implode collects every value of the group into a List, in input order.
//
//	GroupBy(Col("k")).Agg(
//	    Col("v").Implode().Alias("all"),
//	    Col("v").Implode().List().Sort().List().Head(3).Alias("smallest3"),
//	)
//
// It is what makes the .list namespace available inside a group-by: everything
// list.go can do to a list, this can now do to a group.
//
// # Nulls are kept
//
// Every other aggregate skips them — the sum of an all-null group is null, not
// zero. Implode is a selection rather than a reduction, so a null is a value that
// takes a slot, and an all-null group implodes to a list OF nulls rather than to an
// empty list or a null one.
//
// # It makes the group-by single-threaded
//
// A list's element order is its data, and the parallel driver dispatches batches
// round-robin, so a shuffled input would be a different answer rather than the same
// one computed differently. The whole Agg list runs on one worker as a result —
// including the aggregates beside this one, because a query's aggregates share one
// hash table.
//
// # Its memory is the data
//
// Implode holds every value of every group, so a group-by containing one is O(rows)
// rather than O(distinct keys), and under WithMemoryLimit it refuses once a single
// key grows past the budget. Partitioning divides the key space and cannot divide
// one key.
func (e Expr) Implode() Expr { return e.agg(expr.AggImplode) }

// TopK is the k largest values of the group, largest first, as a List.
//
//	GroupBy(Col("id6")).Agg(Col("v3").TopK(2).Alias("largest2")).Explode("largest2")
//
// It holds only k values a group, so unlike Implode it does not grow with the data,
// and it keeps a parallel group-by parallel. Values are in ursus's total order — NaN
// is the largest float, as Sort places it. Nulls are skipped: a group with fewer
// than k values gives a shorter list, and one with none gives null. Defined for
// numeric, temporal, decimal and string columns; k must be at least 1.
func (e Expr) TopK(k int) Expr { return e.aggP(expr.AggTopK, expr.AggParams{K: k}) }

// BottomK is the k smallest values of the group, smallest first, as a List. See
// TopK, whose rules it follows.
func (e Expr) BottomK(k int) Expr { return e.aggP(expr.AggBottomK, expr.AggParams{K: k}) }

func (e Expr) agg(op expr.AggOp) Expr {
	return wrap(&expr.Agg{Op: op, Child: e.node()})
}

func (e Expr) aggP(op expr.AggOp, p expr.AggParams) Expr {
	return wrap(&expr.Agg{Op: op, Child: e.node(), Params: p})
}

// Len counts rows in the current group — SQL's COUNT(*) with no column argument.
//
// It aggregates over a literal rather than a column, because counting rows must
// not depend on any column existing: `GroupBy(k).Agg(Len())` has to work on a frame
// whose only column is the key. It is named "len", as Polars' pl.len() is; the
// literal's name, "literal", showed through in some plans until step 273.
func Len() Expr { return Lit(int64(1)).Len().Alias("len") }

// --- sort keys ----------------------------------------------------------------

// SortKey is one component of an ordering. Build one with Asc or Desc.
type SortKey struct{ k plan.SortKey }

// Asc orders ascending. Nulls come FIRST by default; call NullsLast to move them.
func Asc(e Expr) SortKey {
	return SortKey{plan.SortKey{Expr: e.node()}}
}

// Desc orders descending, with nulls still FIRST by default.
//
// Null placement is deliberately independent of direction. SQL ties them together
// — nulls sort as the largest value, so they land last ascending and first
// descending — which means switching to Desc silently relocates them. Keeping the
// two orthogonal is more predictable.
func Desc(e Expr) SortKey {
	return SortKey{plan.SortKey{Expr: e.node(), Descending: true}}
}

// NullsFirst places nulls at the start of the output.
func (k SortKey) NullsFirst() SortKey {
	k.k.NullsLast = false
	return k
}

// NullsLast places nulls at the END of the output, whichever direction the key
// sorts in.
func (k SortKey) NullsLast() SortKey {
	k.k.NullsLast = true
	return k
}

// --- frame operations ---------------------------------------------------------

// WithColumns adds columns, replacing any that already exist BY NAME and keeping
// their original position.
//
// Later expressions can reference columns that earlier ones added, so
// `WithColumns(a.Alias("x"), Col("x").Mul(2))` works.
//
// An aggregate is the whole frame's, broadcast to every row, as in Polars:
// WithColumns(Col("x").Sub(Col("x").Mean())) centres x (step 250).
func (lf *LazyFrame) WithColumns(exprs ...Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(exprs) == 0 {
		return lf
	}
	ns, err := nodes(exprs)
	if err != nil {
		return &LazyFrame{err: err}
	}
	if ns, err = lf.writeCategories(ns); err != nil {
		return &LazyFrame{err: err}
	}
	ns = lowerReshapes(ns, false)
	for _, n := range ns {
		if changesLength(n) {
			return lf.withColumnsReshaped(ns)
		}
	}
	return lf.derive(&plan.WithColumns{Input: lf.node, Exprs: bindScalars(ns)})
}

// Sort orders rows by one or more keys.
//
//	lf.Sort(ursus.Desc(ursus.Col("revenue")), ursus.Asc(ursus.Col("name")))
//
// The sort is STABLE, so ties keep their input order and a second Sort refines the
// first rather than discarding it.
func errNilSortKey(i int) error {
	return uerr.New(uerr.KindValue, "sort", "sort key %d is the zero SortKey", i).
		Hint("build keys with ursus.Asc(expr) or ursus.Desc(expr)")
}

func (lf *LazyFrame) Sort(keys ...SortKey) *LazyFrame {
	if lf.err != nil || len(keys) == 0 {
		return lf
	}
	ks := make([]plan.SortKey, len(keys))
	for i, k := range keys {
		if k.k.Expr == nil {
			return &LazyFrame{err: errNilSortKey(i)}
		}
		ks[i] = k.k
	}
	return lf.derive(&plan.Sort{Input: lf.node, Keys: ks})
}

// Unique removes duplicate rows.
//
// With no arguments it compares whole rows. With column names it compares only
// those, keeping the FIRST row for each distinct combination — so the other
// columns come from that row rather than being chosen arbitrarily.
func (lf *LazyFrame) Unique(subset ...string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	var sub []string
	if len(subset) > 0 {
		sub = append([]string(nil), subset...)
	}
	return lf.derive(&plan.Distinct{Input: lf.node, Subset: sub})
}

// GroupBy begins an aggregation.
//
//	lf.GroupBy(ursus.Col("region")).
//	    Agg(ursus.Col("revenue").Sum(), ursus.Len().Alias("n"))
//
// With no keys it is a GLOBAL aggregate producing exactly one row — including over
// an empty input, where `count(*)` is 0 rather than no rows at all.
func (lf *LazyFrame) GroupBy(keys ...Expr) *GroupBy {
	if lf.err != nil {
		return &GroupBy{lf: lf}
	}
	ns, err := nodes(keys)
	if err != nil {
		return &GroupBy{lf: &LazyFrame{err: err}}
	}
	return &GroupBy{lf: lf, keys: ns}
}

// GroupBy is the intermediate handle between GroupBy and Agg.
type GroupBy struct {
	lf      *LazyFrame
	keys    []expr.Node
	ordered bool

	// temporal is set by GroupByDynamic and Rolling. When it is, Agg builds that
	// node instead of an Aggregate — which is what lets all thirteen shorthands
	// below work on a temporal group with no second implementation of any of them.
	temporal    *plan.TemporalGroup
	temporalErr error
}

// MaintainOrder emits groups in first-appearance order.
//
// Without it the group order is unspecified — which is standard (SQL uses set
// semantics and DuckDB exploits it for parallelism) but means a test that assumes
// an order is flaky.
//
// # The flag is no longer free, and that is the point of it
//
// It USED to document a guarantee the implementation happened to give anyway. Since
// step 12 a group-by over more distinct keys than the memory limit holds partitions
// the overflow to disk, and the unordered path then emits partition-major: resident
// groups first, then partition 0's groups, then partition 1's. The order depends on
// where the freeze fell, so it depends on the limit. Since step 150 a group-by of
// tens of thousands of groups or more, on several threads, folds its workers'
// tables partitioned, each partition on its own goroutine, and emits partition-major
// as well, in an order that depends on the thread count.
//
// The CONTENT never does. Every group, every value, identical at every limit and
// thread count. See WithMemoryLimit, which states the same contract from the other
// side. MaintainOrder runs serially.
//
// Costs nothing when the aggregation fits in memory. When it spills it adds an
// int64 ordinal to every routed row and materialises the whole result to permute it
// — plan.Aggregate.MaintainOrder has the detail.
func (g *GroupBy) MaintainOrder() *GroupBy {
	g.ordered = true
	return g
}

// Agg reduces each group.
//
// Every expression must be an aggregation: every column it reads has to pass
// through an aggregate function. `Col("a").Sum().Add(1)` is fine;
// `Col("a").Sum().Add(Col("b"))` is not, because b is still a per-row value and
// there is no defined way to combine the two shapes.
func (g *GroupBy) Agg(exprs ...Expr) *LazyFrame {
	if g.lf.err != nil {
		return g.lf
	}
	if g.temporalErr != nil {
		return &LazyFrame{err: g.temporalErr}
	}
	ns, err := nodes(exprs)
	if err != nil {
		return &LazyFrame{err: err}
	}
	if ns, err = g.lf.writeCategories(ns); err != nil {
		return &LazyFrame{err: err}
	}
	ns = lowerReshapes(ns, true)
	if g.temporal != nil {
		t := *g.temporal
		t.Input, t.Aggs = g.lf.node, ns
		return g.lf.derive(&t)
	}
	return g.lf.derive(&plan.Aggregate{
		Input:         g.lf.node,
		Keys:          g.keys,
		Aggs:          ns,
		MaintainOrder: g.ordered,
	})
}

// Count is shorthand for Agg(Len().Alias("count")).
func (g *GroupBy) Count() *LazyFrame {
	return g.Agg(Len().Alias("count"))
}

// --- GroupBy shorthands ---------------------------------------------------------
//
// Each applies one aggregate to EVERY non-key column, which is what makes them
// worth having: `GroupBy(k).Sum()` over a forty-column frame is one call rather
// than forty.
//
// They are sugar over Agg, and deliberately so. The selector does the work —
// `All().Exclude(keys...)` expands at plan time against the real schema — so the
// shorthands inherit expansion, type checking and projection pushdown without any
// of them being reimplemented. A column the aggregate is not defined for is
// refused by name, the same as if it had been written out.

// keyNames returns the names the group keys read, which are the columns a
// shorthand must NOT aggregate.
//
// A computed key such as `GroupBy(Col("t").Dt().Year())` reads `t`, so `t` is
// excluded — aggregating a column the grouping already consumed would produce a
// column whose meaning depends on how the key was written.
func (g *GroupBy) keyNames() []string {
	var out []string
	seen := map[string]struct{}{}
	keys := g.keys
	if g.temporal != nil {
		// The INDEX is a key too for the purpose of the shorthands: aggregating the
		// column the windows were cut from would produce a column whose meaning
		// depends on how the grouping was written, which is the argument the
		// computed-key note below already makes.
		keys = append([]expr.Node{g.temporal.Index}, keys...)
	}
	for _, k := range keys {
		for _, n := range expr.RootNames(k) {
			if _, dup := seen[n]; !dup {
				seen[n] = struct{}{}
				out = append(out, n)
			}
		}
	}
	return out
}

// numericTypes is every type arithmetic is defined on. It exists because the
// selector API takes explicit types and there is no "numeric" family matcher.
var numericTypes = []DataType{
	dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64, dtype.Int128,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64,
	dtype.Float32, dtype.Float64,
}

// each applies fn to the non-key columns, restricted to numeric ones when the
// aggregate needs them.
//
// # Why the numeric ones restrict rather than refuse
//
// `GroupBy(k).Sum()` on a frame with one string column would otherwise be an error,
// and almost every real frame has one — which would make the shorthand useless
// exactly where it is most wanted. Selecting the numeric columns is not a silent
// skip either: the selector is part of the expression, so Explain shows precisely
// which columns were summed.
//
// Min, Max, First, Last and NUnique take everything, because they select or count
// rather than compute and are defined for every type.
func (g *GroupBy) each(numericOnly bool, fn func(Expr) Expr) *LazyFrame {
	if g.lf.err != nil {
		return g.lf
	}
	var inner expr.Matcher = &expr.AllMatcher{}
	if numericOnly {
		inner = &expr.DTypeMatcher{Types: numericTypes}
	}
	sel := wrap(&expr.Match{M: &expr.ExcludeMatcher{Inner: inner, Names: g.keyNames()}})
	return g.Agg(fn(sel))
}

// Sum, Mean, Min, Max, Median, NUnique, First and Last apply that aggregate to
// every non-key column.
// Sum, Mean, Median and Quantile apply to the NUMERIC non-key columns.
func (g *GroupBy) Sum() *LazyFrame    { return g.each(true, Expr.Sum) }
func (g *GroupBy) Mean() *LazyFrame   { return g.each(true, Expr.Mean) }
func (g *GroupBy) Median() *LazyFrame { return g.each(true, Expr.Median) }

// Min, Max, First, Last and NUnique apply to EVERY non-key column, because they
// select or count rather than compute.
func (g *GroupBy) Min() *LazyFrame     { return g.each(false, Expr.Min) }
func (g *GroupBy) Max() *LazyFrame     { return g.each(false, Expr.Max) }
func (g *GroupBy) NUnique() *LazyFrame { return g.each(false, Expr.NUnique) }
func (g *GroupBy) First() *LazyFrame   { return g.each(false, Expr.First) }
func (g *GroupBy) Last() *LazyFrame    { return g.each(false, Expr.Last) }

// Quantile applies one quantile to every numeric non-key column.
func (g *GroupBy) Quantile(q float64, interp Interpolation) *LazyFrame {
	return g.each(true, func(e Expr) Expr { return e.Quantile(q, interp) })
}

// Len counts the rows in each group under the given name.
//
// Distinct from Count, which uses the fixed name "count": Len takes one because a
// frame that already has a column called "count" is not unusual, and silently
// colliding would be worse than asking.
func (g *GroupBy) Len(name string) *LazyFrame {
	return g.Agg(Len().Alias(name))
}
