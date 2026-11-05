package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// ResolveWinFn gives an ordered window function's output type.
//
// The single authority, consulted by Field for the schema and by the evaluator for
// the kernel's output type — the Binding lesson, applied a fourth time.
func ResolveWinFn(fn WinFnOp, p WinParams, in dtype.DataType) (dtype.DataType, error) {
	switch fn {
	case WinRank:
		if !in.IsOrdered() {
			return dtype.Null, uerr.New(uerr.KindType, "rank",
				"rank() is not defined for %s", in).
				Hint("only numeric, temporal, string and boolean types have an ordering")
		}
		// It fell through to the kernel's average arm before step 72, with a
		// Uint32 output and no Float64 buffer to write into: an index out of range.
		if !p.Method.Valid() {
			return dtype.Null, uerr.New(uerr.KindValue, "rank",
				"rank method %d is not a RankMethod", uint8(p.Method)).
				Hint("use RankOrdinal, RankDense, RankMin, RankMax or RankAverage")
		}
		// Average is the odd one out: the mean of two adjacent positions is not an
		// integer, and truncating it would make it indistinguishable from min.
		if p.Method == RankAverage {
			return dtype.Float64, nil
		}
		return dtype.Uint32, nil

	case WinCumCount:
		return dtype.Uint32, nil

	case WinRleID:
		// A run is of values a group-by keys alike: NaN with NaN, -0 with 0.
		if !in.IsHashable() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "rle_id", "rle_id compares values, and %s cannot be a key", in)
		}
		return dtype.Uint32, nil

	case WinCatCodes:
		if !in.IsHashable() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "cat.physical", "codes number values, and %s cannot be a key", in)
		}
		return dtype.Uint32, nil

	case WinCumSum:
		// The same binding Sum uses, so `cum_sum(x)` and `sum(x)` agree on the last
		// row rather than differing by an accumulator width. Its refusal names
		// cum_sum: it named sum(), which the caller had not written (audit.md A11).
		b, err := ResolveAggBinding(AggSum, in)
		if err != nil {
			return dtype.Null, cumulativeErr(fn, in, err)
		}
		return b.Out, nil

	case WinCumProd:
		b, err := ResolveAggBinding(AggProduct, in)
		if err != nil {
			return dtype.Null, cumulativeErr(fn, in, err)
		}
		return b.Out, nil

	case WinCumMin, WinCumMax:
		if !in.IsOrdered() {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s() is not defined for %s", fn, in).
				Hint("only numeric, temporal, string and boolean types have an ordering")
		}
		return in, nil

	case WinShift, WinForwardFill, WinBackwardFill:
		return in, nil

	case WinRollingSum, WinRollingMean, WinRollingMin, WinRollingMax, WinRollingVar, WinRollingStd,
		WinRollingMedian, WinRollingQuantile, WinRollingSkew, WinRollingKurtosis, WinRollingRank,
		WinRollingCov, WinRollingCorr:
		if fn == WinRollingQuantile && (!(p.Q >= 0 && p.Q <= 1) || !p.Interp.Valid()) {
			return dtype.Null, uerr.New(uerr.KindValue, fn.String(),
				"%s needs a quantile in [0, 1] and a known interpolation, got %v and %s",
				fn, p.Q, p.Interp).
				Hint("e.g. RollingQuantile(0.9, ursus.InterpNearest, 10)")
		}
		return rollingOut(fn, p, in)

	case WinRollingMap:
		if p.Map == nil {
			return dtype.Null, uerr.Internalf("expr: rolling_map has no function")
		}
		return p.Map.Out, nil
	case WinCumulativeEval:
		// The body's type over a list of the values, as List().Eval types it.
		if p.Body == nil || p.MinSamples < 0 {
			return dtype.Null, uerr.New(uerr.KindValue, fn.String(),
				"cumulative_eval needs a body and min_samples of at least 0, got %d", p.MinSamples)
		}
		s, err := dtype.NewSchema(dtype.Field{Name: CumulativeName, Type: dtype.List(in), Nullable: true})
		if err != nil {
			return dtype.Null, err
		}
		f, err := p.Body.Field(s)
		if err != nil {
			return dtype.Null, err
		}
		return f.Type, nil

	case WinEwmMean, WinEwmSum, WinEwmStd, WinEwmVar, WinInterpolate:
		if p.By && fn == WinInterpolate {
			return interpolateByOut(in)
		}
		if p.By {
			return ewmByOut(fn, p, in)
		}
		if fn == WinInterpolate && p.Nearest {
			// The nearest value is a value of the column, so its type is kept, as
			// Polars' method="nearest" keeps it.
			if in.IsNumeric() || in.IsTemporal() {
				return in, nil
			}
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"interpolate(nearest) is not defined for %s", in)
		}
		if fn != WinInterpolate {
			if !(p.Alpha > 0 && p.Alpha <= 1) {
				return dtype.Null, uerr.New(uerr.KindValue, fn.String(),
					"alpha is %v, and must be in (0, 1]", p.Alpha)
			}
			if p.MinSamples < 0 {
				return dtype.Null, uerr.New(uerr.KindValue, fn.String(),
					"min_samples is %d, and must not be negative", p.MinSamples)
			}
		}
		// Weighted means and lines between values are fractions, so an integer or a
		// Decimal answers a Float64, as Polars' do; a float keeps its width.
		switch {
		case in.IsFloat():
			return in, nil
		case in.IsNumeric() && in.ID() != dtype.TypeInt128:
			return dtype.Float64, nil
		case fn == WinInterpolate && in.IsTemporal():
			// An instant, a duration or a time is interpolated on its ticks, in
			// integers, and keeps its type, as Polars' is (step 301).
			return in, nil
		}
		return dtype.Null, uerr.New(uerr.KindType, fn.String(),
			"%s() is not defined for %s", fn, in).
			Hint("cast it first, e.g. .Cast(ursus.Float64)")

	default:
		return dtype.Null, uerr.Internalf("expr: unknown window function %d", fn)
	}
}

