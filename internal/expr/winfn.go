package expr

import (
	"strconv"

	"github.com/advenn/ursus/dtype"
)

// --- ordered window functions ---------------------------------------------------

// WinFnOp is a function that is only meaningful within an ordered window.
type WinFnOp uint8

const (
	WinRank WinFnOp = iota
	WinCumCount
	WinCumSum
	WinCumProd
	WinCumMin
	WinCumMax
	WinShift

	// Forward and backward fill. TWO ops rather than one plus a Reverse flag, on
	// purpose: WinFn.String() renders the op name, so the direction can never
	// collide even if the parameter rendering is imperfect. With one op the
	// direction would live ONLY in WinParams.args, which is exactly the field that
	// has been the source of every collision bug so far.
	WinForwardFill
	WinBackwardFill

	// The rolling functions (step 149): each row's answer over the Params.N rows of
	// its partition up to and including it, in the window's order, or null where
	// fewer than Params.MinSamples of them hold a value.
	WinRollingSum
	WinRollingMean
	WinRollingMin
	WinRollingMax
	WinRollingVar
	WinRollingStd
	// A median and a quantile of the window's values (step 227): Params.Q and
	// Params.Interp, a median's 0.5 and linear.
	WinRollingMedian
	WinRollingQuantile

	// The exponentially weighted functions (step 153), Polars' and pandas' ewm_*:
	// each row's answer over every row of its partition up to it, weighted by
	// Params.Alpha's decay.
	WinEwmMean
	WinEwmStd
	WinEwmVar

	// WinInterpolate fills each run of nulls between two values on the straight line
	// between them, by position in the window's order.
	WinInterpolate

	// WinRleID numbers each row's run of equal values, from 0, by the group-by's keys
	// (step 281): Polars' rle_id.
	WinRleID

	// More rolling functions (step 284), appended so no op moves: the skew and
	// kurtosis of each row's window, Params.Bias and Params.Fisher as Polars' bias and
	// fisher; each row's rank among its window's values, by Params.Method; and the
	// covariance and correlation of a pair, the child PairOf(a, b), Params.Ddof the
	// covariance's. IsRolling takes both blocks.
	WinRollingSkew
	WinRollingKurtosis
	WinRollingRank
	WinRollingCov
	WinRollingCorr

	// WinEwmSum is Polars' ewm_sum (step 285): each value plus the running sum
	// decayed by Params.Alpha, appended so no op moves; IsEwm takes it. With
	// Params.By it and WinEwmMean are ewm_sum_by and ewm_mean_by, their child
	// PairOf(value, by), decayed by Params.Period, a half-life, in by's order.
	WinEwmSum

	// WinCumulativeEval is Polars' cumulative_eval (step 286): Params.Body, lowered by
	// LowerCumulativeEval, over each row's prefix of its partition. The physical
	// window evaluates it, as no kernel can evaluate an expression.
	WinCumulativeEval

	// WinRollingMap is Polars' rolling_map (step 338): Params.Map's Go function over
	// each row's window of Params.N rows, as the rolling functions' are, appended so
	// no op moves; IsRolling takes it.
	WinRollingMap

	// WinCatCodes is each row's value's code (step 351): the order, from 0, its
	// value first appears in among the window's non-null values, by the group-by's
	// keys; a null's null. Over the frame it is a Categorical's codes, as Polars
	// numbers them in a fresh process, which the root writes ToPhysical out as.
	WinCatCodes

	winFnCount
)

var winFnNames = [winFnCount]string{
	WinRank: "rank", WinCumCount: "cum_count", WinCumSum: "cum_sum",
	WinCumProd: "cum_prod", WinCumMin: "cum_min", WinCumMax: "cum_max",
	WinShift:       "shift",
	WinForwardFill: "forward_fill", WinBackwardFill: "backward_fill",
	WinRollingSum: "rolling_sum", WinRollingMean: "rolling_mean",
	WinRollingMin: "rolling_min", WinRollingMax: "rolling_max",
	WinRollingVar: "rolling_var", WinRollingStd: "rolling_std",
	WinRollingMedian: "rolling_median", WinRollingQuantile: "rolling_quantile",
	WinEwmMean: "ewm_mean", WinEwmStd: "ewm_std", WinEwmVar: "ewm_var",
	WinInterpolate: "interpolate", WinRleID: "rle_id",
	WinRollingSkew: "rolling_skew", WinRollingKurtosis: "rolling_kurtosis", WinRollingRank: "rolling_rank",
	WinRollingCov: "rolling_cov", WinRollingCorr: "rolling_corr", WinEwmSum: "ewm_sum",
	WinCumulativeEval: "cumulative_eval", WinRollingMap: "rolling_map", WinCatCodes: "cat.physical",
}

