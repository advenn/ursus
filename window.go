package ursus

import (
	"math"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// MappingStrategy decides how a window's per-partition result returns to the frame.
type MappingStrategy = expr.MappingStrategy

const (
	// MapGroupsToRows gives every row its own partition's value, in the original
	// row order. The default, and the only one that composes with other columns.
	MapGroupsToRows = expr.MapGroupsToRows

	// MapExplode would leave the output in partition order instead of permuting it
	// back, Polars' mapping_strategy="explode". Refused while the plan is built: it
	// reorders the frame to partition-major order, which no operator produces yet.
	MapExplode = expr.MapExplode

	// MapJoin would aggregate each partition into a List repeated across its rows.
	// Refused, because it is a synonym: Col(...).Implode().Over(...) is exactly
	// this under the default mapping.
	MapJoin = expr.MapJoin
)

// RankMethod decides how Rank breaks ties.
type RankMethod = expr.RankMethod

const (
	// RankOrdinal gives every row a distinct rank, ties broken by input order.
	RankOrdinal = expr.RankOrdinal
	// RankDense gives tied rows the same rank and does not skip the next value:
	// [10,20,20,30] ranks as 1,2,2,3.
	RankDense = expr.RankDense
	// RankMin and RankMax give tied rows the lowest or highest of their positions:
	// [10,20,20,30] ranks as 1,2,2,4 and 1,3,3,4.
	RankMin = expr.RankMin
	RankMax = expr.RankMax
	// RankAverage gives tied rows the mean of their positions, so it returns
	// Float64 where every other method returns Uint32.
	RankAverage = expr.RankAverage
)

// WindowSpec is the full form of a window, for OverWith.
type WindowSpec struct {
	PartitionBy []Expr
	OrderBy     []SortKey
	Mapping     MappingStrategy
}

// Over computes the expression within partitions and writes the answer back onto
// every row.
//
//	// each row's deviation from its group's mean
//	ursus.Col("x").Sub(ursus.Col("x").Mean().Over(ursus.Col("g")))
//
//	// dense rank of speed within each type
//	ursus.Col("speed").Rank(ursus.RankDense, true).Over(ursus.Col("type"))
//
// # The distinction from GroupBy
//
// GroupBy REDUCES: N rows in, one row per group out. Over PRESERVES: N rows in, N
// rows out, each carrying its own partition's answer. That is why an aggregate is
// refused in Select but the same aggregate under Over is not.
//
// With no partition keys the whole frame is one partition, so
// `Col("x").Sum().Over()` puts the grand total on every row.
//
// # Nulls form their own partition
//
// A null partition key groups with other nulls, the same rule GroupBy uses — as
// opposed to joins, where null keys match nothing. The two differ on purpose and
// each is documented where it applies.
func (e Expr) Over(partitionBy ...Expr) Expr {
	return e.OverWith(WindowSpec{PartitionBy: partitionBy})
}

// OverWith is Over with an ordering and a mapping strategy.
//
// An ordering is what makes Rank, the cumulative functions and Shift meaningful:
// without one they run in input order, which is well-defined but rarely what a
// ranking wants.
func (e Expr) OverWith(spec WindowSpec) Expr {
	part := make([]expr.Node, len(spec.PartitionBy))
	for i, p := range spec.PartitionBy {
		if p.n == nil {
			return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "over",
				"partition key %d is the zero Expr", i)})
		}
		part[i] = p.n
	}
	ord := make([]expr.OrderKey, len(spec.OrderBy))
	for i, k := range spec.OrderBy {
		if k.k.Expr == nil {
			return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "over",
				"order key %d is the zero SortKey", i)})
		}
		ord[i] = k.k
	}
	return wrap(&expr.Window{
		Child: e.node(), PartitionBy: part, OrderBy: ord, Mapping: spec.Mapping,
	})
}

// --- ordered window functions ---------------------------------------------------

// Rank numbers rows by value within the window, smallest first unless descending.
//
// Ties are resolved by method; see RankMethod. Rank is never null — a null value
// still occupies a position — and returns Uint32, or Float64 for RankAverage.
func (e Expr) Rank(method RankMethod, descending bool) Expr {
	return e.winFn(expr.WinRank, expr.WinParams{Method: method, Descending: descending})
}