// interpolateByOut types interpolate_by (step 329): a by of a Date, a Datetime, an
// integer of 32 or 64 bits or a float, as Polars takes, and a value that is a number,
// a float keeping its width, as interpolate's does.
func interpolateByOut(in dtype.DataType) (dtype.DataType, error) {
	fs := in.Fields()
	if in.ID() != dtype.TypeStruct || len(fs) != 2 {
		return dtype.Null, uerr.Internalf("expr: interpolate_by over %s", in)
	}
	value, by := fs[0].Type, fs[1].Type
	switch by.ID() {
	case dtype.TypeDate, dtype.TypeDatetime, dtype.TypeInt32, dtype.TypeInt64, dtype.TypeUint32, dtype.TypeUint64,
		dtype.TypeFloat32, dtype.TypeFloat64:
	default:
		return dtype.Null, uerr.New(uerr.KindType, "interpolate_by",
			"interpolate_by's by is a Date, a Datetime, an integer of 32 or 64 bits, or a float, not %s", by)
	}
	switch {
	case value.IsFloat():
		return value, nil
	case value.IsNumeric() && value.ID() != dtype.TypeInt128:
		return dtype.Float64, nil
	}
	return dtype.Null, uerr.New(uerr.KindType, "interpolate_by", "interpolate_by is not defined for %s", value).
		Hint("cast it to a number first, e.g. .Cast(ursus.Float64)")
}

// ewmByOut types ewm_mean_by and ewm_sum_by (step 285): a by of a Date or a
// Datetime, and a half-life that is a constant duration, as Polars requires: no
// months, and no days on a zoned by, whose days are not all 24 hours. The value is
// an ewm's.
func ewmByOut(fn WinFnOp, p WinParams, in dtype.DataType) (dtype.DataType, error) {
	fs := in.Fields()
	if in.ID() != dtype.TypeStruct || len(fs) != 2 || (fn != WinEwmMean && fn != WinEwmSum) {
		return dtype.Null, uerr.Internalf("expr: %s by over %s", fn, in)
	}
	value, by := fs[0].Type, fs[1].Type
	name := fn.String() + "_by"
	switch by.ID() {
	case dtype.TypeDate, dtype.TypeDatetime, dtype.TypeInt32, dtype.TypeInt64, dtype.TypeUint32, dtype.TypeUint64:
	default:
		return dtype.Null, uerr.New(uerr.KindType, name,
			"%s orders its rows by a Date, a Datetime or an integer of 32 or 64 bits, got %s", name, by)
	}
	if err := IndexInterval(name, "half-life", p.Period, by); err != nil {
		return dtype.Null, err
	}
	hl := p.Period
	switch {
	case hl.Err() != nil:
		return dtype.Null, hl.Err()
	case hl.Negative():
		// Zero is taken, as Polars takes it: each row's answer is its own value.
		return dtype.Null, uerr.New(uerr.KindValue, name, "the half-life is %s, and must not be negative", hl)
	case hl.Months() != 0 || hl.Days() != 0 && by.TimeZone() != "":
		return dtype.Null, uerr.New(uerr.KindValue, name,
			"the half-life %s is not a constant duration over %s", hl, by).
			Hint("a month's length varies, and so does a zoned day's: use hours, e.g. \"730h\" or \"24h\"")
	}
	switch {
	case value.IsFloat():
		return value, nil
	case value.IsInteger() && value.ID() != dtype.TypeInt128:
		return dtype.Float64, nil
	}
	return dtype.Null, uerr.New(uerr.KindType, name, "%s() is not defined for %s", name, value).
		Hint("cast it first, e.g. .Cast(ursus.Float64)")
}