func (o WinFnOp) String() string {
	// The `!= ""` half is the load-bearing one. winFnNames is sized by the enum's
	// sentinel, so `int(o) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the four dedup maps. dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(o) < len(winFnNames) && winFnNames[o] != "" {
		return winFnNames[o]
	}
	return "?"
}

// IsCumulative reports whether the op is a running scan over the ordered partition.
func (o WinFnOp) IsCumulative() bool { return o >= WinCumSum && o <= WinCumMax }

// IsRolling reports whether the op is over a fixed number of rows up to each.
func (o WinFnOp) IsRolling() bool {
	return o >= WinRollingSum && o <= WinRollingQuantile || o >= WinRollingSkew && o <= WinRollingCorr ||
		o == WinRollingMap
}

// IsPaired reports whether the op is of two series, its child PairOf(a, b): rolling_cov
// and rolling_corr (step 284).
func (o WinFnOp) IsPaired() bool { return o == WinRollingCov || o == WinRollingCorr }

// IsEwm reports whether the op is exponentially weighted.
func (o WinFnOp) IsEwm() bool { return o >= WinEwmMean && o <= WinEwmVar || o == WinEwmSum }

// RankMethod decides how Rank breaks ties.
type RankMethod uint8

const (
	// RankOrdinal gives every row a distinct rank, ties broken by input order.
	RankOrdinal RankMethod = iota
	// RankDense gives tied rows the same rank and does not skip the next value.
	RankDense
	// RankMin and RankMax give tied rows the lowest or highest of their positions.
	RankMin
	RankMax
	// RankAverage gives tied rows the mean of their positions, so it returns a
	// float where every other method returns an integer.
	RankAverage

	rankMethodCount
)

var rankNames = [rankMethodCount]string{
	RankOrdinal: "ordinal", RankDense: "dense", RankMin: "min",
	RankMax: "max", RankAverage: "average",
}

func (m RankMethod) String() string {
	// The `!= ""` half is the load-bearing one. rankNames is sized by the enum's
	// sentinel, so `int(m) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the four dedup maps. dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(m) < len(rankNames) && rankNames[m] != "" {
		return rankNames[m]
	}
	return "?"
}

// Valid reports whether m is one of the declared methods. RankMethod is a public
// integer type, so RankMethod(99) compiles; ResolveWinFn refuses it, and the kernel
// never sees one.
func (m RankMethod) Valid() bool { return m < rankMethodCount }

// WinParams carries an ordered function's configuration.
//
// A typed struct rather than child nodes, for the reason AggParams gives: these are
// configuration, not operands. There is no meaning to a per-row rank method.
type WinParams struct {
	Method     RankMethod
	Descending bool
	Reverse    bool
	N          int64

	// MinSamples is a rolling function's least number of values in a window for it
	// to answer; zero means N, as Polars' min_samples=None does. Ddof is rolling
	// var's and std's delta degrees of freedom.
	MinSamples int64
	Ddof       int64

	// Q and Interp are rolling_quantile's quantile and interpolation (step 227).
	Q      float64
	Interp Interpolation

	// Center labels a rolling window at its middle row rather than its last, as
	// Polars' center=True; Nearest is Interpolate's method="nearest" (step 172).
	Center  bool
	Nearest bool

	// The ewm functions' (step 153): Alpha is the decay; NoAdjust is Polars'
	// adjust=False, so the zero value is its default; IgnoreNulls weighs by
	// position among the values rather than among the rows; Bias is ewm_var's and
	// ewm_std's bias=True.
	Alpha       float64
	NoAdjust    bool
	IgnoreNulls bool
	Bias        bool

	// Fisher is rolling_kurtosis's fisher, the excess kurtosis; Bias is also the
	// rolling skew's and kurtosis's bias (step 284).
	Fisher bool

	// Body is cumulative_eval's, lowered over CumulativeName (step 286).
	Body Node

	// Map is rolling_map's function (step 338), WinRollingMap's.
	Map *RollingMapFn

	// A rolling function by time (step 177), Polars' rolling_*_by: each row's window
	// is the rows whose by value lies within Period of its own, closed as Closed says,
	// ClosedRight by default. The function's child is then PairOf(value, by), and N is
	// unused.
	By     bool
	Period dtype.Interval
	Closed Closed
}