// CumCount numbers rows within the window, starting at 1.
//
// With reverse it counts from the end, so the last row is 1. That is what makes
// IsLastDistinct expressible as a window rather than a second pass.
func (e Expr) CumCount(reverse bool) Expr {
	return e.winFn(expr.WinCumCount, expr.WinParams{Reverse: reverse})
}

// CumSum, CumProd, CumMin and CumMax are running reductions over the window.
//
// Nulls are SKIPPED by the running value and PRESERVED in place: cum_sum of
// [1, null, 3] is [1, null, 4], not [1, 1, 4] and not [1, null, null]. The running
// total ignores what is not there; the output still says the value was missing.
//
// CumSum uses the same accumulator width as Sum, so the last row of `cum_sum(x)`
// equals `sum(x)` rather than differing by an overflow. CumProd returns Float64 for
// the reason Product does: i128 has addition but no multiplication.
func (e Expr) CumSum(reverse bool) Expr {
	return e.winFn(expr.WinCumSum, expr.WinParams{Reverse: reverse})
}

func (e Expr) CumProd(reverse bool) Expr {
	return e.winFn(expr.WinCumProd, expr.WinParams{Reverse: reverse})
}

func (e Expr) CumMin(reverse bool) Expr {
	return e.winFn(expr.WinCumMin, expr.WinParams{Reverse: reverse})
}

func (e Expr) CumMax(reverse bool) Expr {
	return e.winFn(expr.WinCumMax, expr.WinParams{Reverse: reverse})
}

// CumulativeEval answers each row with body over the values up to and including it,
// in the window's order: Polars' cumulative_eval (step 286). body reads Element(), as
// List().Eval's does, and is one value a prefix, an aggregate such as
// Element().Sum() or Element().First().Sub(Element().Last().Pow(2)); another column
// is refused, as Polars refuses it. A row whose prefix holds fewer than MinSamples
// values, 1 by default, is null.
//
// It is a window: over the frame in its order, or .Over(key) a partition's own. Each
// row evaluates its whole prefix, so its time grows with the square of a partition's
// rows, as Polars' does; the prefixes are built a run of rows at a time. A window
// over the whole frame needs the frame in memory.
func (e Expr) CumulativeEval(body Expr, opts ...WindowOption) Expr {
	b, err := expr.LowerCumulativeEval(body.node())
	if err != nil {
		return wrap(&expr.Err{E: err})
	}
	p := expr.WinParams{Body: b, MinSamples: 1}
	for _, o := range opts {
		o(&p)
	}
	return e.winFn(expr.WinCumulativeEval, p)
}

// Shift moves values n places later within the window, filling the vacated rows
// with null. A negative n shifts earlier — Polars' lag and lead, one function.
//
// Rows shifted in from outside the partition are null, never a neighbouring
// partition's value.
func (e Expr) Shift(n int) Expr {
	return e.winFn(expr.WinShift, expr.WinParams{N: int64(n)})
}

// ShiftFill is Shift with a value instead of null in the vacated rows.
//
// The fill meets the column as FillNullWith's does: a Go value that fits the
// column's type takes it, so ShiftFill(1, 0) on an Int32 column is an Int32, and
// otherwise the two promote, so ShiftFill(1, 1.5) on an Int64 column is a Float64.
func (e Expr) ShiftFill[T Operand](n int, fill T) Expr {
	return wrap(&expr.WinFn{
		Fn: expr.WinShift, Child: e.node(), Fill: liftWeak(fill),
		Params: expr.WinParams{N: int64(n)},
	})
}

func (e Expr) winFn(fn expr.WinFnOp, p expr.WinParams) Expr {
	return wrap(&expr.WinFn{Fn: fn, Child: e.node(), Params: p})
}

// ForwardFill replaces each null with the last non-null value before it.
//
// limit caps how many consecutive nulls one value may fill; 0 means unlimited. A
// leading run of nulls has nothing to carry and stays null.
//
// Like every ordered function it is a window: with no .Over() it runs over the
// whole frame in scan order, which makes it a pipeline breaker.
func (e Expr) ForwardFill(limit int) Expr {
	return e.winFn(expr.WinForwardFill, expr.WinParams{N: int64(limit)})
}