// rollingOut types a rolling function: its window first, then its operand.
//
// Each type is the matching aggregate's, so a full window's answer is the aggregate
// of its rows: a sum of integers is an Int128, which a window of them cannot wrap; a
// mean, var and std of numbers are floats; a min and max are the operand's own type,
// of any type with an order. A sum of a Decimal, a Duration or an Int128 could leave
// its type over a long enough window, and a mean of a Duration is a Duration, so
// those are refused: cast to Float64 first.
func rollingOut(fn WinFnOp, p WinParams, in dtype.DataType) (dtype.DataType, error) {
	if p.By {
		return rollingByOut(fn, p, in)
	}
	if p.N < 1 {
		return dtype.Null, uerr.New(uerr.KindValue, fn.String(),
			"a rolling window needs at least one row, got %d", p.N)
	}
	if p.MinSamples < 0 || p.MinSamples > p.N {
		return dtype.Null, uerr.New(uerr.KindValue, fn.String(),
			"min_samples is %d, and must be between 1 and the window's %d rows", p.MinSamples, p.N)
	}
	if p.Ddof < 0 || p.Ddof > 255 {
		return dtype.Null, uerr.New(uerr.KindValue, fn.String(),
			"ddof is %d, and must be between 0 and 255", p.Ddof)
	}
	switch fn {
	case WinRollingSkew, WinRollingKurtosis:
		// Polars' moments of a number, in float64, a Float32's given back as one.
		if !in.IsFloat() && !in.IsInteger() {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s() is not defined for %s", fn, in).
				Hint("cast it first, e.g. .Cast(ursus.Float64)")
		}
		if in == dtype.Float32 {
			return dtype.Float32, nil
		}
		return dtype.Float64, nil
	case WinRollingRank:
		return rollingRankOut(fn, p, in)
	case WinRollingCov, WinRollingCorr:
		// Two numbers, in their float type as Polars casts them: a Float32 for two
		// Float32s, and a Float64 otherwise.
		fs := in.Fields()
		if in.ID() != dtype.TypeStruct || len(fs) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s over %s, not a pair", fn, in)
		}
		for _, f := range fs {
			if !f.Type.IsNumeric() {
				return dtype.Null, uerr.New(uerr.KindType, fn.String(),
					"%s() is not defined for %s", fn, f.Type).
					Hint("cast it first, e.g. .Cast(ursus.Float64)")
			}
		}
		if fs[0].Type == dtype.Float32 && fs[1].Type == dtype.Float32 {
			return dtype.Float32, nil
		}
		return dtype.Float64, nil
	}
	return rollingValueOut(fn, in)
}

// rollingByOut types a rolling function by time (step 177): its by column and
// period first, as the frame-level Rolling checks its index, then its value as any
// rolling function's.
func rollingByOut(fn WinFnOp, p WinParams, in dtype.DataType) (dtype.DataType, error) {
	fs := in.Fields()
	if in.ID() != dtype.TypeStruct || len(fs) != 2 {
		return dtype.Null, uerr.Internalf("expr: %s by time over %s, not a pair", fn, in)
	}
	value, by := fs[0].Type, fs[1].Type
	name := fn.String() + "_by"
	switch by.ID() {
	case dtype.TypeDate, dtype.TypeDatetime, dtype.TypeTime, dtype.TypeInt32, dtype.TypeInt64, dtype.TypeUint32,
		dtype.TypeUint64:
	default:
		return dtype.Null, uerr.New(uerr.KindType, name,
			"%s orders its windows by a Date, a Datetime, a Time or an integer of 32 or 64 bits, got %s", name, by)
	}
	if err := IndexInterval(name, "period", p.Period, by); err != nil {
		return dtype.Null, err
	}
	switch {
	case p.Period.Err() != nil:
		return dtype.Null, p.Period.Err()
	case p.Period.IsZero() || p.Period.Negative():
		return dtype.Null, uerr.New(uerr.KindValue, name,
			"the period is %s, and must be positive", p.Period)
	case p.Center:
		return dtype.Null, uerr.New(uerr.KindValue, name,
			"a window by time ends at its row's own instant, and cannot be centered")
	case !p.Closed.Valid():
		return dtype.Null, uerr.New(uerr.KindValue, name, "closed is %d, not a Closed", uint8(p.Closed))
	case p.MinSamples < 1:
		return dtype.Null, uerr.New(uerr.KindValue, name,
			"min_samples is %d, and must be at least 1", p.MinSamples)
	case p.Ddof < 0 || p.Ddof > 255:
		return dtype.Null, uerr.New(uerr.KindValue, name,
			"ddof is %d, and must be between 0 and 255", p.Ddof)
	}
	// What the frame-level Rolling refuses of its index, for the same reasons: a
	// Time has no date for a calendar period to count, and a period finer than the
	// by column's tick would floor every window's start to the row's own.
	if by.ID() == dtype.TypeTime && p.Period.IsCalendar() {
		return dtype.Null, uerr.New(uerr.KindType, name,
			"the period %s is a calendar interval and by is %s, which has no date", p.Period, by).
			Hint("use a sub-day period, or cast by to Datetime first")
	}
	if npt, ok := by.NanosPerTick(); ok && p.Period.Nanos()%npt != 0 {
		return dtype.Null, uerr.New(uerr.KindValue, name,
			"the period %s is finer than by's %s ticks", p.Period, by).
			Hint("use a period of whole ticks, or cast by to a finer unit first")
	}
	if fn == WinRollingRank {
		return rollingRankOut(fn, p, value) // rolling_rank_by's, as rolling_rank's (step 338)
	}
	return rollingValueOut(fn, value)
}