func (p WinParams) args(op WinFnOp) string {
	switch op {
	case WinRank:
		s := p.Method.String()
		if p.Descending {
			s += ", desc"
		}
		return s
	case WinShift, WinForwardFill, WinBackwardFill:
		// WITHOUT THIS ARM the fills fall to the default below and render as
		// "forward_fill()" whatever their limit — so ForwardFill(1) and
		// ForwardFill(3) would be one string, collide in the four dedup maps,
		// and one would silently take the other's answer. That is step 7's
		// quantile bug and step 8's partition-key bug, third occurrence.
		return strconv.FormatInt(p.N, 10)
	case WinRollingVar, WinRollingStd:
		// Every parameter the answer depends on, for the reason the arm above gives.
		return window(p) + ", min_samples " + strconv.FormatInt(p.MinSamples, 10) +
			", ddof " + strconv.FormatInt(p.Ddof, 10) + centered(p)
	case WinRollingSum, WinRollingMean, WinRollingMin, WinRollingMax, WinRollingMedian:
		return window(p) + ", min_samples " + strconv.FormatInt(p.MinSamples, 10) + centered(p)
	case WinRollingQuantile:
		return strconv.FormatFloat(p.Q, 'g', -1, 64) + ", " + p.Interp.String() + ", " +
			window(p) + ", min_samples " + strconv.FormatInt(p.MinSamples, 10) + centered(p)
	case WinRollingSkew, WinRollingKurtosis:
		s := window(p) + ", min_samples " + strconv.FormatInt(p.MinSamples, 10) +
			", bias " + strconv.FormatBool(p.Bias)
		if op == WinRollingKurtosis {
			s += ", fisher " + strconv.FormatBool(p.Fisher)
		}
		return s + centered(p)
	case WinRollingRank:
		return p.Method.String() + ", " + window(p) + ", min_samples " +
			strconv.FormatInt(p.MinSamples, 10) + centered(p)
	case WinCumulativeEval:
		return p.Body.String() + ", min_samples " + strconv.FormatInt(p.MinSamples, 10)
	case WinRollingMap:
		// The name stands for the function, as a UDF's does; CheckUDFNames keeps
		// two of one name from being two functions.
		name := ""
		if p.Map != nil {
			name = p.Map.Name
		}
		return strconv.Quote(name) + ", " + window(p) + ", min_samples " +
			strconv.FormatInt(p.MinSamples, 10) + centered(p)
	case WinRollingCov, WinRollingCorr:
		s := window(p) + ", min_samples " + strconv.FormatInt(p.MinSamples, 10)
		if op == WinRollingCov {
			s += ", ddof " + strconv.FormatInt(p.Ddof, 10)
		}
		return s + centered(p)
	case WinInterpolate:
		switch {
		case p.Nearest:
			return "nearest"
		case p.By:
			return "by"
		}
		return ""
	case WinEwmMean, WinEwmSum, WinEwmStd, WinEwmVar:
		if p.By {
			return "half_life " + p.Period.String()
		}
		// Every parameter, bias included where it does not apply, so two calls that
		// differ in any one cannot share a temporary.
		return "alpha " + strconv.FormatFloat(p.Alpha, 'g', -1, 64) +
			", adjust " + strconv.FormatBool(!p.NoAdjust) +
			", min_samples " + strconv.FormatInt(p.MinSamples, 10) +
			", ignore_nulls " + strconv.FormatBool(p.IgnoreNulls) +
			", bias " + strconv.FormatBool(p.Bias)
	default:
		if p.Reverse {
			return "reverse"
		}
		return ""
	}
}