// BackwardFill replaces each null with the next non-null value after it.
//
// It is ForwardFill walked in the other direction, which costs nothing: the
// ordered walk flips its direction rather than building a second permutation.
func (e Expr) BackwardFill(limit int) Expr {
	return e.winFn(expr.WinBackwardFill, expr.WinParams{N: int64(limit)})
}

// WindowOption configures a rolling or an exponentially weighted function. An option
// a function has no use for, Ddof on an EwmMean, does nothing.
type WindowOption func(*expr.WinParams)

// MinSamples lets a rolling window answer from k values, where by default it needs
// as many as it has rows: RollingMean(7, MinSamples(1)) averages what the first six
// rows have. k is between 1 and the window's size.
func MinSamples(k int) WindowOption {
	return func(p *expr.WinParams) { p.MinSamples = int64(k) }
}

// Ddof sets RollingVar's, RollingStd's and RollingCov's delta degrees of freedom; the
// default, 1, is the sample variance, as Var(1) and Polars' rolling_var.
func Ddof(d int) WindowOption {
	return func(p *expr.WinParams) { p.Ddof = int64(d) }
}

// RollingCenter labels a rolling window at its middle row rather than its last, as
// Polars' center=True (step 172): a window of size rows covers size/2 rows before
// the row and (size-1)/2 after it. A window that runs past either end of the rows
// holds fewer values, and is null unless MinSamples allows it.
func RollingCenter() WindowOption {
	return func(p *expr.WinParams) { p.Center = true }
}

// InterpolateNearest makes Interpolate fill a run of nulls with the nearer of the two
// values around it, the later on a tie, as Polars' method="nearest" (step 172). The
// column's own type is kept, a number's or an instant's.
func InterpolateNearest() WindowOption {
	return func(p *expr.WinParams) { p.Nearest = true }
}

// RollingSum, RollingMean, RollingMin, RollingMax, RollingVar and RollingStd answer
// each row over the size rows up to and including it: Polars' rolling_* with a
// window_size, center=False. RollingCenter() labels each window at its middle row
// instead, as center=True does.
//
//	Col("price").RollingMean(7)                           // the week's average, from the 7th row
//	Col("price").RollingMean(7).Over(Col("ticker"))       // per ticker
//	Col("price").RollingMax(3, MinSamples(1)).OverWith(spec)  // in the spec's order
//
// The rows are the window's: a partition's, in its order, with .Over or .OverWith,
// or else the frame's, in scan order. A row whose window holds fewer than
// MinSamples values — by default, size, so any null in a full window — is null.
//
// Each answers what the aggregate of the same name would over that window, with the
// same type: a sum of integers is an Int128, a mean, var and std of numbers floats,
// a min and max the column's own type, of any type with an order. A NaN in a float
// window makes its sum, mean, var and std NaN, and its max NaN, as the aggregates'.
func (e Expr) RollingSum(size int, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingSum, size, 0, opts)
}

func (e Expr) RollingMean(size int, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingMean, size, 0, opts)
}

func (e Expr) RollingMin(size int, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingMin, size, 0, opts)
}

func (e Expr) RollingMax(size int, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingMax, size, 0, opts)
}

func (e Expr) RollingVar(size int, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingVar, size, 1, opts)
}

func (e Expr) RollingStd(size int, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingStd, size, 1, opts)
}

// RollingMedian and RollingQuantile answer each row over the size rows up to and
// including it, as the rolling functions above do: the median, and the q-quantile
// interpolated as interp says, of the window's values (step 227). Polars'
// rolling_median and rolling_quantile; Polars' rolling_quantile defaults to
// InterpNearest. Each answers as Median and Quantile do over the window: NaN is the
// largest value, as in Polars, and the answer is a Float64, where Polars keeps a
// Float32 window a Float32.
func (e Expr) RollingMedian(size int, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingMedian, size, 0, opts)
}

func (e Expr) RollingQuantile(q float64, interp Interpolation, size int, opts ...WindowOption) Expr {
	p := expr.WinParams{N: int64(size), Q: q, Interp: interp}
	for _, o := range opts {
		o(&p)
	}
	return e.winFn(expr.WinRollingQuantile, p)
}