// rollingRankOut is a rolling rank's type, of a value of type in: a Float64 by
// average, Polars' default, and a UInt32 by min, max or dense.
func rollingRankOut(fn WinFnOp, p WinParams, in dtype.DataType) (dtype.DataType, error) {
	// Any value with an order but text and a Decimal, which Polars refuses.
	if in.HasStringStorage() || in.ID() == dtype.TypeDecimal || !in.IsOrdered() {
		return dtype.Null, uerr.New(uerr.KindType, fn.String(),
			"%s() is not defined for %s", fn, in).
			Hint("it ranks numbers, instants and Booleans")
	}
	switch p.Method {
	case RankAverage:
		return dtype.Float64, nil
	case RankMin, RankMax, RankDense:
		return dtype.Uint32, nil
	}
	return dtype.Null, uerr.New(uerr.KindValue, fn.String(),
		"%s ranks by min, max, average or dense, got %s", fn, p.Method).
		Hint("Polars' rolling_rank has no ordinal; its random is not here")
}

// IndexInterval refuses an interval of the wrong kind for its column (step 312): an
// integer column counts by Polars' integer intervals, "3i", and nothing else; a
// temporal one takes time, and no "i". Polars refuses both, the first "must be a
// parsed integer", the second "may not be a parsed integer". A zero interval, which
// some options leave unset, passes either.
func IndexInterval(op, name string, iv dtype.Interval, column dtype.DataType) error {
	if iv.IsZero() || iv.Err() != nil {
		return nil
	}
	integer := column.IsInteger()
	switch {
	case integer && !iv.Index():
		return uerr.New(uerr.KindValue, op,
			"%s=%s is an interval of time, and the column is %s, which counts by an integer interval", name, iv, column).
			Hint("write it in the integer unit i, e.g. Every(\"3i\")")
	case !integer && iv.Index():
		return uerr.New(uerr.KindValue, op,
			"%s=%s counts an integer index, and the column is %s", name, iv, column).
			Hint("write it in time, e.g. Every(\"3d\"), or cast the column to an integer")
	}
	return nil
}

// rollingValueOut types a rolling function's value.
func rollingValueOut(fn WinFnOp, in dtype.DataType) (dtype.DataType, error) {
	if fn == WinRollingMin || fn == WinRollingMax {
		if !in.IsOrdered() {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s() is not defined for %s", fn, in).
				Hint("only numeric, temporal, string and boolean types have an ordering")
		}
		return in, nil
	}
	ok := in.IsFloat() || (in.IsInteger() && in.ID() != dtype.TypeInt128)
	switch fn {
	case WinRollingSum:
		ok = ok || in.IsBool()
	default:
		ok = ok || in.ID() == dtype.TypeDecimal
	}
	if !ok {
		return dtype.Null, uerr.New(uerr.KindType, fn.String(),
			"%s() is not defined for %s", fn, in).
			Hint("cast it first, e.g. .Cast(ursus.Float64)")
	}
	op := map[WinFnOp]AggOp{WinRollingSum: AggSum, WinRollingMean: AggMean,
		WinRollingVar: AggVar, WinRollingStd: AggStd, WinRollingMedian: AggMedian,
		WinRollingQuantile: AggQuantile}[fn]
	b, err := ResolveAggBinding(op, in)
	if err != nil {
		return dtype.Null, cumulativeErr(fn, in, err)
	}
	return b.Out, nil
}

// cumulativeErr is a refusal of the aggregate a cumulative function borrows its
// binding from, renamed to the function the caller wrote.
func cumulativeErr(fn WinFnOp, in dtype.DataType, cause error) error {
	return uerr.Wrap(cause, uerr.KindType, fn.String(),
		"%s() is not defined for %s", fn, in).
		Hint("it accumulates numbers: a numeric, Boolean or Decimal column")
}