// window renders a rolling function's window: its rows, or by time its period and
// how it is closed.
func window(p WinParams) string {
	if p.By {
		return p.Period.String() + ", closed " + p.Closed.Or(ClosedRight).String()
	}
	return strconv.FormatInt(p.N, 10)
}

// centered renders a rolling window's center, which its answer depends on.
func centered(p WinParams) string {
	if p.Center {
		return ", center"
	}
	return ""
}

// WinFn is an ordered window function — rank, a running total, a shift.
//
// # Why not a Call
//
// Call{Fn, Args} already carries a function plus literal arguments, and this is the
// same shape. But Call is contractually ELEMENTWISE: evalCall runs inside a BatchOp,
// which sees one batch and may run on any worker. These functions need the whole
// partition in order, so they are pipeline breakers. Same shape, opposite contract —
// and a separate node type is what makes "only legal inside a Window" checkable.
//
// # Why not an AggOp
//
// They do not reduce. kernel.Accumulator's Finish(name, nGroups) returns one row per
// group; these return one row per INPUT row. They need a different kernel family.
//
// Fill is Shift's optional replacement for the rows shifted in from outside the
// partition. nil means null, which is the usual want.
type WinFn struct {
	Fn     WinFnOp
	Child  Node
	Fill   Node
	Params WinParams
}

func (f *WinFn) node() {}

func (f *WinFn) Children() []Node {
	if f.Fill == nil {
		return []Node{f.Child}
	}
	return []Node{f.Child, f.Fill}
}

func (f *WinFn) String() string {
	// A rolling function by time reads as written, col("v").rolling_sum_by(col("t"),
	// …), not through the struct that carries its two inputs.
	if x, y, ok := pairArgs(f.Child); ok && f.Params.By {
		return x.String() + "." + f.Fn.String() + "_by(" + y.String() + ", " + f.Params.args(f.Fn) + ")"
	}
	if x, y, ok := pairArgs(f.Child); ok && f.Fn.IsPaired() {
		return f.Fn.String() + "(" + x.String() + ", " + y.String() + ", " + f.Params.args(f.Fn) + ")"
	}
	s := f.Child.String() + "." + f.Fn.String() + "(" + f.Params.args(f.Fn) + ")"
	if f.Fill != nil {
		s += ".fill(" + f.Fill.String() + ")"
	}
	return s
}

func (f *WinFn) Field(in *dtype.Schema) (dtype.Field, error) {
	cf, err := f.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	out, err := WinFnType(f, cf.Type, in)
	if err != nil {
		return dtype.Field{}, err
	}

	// cum_count numbers rows, so it is never null, nor is an rle id. Everything else
	// can be: a rank of a null is null (step 335), a shift past the partition edge
	// produces nulls even over a non-null column, and a running total inherits its
	// input's nullability.
	nullable := cf.Nullable
	switch {
	case f.Fn == WinCumCount || f.Fn == WinRleID:
		nullable = false
	case f.Fn == WinShift || f.Fn.IsRolling() || f.Fn.IsEwm() || f.Fn == WinCumulativeEval:
		// A rolling window's first rows hold fewer values than it needs, and so can
		// an ewm's; an ewm variance of one value has none.
		nullable = true
	}
	return dtype.Field{Name: OutputName(f), Type: out, Nullable: nullable}, nil
}

// WinFnType is an ordered window function's output type, its fill included: the
// one authority Field and the physical planner both ask.
//
// A shift's fill used to be ignored here, so the output was always the column's
// type, the fill kept its own, and the kernel's select between the two refused them
// at run time — ShiftFill(1, 1.5) on an Int64 column was ErrInternal. The fill now
// meets the column as a When's branches do, through ResolveCond: a weak literal
// that fits takes the column's type, and otherwise the two promote, so 1.5 on an
// Int64 column is a Float64, which is the type Polars collects too.
func WinFnType(f *WinFn, child dtype.DataType, in *dtype.Schema) (dtype.DataType, error) {
	out, err := ResolveWinFn(f.Fn, f.Params, child)
	if err != nil || f.Fill == nil {
		return out, err
	}
	ff, err := f.Fill.Field(in)
	if err != nil {
		return dtype.Null, err
	}
	return ResolveCond(f.Child, f.Fill, out, ff.Type)
}