// RollingSkew and RollingKurtosis answer each row with the skew or kurtosis of its
// window's values, as the rolling functions above do: Polars' rolling_skew and
// rolling_kurtosis, bias and fisher as Skew's and Kurtosis's, Polars' defaults both
// true (step 284). They are Polars' algorithm, a queue of mergeable moments, so a
// kurtosis is Polars' to the bit, and a skew is but for about one in a thousand, an
// ulp apart: its m2^1.5 is rounded once, where glibc's pow, Polars', can round the
// other way near a midpoint. A Float32 answers a Float32 and any other number a
// Float64. A window of values all alike is NaN, as is one holding a NaN or an
// infinity; without bias, a skew needs three values and a kurtosis four.
func (e Expr) RollingSkew(size int, bias bool, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingSkew, size, 0,
		append([]WindowOption{func(p *expr.WinParams) { p.Bias = bias }}, opts...))
}

func (e Expr) RollingKurtosis(size int, fisher, bias bool, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingKurtosis, size, 0,
		append([]WindowOption{func(p *expr.WinParams) { p.Fisher, p.Bias = fisher, bias }}, opts...))
}

// RollingRank answers each row with its own value's rank among its window's values:
// Polars' rolling_rank (step 284). RankMin, RankMax and RankDense are a UInt32 and
// RankAverage, Polars' default, a Float64; RankOrdinal is refused, as Polars has
// none, and Polars' random is not here. Values compare as Sort orders them, NaN
// above every number and -0 with 0. It ranks numbers, instants and Booleans; a row
// whose value is null is null.
//
// With RollingCenter a row is ranked in the window centred on it. Polars then
// answers null where the window's last row is null, though the row's own is not;
// here such a row is ranked.
func (e Expr) RollingRank(size int, method RankMethod, opts ...WindowOption) Expr {
	return e.rolling(expr.WinRollingRank, size, 0,
		append([]WindowOption{func(p *expr.WinParams) { p.Method = method }}, opts...))
}

// RollingCov is the covariance of a and b over each row's window of size rows, and
// RollingCorr their Pearson correlation: Polars' rolling_cov and rolling_corr (step
// 284). They are built as Polars builds them, from rolling means and variances,
// pandas' way, over the window's rows where both hold a value, n of them:
//
//	cov  = (mean(a·b) − mean(a)·mean(b)) · n/(n − ddof)
//	corr = the cov at ddof 1 / sqrt(var(a)·var(b))
//
// Ddof sets the covariance's, 1 by default; a correlation does not depend on it, as
// Polars says of its own. Each is a Float32 for two Float32s and a Float64 otherwise.
// A window needs size such rows, or MinSamples, and a correlation two.
func RollingCov(a, b Expr, size int, opts ...WindowOption) Expr {
	return rollingPair(expr.WinRollingCov, a, b, size, opts)
}

func RollingCorr(a, b Expr, size int, opts ...WindowOption) Expr {
	return rollingPair(expr.WinRollingCorr, a, b, size, opts)
}

func rollingPair(fn expr.WinFnOp, a, b Expr, size int, opts []WindowOption) Expr {
	p := expr.WinParams{N: int64(size), Ddof: 1}
	for _, o := range opts {
		o(&p)
	}
	return wrap(&expr.WinFn{Fn: fn, Child: expr.PairOf(a.node(), b.node()), Params: p})
}

func (e Expr) rolling(fn expr.WinFnOp, size int, ddof int64, opts []WindowOption) Expr {
	p := expr.WinParams{N: int64(size), Ddof: ddof}
	for _, o := range opts {
		o(&p)
	}
	return e.winFn(fn, p)
}

// RollingSumBy, RollingMeanBy, RollingMinBy, RollingMaxBy, RollingVarBy and
// RollingStdBy answer each row over the rows whose by value lies within period of
// its own, (t − period, t]: Polars' rolling_*_by, and the expression form of the
// frame-level Rolling.
//
//	Col("amount").RollingSumBy(Col("at"), Every("1h"))               // the hour up to each row
//	Col("amount").RollingMeanBy(Col("at"), Every("7d")).Over(Col("account"))
//
// The window is found by value, not position: by need not be sorted, the rows that
// tie with a row's by are in its window, the later ones too, and a row whose by is
// null is in no window and is null. By default a window answers from one value, as
// Polars' min_samples=1; MinSamples asks for more. WindowClosed chooses which ends of
// the window are in it.
//
// by is a Date, Datetime or Time, or an integer of 32 or 64 bits, whose period counts
// it in Polars' integer unit, Every("3i") (step 312). A calendar period counts months
// and days on the wall clock of by's zone, so Every("1d") across a change of offset
// is a calendar day, and FromDuration(24*time.Hour) twenty-four hours. The answers'
// types are the rolling functions' of the same names.
//
// Where Polars differs: it answers the sum of a window holding no value, one empty
// under closed="left" or "none" or one whose rows are all null, as 0.0, although
// min_samples is 1. Here that window is null.
func (e Expr) RollingSumBy(by Expr, period Interval, opts ...WindowOption) Expr {
	return e.rollingBy(expr.WinRollingSum, by, period, 0, opts)
}

func (e Expr) RollingMeanBy(by Expr, period Interval, opts ...WindowOption) Expr {
	return e.rollingBy(expr.WinRollingMean, by, period, 0, opts)
}

func (e Expr) RollingMinBy(by Expr, period Interval, opts ...WindowOption) Expr {
	return e.rollingBy(expr.WinRollingMin, by, period, 0, opts)
}

func (e Expr) RollingMaxBy(by Expr, period Interval, opts ...WindowOption) Expr {
	return e.rollingBy(expr.WinRollingMax, by, period, 0, opts)
}

func (e Expr) RollingVarBy(by Expr, period Interval, opts ...WindowOption) Expr {
	return e.rollingBy(expr.WinRollingVar, by, period, 1, opts)
}

func (e Expr) RollingStdBy(by Expr, period Interval, opts ...WindowOption) Expr {
	return e.rollingBy(expr.WinRollingStd, by, period, 1, opts)
}

// RollingMedianBy and RollingQuantileBy are RollingMedian and RollingQuantile over
// the windows of RollingSumBy and its kin (step 227): Polars' rolling_median_by and
// rolling_quantile_by.
func (e Expr) RollingMedianBy(by Expr, period Interval, opts ...WindowOption) Expr {
	return e.rollingBy(expr.WinRollingMedian, by, period, 0, opts)
}

func (e Expr) RollingQuantileBy(q float64, interp Interpolation, by Expr, period Interval,
	opts ...WindowOption) Expr {
	return e.rollingBy(expr.WinRollingQuantile, by, period, 0,
		append([]WindowOption{func(p *expr.WinParams) { p.Q, p.Interp = q, interp }}, opts...))
}

func (e Expr) rollingBy(fn expr.WinFnOp, by Expr, period Interval, ddof int64, opts []WindowOption) Expr {
	p := expr.WinParams{By: true, Period: period, MinSamples: 1, Ddof: ddof}
	for _, o := range opts {
		o(&p)
	}
	return wrap(&expr.WinFn{Fn: fn, Child: expr.PairOf(e.node(), by.node()), Params: p})
}

// WindowClosed chooses which ends of a rolling window by time hold their instant:
// ClosedRight, the default, is (t − period, t], as Polars' closed="right";
// ClosedLeft is [t − period, t), ClosedBoth both ends and ClosedNone neither. A
// rolling window of N rows has no use for it.
func WindowClosed(c Closed) WindowOption {
	return func(p *expr.WinParams) { p.Closed = c }
}

// EwmDecay is how fast an exponentially weighted function forgets: the weight a row
// loses with each row after it. Build it from whichever quantity is to hand, as
// Polars' com, span, half_life and alpha are.
type EwmDecay struct {
	alpha float64
	err   error
}

// EwmAlpha is the decay itself: each row after keeps 1-alpha of a row's weight.
// 0 < alpha <= 1.
func EwmAlpha(alpha float64) EwmDecay {
	if !(alpha > 0 && alpha <= 1) {
		return EwmDecay{err: uerr.New(uerr.KindValue, "ewm", "alpha is %v, and must be in (0, 1]", alpha)}
	}
	return EwmDecay{alpha: alpha}
}

// EwmSpan is a decay by an N-row span: alpha = 2/(span+1), for span >= 1.
func EwmSpan(span float64) EwmDecay {
	if !(span >= 1) {
		return EwmDecay{err: uerr.New(uerr.KindValue, "ewm", "span is %v, and must be at least 1", span)}
	}
	return EwmAlpha(2 / (span + 1))
}

// EwmCom is a decay by a centre of mass: alpha = 1/(1+com), for com >= 0.
func EwmCom(com float64) EwmDecay {
	if !(com >= 0) {
		return EwmDecay{err: uerr.New(uerr.KindValue, "ewm", "com is %v, and must not be negative", com)}
	}
	return EwmAlpha(1 / (1 + com))
}

// EwmHalfLife is a decay by the rows over which a weight halves:
// alpha = 1 - exp(-ln 2 / halfLife), for halfLife > 0.
func EwmHalfLife(halfLife float64) EwmDecay {
	if !(halfLife > 0) {
		return EwmDecay{err: uerr.New(uerr.KindValue, "ewm", "half_life is %v, and must be positive", halfLife)}
	}
	return EwmAlpha(1 - math.Exp(-math.Ln2/halfLife))
}

// EwmAdjust chooses how the first rows are weighed; true is the default, as Polars'
// adjust=True. Adjusted, each row's answer divides by the weights its rows had;
// unadjusted, the newest row weighs alpha and the running answer 1-alpha.
func EwmAdjust(adjust bool) WindowOption {
	return func(p *expr.WinParams) { p.NoAdjust = !adjust }
}

// IgnoreNulls weighs an ewm by position among the values, so a null does not age
// the rows before it. By default it does, as a row would: Polars' ignore_nulls=False.
func IgnoreNulls() WindowOption {
	return func(p *expr.WinParams) { p.IgnoreNulls = true }
}

// Biased makes EwmVar and EwmStd the biased estimate, Polars' bias=True. By default
// they are corrected for the weights, and a first value's is null.
func Biased() WindowOption {
	return func(p *expr.WinParams) { p.Bias = true }
}

// EwmMean, EwmStd and EwmVar are exponentially weighted: each row's answer is over
// every row of its window up to it, each weighted by decay's power of how far behind
// it lies — Polars' ewm_mean, ewm_std and ewm_var, whose recurrences are pandas'.
//
//	Col("price").EwmMean(EwmSpan(20))                         // a 20-row moving average
//	Col("price").EwmStd(EwmHalfLife(5)).OverWith(byDayPerTicker)
//
// A null row is no observation, and answers null, as Polars' does, where pandas
// answers the running value; MinSamples sets how many values must have been seen
// before a row answers, one by default. Each is a Float64, or a Float32 for one. A
// NaN is a value, and makes the answer NaN from there on, where pandas would skip
// it.
func (e Expr) EwmMean(decay EwmDecay, opts ...WindowOption) Expr {
	return e.ewm(expr.WinEwmMean, decay, opts)
}

func (e Expr) EwmStd(decay EwmDecay, opts ...WindowOption) Expr {
	return e.ewm(expr.WinEwmStd, decay, opts)
}

func (e Expr) EwmVar(decay EwmDecay, opts ...WindowOption) Expr {
	return e.ewm(expr.WinEwmVar, decay, opts)
}

// EwmSum is each value plus the sum before it decayed by decay, s = x + (1−alpha)·s,
// the first value its own: Polars' ewm_sum (step 285). A null decays the sum, as a row
// ages the weights, but with IgnoreNulls; a null row is null, and a row answers once
// MinSamples values have been seen. EwmAdjust and Biased do nothing. A Float32 is
// summed in float32, as Polars sums it, and any other number answers a Float64.
func (e Expr) EwmSum(decay EwmDecay, opts ...WindowOption) Expr {
	return e.ewm(expr.WinEwmSum, decay, opts)
}

// EwmMeanBy and EwmSumBy are exponentially weighted by time: Polars' ewm_mean_by and
// ewm_sum_by (step 285). In by's order, each row decays the last answer by
// d = 0.5^(Δt/halfLife), Δt the time since the last row holding a value:
//
//	mean: d·last + (1−d)·x
//	sum:  d·last + x
//
// The first such row is its value. by is a Date or a Datetime, or an integer of 32 or
// 64 bits whose half-life is in Polars' integer unit, Every("2i") (step 312); it need
// not be sorted, and the answers go back to the rows' order. A row whose value or by is null is null and
// skipped. halfLife is a constant duration: no months, and no days over a zoned by,
// as Polars requires. A Float32 answers a Float32 and any other number a Float64.
// Rows of one instant are taken in the window's order, where Polars' unstable sort
// takes them as it finds them.
func (e Expr) EwmMeanBy(by Expr, halfLife Interval) Expr {
	return e.ewmBy(expr.WinEwmMean, by, halfLife)
}
func (e Expr) EwmSumBy(by Expr, halfLife Interval) Expr { return e.ewmBy(expr.WinEwmSum, by, halfLife) }

func (e Expr) ewmBy(fn expr.WinFnOp, by Expr, halfLife Interval) Expr {
	return wrap(&expr.WinFn{Fn: fn, Child: expr.PairOf(e.node(), by.node()),
		Params: expr.WinParams{By: true, Period: halfLife}})
}

func (e Expr) ewm(fn expr.WinFnOp, decay EwmDecay, opts []WindowOption) Expr {
	if decay.err != nil {
		return wrap(&expr.Err{E: decay.err})
	}
	if decay.alpha == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "ewm",
			"no decay: pass EwmAlpha, EwmSpan, EwmCom or EwmHalfLife")})
	}
	p := expr.WinParams{Alpha: decay.alpha}
	for _, o := range opts {
		o(&p)
	}
	return e.winFn(fn, p)
}

// Interpolate fills each run of nulls between two values with the points on the
// straight line between them, by position in the window's order: Polars'
// interpolate. [1, null, null, 7] is [1, 3, 5, 7]. A run before the first value or
// after the last stays null. An integer or a Decimal answers a Float64; a float keeps
// its width. An instant, a duration or a time keeps its type, interpolated on its
// ticks in integers as Polars' is: a + step·(b−a)/span, the quotient truncated
// toward zero, so halfway from January 1 to January 4 is January 2 (step 301). InterpolateNearest() fills it with the nearer value instead:
// [1, 1, 7, 7].
func (e Expr) Interpolate(opts ...WindowOption) Expr {
	var p expr.WinParams
	for _, o := range opts {
		o(&p)
	}
	return e.winFn(expr.WinInterpolate, p)
}

// InterpolateBy fills each run of nulls between two values on the straight line
// between them, at each row's by: Polars' interpolate_by (step 329). A null at x
// between (x0, y0) and (x1, y1) is y0 + (y1−y0)/(x1−x0)·(x−x0), Polars' arithmetic to
// the bit. In by's order: it need not be sorted, and the answers go back to the rows'
// order, rows of one by taken in the window's order. A run before the first value or
// after the last stays null, and a null in by is refused, as Polars refuses it. The
// value is a number, a Float32 answering a Float32 and any other a Float64; by is a
// Date, counted in days, a Datetime, an integer of 32 or 64 bits, or a float.
func (e Expr) InterpolateBy(by Expr) Expr {
	return wrap(&expr.WinFn{Fn: expr.WinInterpolate, Child: expr.PairOf(e.node(), by.node()),
		Params: expr.WinParams{By: true}})
}

// Diff is the difference from the value n rows earlier.
//
// # It costs ONE shift, not two
//
// `e.Sub(e.Shift(n))` mentions the shift twice, and window resolution dedups on
// the rendered expression — so both mentions resolve to the same temporary and the
// shift is computed once. That is real common-subexpression elimination, unlike
// Coalesce, which has none.
//
// The type is whatever subtraction gives, which is the operand's own for a signed
// number and a DURATION for an instant — the temporal algebra's instant-minus-instant
// rule. Polars' diff carries a null_behavior parameter this composition cannot
// express; the first n rows are null here.
//
// # An unsigned column is widened first
//
// Subtracting at its own width wrapped: a UInt8 that went from 3 to 1 changed by
// 254. It is taken one signed width up — UInt8 to Int16, UInt16 to Int32, UInt32 to
// Int64, UInt64 to Int128 — which holds every difference exactly, by the same
// plan-time call PctChange uses to reach a float. A signed integer keeps its width
// and wraps, as all integer arithmetic here does, and as Polars' diff does.
func (e Expr) Diff(n int) Expr {
	w := wrap(&expr.Call{Fn: expr.FnMathDiffWiden, Args: []expr.Node{e.node()}})
	return w.Sub(w.Shift(n))
}

// PctChange is the fractional change from the value n rows earlier.
//
// Always a float on a numeric column, because it divides — and a zero previous
// value gives ±Inf rather than a null, since float division is total. A Float32
// stays Float32. On an instant it is a plan-time error.
//
// # The value is taken to a float BEFORE the subtraction
//
// It used to subtract at the column's own width, and integer subtraction wraps: a
// UInt8 that went from 3 to 1 changed by 254, so its pct_change was 84.67 where the
// answer is −0.67. The public Expr has no type in hand to choose a cast with, so an
// internal call, typed at plan time, takes an integer or a Decimal to Float64 and a
// Duration's ticks to Float64, keeps a float as it is, and refuses an instant.
func (e Expr) PctChange(n int) Expr {
	x := wrap(&expr.Call{Fn: expr.FnMathAsFloat, Args: []expr.Node{e.node()}})
	prev := x.Shift(n)
	return x.Sub(prev).Div(prev)
}

// --- the predicates step 7 deferred to this step ---------------------------------

// IsUnique reports whether each row's value occurs exactly once in the column.
// IsDuplicated is its negation for non-null rows.
//
// # These are windows, and that is why they were not built earlier
//
// The answer for row 0 depends on the last row, so neither is elementwise. Step 7
// deferred them precisely here, and they need no new machinery: a value is unique
// iff the partition keyed by that value has exactly one row.
//
// Equality is GROUPING equality, the same the partitioning uses — so NaN equals NaN
// and -0.0 equals +0.0, and IsUnique agrees with Unique() about the same data.
// Nulls likewise group together, so two nulls are duplicates of each other.
func (e Expr) IsUnique() Expr { return e.Len().Over(e).Eq(int64(1)) }

func (e Expr) IsDuplicated() Expr { return e.Len().Over(e).Gt(int64(1)) }

// IsDuplicated and IsUnique are Expr.IsDuplicated and IsUnique over several columns
// at once: whether each row's values of exprs together are another row's too, or
// no other row's (step 290). Polars' pl.struct(...).is_duplicated() and
// DataFrame.is_duplicated(). They are named after their first expression, as Polars
// names a struct after its first field. A key's equality is grouping's, so nulls,
// NaNs and -0 with 0 are each equal, as Unique compares them.
//
// The columns are named: All() here would expand the whole expression once a column,
// as Polars' over(pl.all()) does, each partitioned by one column. Whole rows are
// DataFrame.IsDuplicated and IsUnique.
func IsDuplicated(exprs ...Expr) Expr {
	return rowsLen("is_duplicated", exprs, func(n Expr) Expr { return n.Gt(int64(1)) })
}

func IsUnique(exprs ...Expr) Expr {
	return rowsLen("is_unique", exprs, func(n Expr) Expr { return n.Eq(int64(1)) })
}

// rowsLen is test of how many rows share each row's values of exprs, named after the
// first.
func rowsLen(op string, exprs []Expr, test func(Expr) Expr) Expr {
	if len(exprs) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, op, "%s needs the columns it compares", op).
			Hint("e.g. IsDuplicated(Col(\"a\"), Col(\"b\")); DataFrame.IsDuplicated for whole rows")})
	}
	return test(Len().Over(exprs...)).Alias(expr.OutputName(exprs[0].node()))
}

// IsFirstDistinct and IsLastDistinct mark the first and last occurrence of each
// value, in input order.
//
// Note the asymmetry with IsUnique: a value occurring three times has one first
// occurrence, one last, and is not unique anywhere.
func (e Expr) IsFirstDistinct() Expr {
	return e.CumCount(false).Over(e).Eq(uint32(1))
}

func (e Expr) IsLastDistinct() Expr {
	return e.CumCount(true).Over(e).Eq(uint32(1))
}
