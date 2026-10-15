package ursus

import (
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Generators (step 263, G1 of v0.6.6-scope.md): Polars' int_range, linear_space,
// repeat, ones and zeros, and their per-row forms.
//
// The per-row form is one List a row: IntRanges(Col("lo"), Col("hi"), 1) is each
// row's integers from lo up to hi. The scalar form is that list for one row of its
// arguments, exploded, a column of its own length:
//
//	lf.Select(IntRange(0, 5, 1))                       // 0, 1, 2, 3, 4
//	lf.WithColumns(IntRange(0, Len(), 1).Alias("i"))    // a row index
//	lf.GroupBy(Col("g")).Agg(IntRange(0, Len(), 1))     // one a group
//
// Its arguments are constants or aggregates, one value for the frame, or for the
// group in an aggregation. In Select and WithColumns it stands beside other columns
// as a length-changing expression does (step 258), so its length is checked against
// theirs; an aggregate over it is one value, IntRange(0, 5, 1).Sum().
//
// A range is built whole, as Polars builds it, and one batch of them holds at most
// 2³¹ - 1 elements. For a row index of a frame larger than memory, WithRowIndex
// streams.

// explodeOf is the scalar form of a generator: its one list exploded, an empty list
// no row.
func explodeOf(list expr.Node) Expr {
	return wrap(&expr.Reshape{Op: expr.ReshapeExplode, Child: list, Args: []int64{1}})
}

func horizontal(fn expr.CallFn, args ...expr.Node) expr.Node {
	return &expr.Call{Fn: fn, Args: args}
}

// IntRange is the integers from start up to end, end excluded, by step, as Int64:
// Polars' int_range. A negative step counts down, a range that step points away
// from is empty, and a step of zero is refused. Cast the column for another integer
// type.
func IntRange[S, E Operand](start S, end E, step int) Expr {
	return explodeOf(horizontal(expr.FnIntRanges, lift(start), lift(end), litNode(int64(step))))
}

// IntRanges is IntRange a row, a List(Int64): Polars' int_ranges. Each argument is a
// constant or an expression, and a null among a row's makes its list null.
func IntRanges[S, E, T Operand](start S, end E, step T) Expr {
	return wrap(horizontal(expr.FnIntRanges, lift(start), lift(end), lift(step)))
}

// closedOf is a generator's Closed, ClosedBoth unless one is given; more than one is
// refused.
func closedOf(op string, closed []Closed) (Closed, error) {
	switch len(closed) {
	case 0:
		return ClosedBoth, nil
	case 1:
		if c := closed[0].Or(ClosedBoth); c.Valid() {
			return c, nil
		}
		return 0, uerr.New(uerr.KindValue, op, "Closed(%d) is not a declared Closed", uint8(closed[0])).
			Hint("use ClosedLeft, ClosedRight, ClosedBoth or ClosedNone")
	}
	return 0, uerr.New(uerr.KindValue, op, "%s takes at most one Closed, got %d", op, len(closed))
}

// LinearSpace is n values evenly spaced from start to end: Polars' linear_space.
// Both ends belong unless closed says otherwise, ClosedLeft, ClosedRight or
// ClosedNone.
//
// Two numbers give Float64, two Float32 ends Float32. Two values of one temporal
// type give that type, spaced over its ticks and truncated toward zero, and two
// Dates a Datetime(us), as Polars gives them. The values are Polars' to the bit: each
// but the last is the first plus i steps, and the last is the far end exactly.
func LinearSpace[S, E Operand](start S, end E, n int, closed ...Closed) Expr {
	c, err := closedOf("linear_space", closed)
	if err != nil {
		return wrap(&expr.Err{E: err})
	}
	return explodeOf(horizontal(expr.FnLinearSpaces, lift(start), lift(end), litNode(int64(n)),
		litNode(int64(c))))
}

// LinearSpaces is LinearSpace a row, a List: Polars' linear_spaces. n may be an
// expression too.
func LinearSpaces[S, E, N Operand](start S, end E, n N, closed ...Closed) Expr {
	c, err := closedOf("linear_spaces", closed)
	if err != nil {
		return wrap(&expr.Err{E: err})
	}
	return wrap(horizontal(expr.FnLinearSpaces, lift(start), lift(end), lift(n), litNode(int64(c))))
}

// RepeatBy is each row's value n times, a List: Polars' repeat_by. A null value is n
// nulls, a null n a null list, and a negative n is refused.
func (e Expr) RepeatBy[N Operand](n N) Expr {
	return wrap(horizontal(expr.FnRepeatBy, e.node(), lift(n)))
}

// Repeat is value n times, a column named "repeat": Polars' repeat. Both are
// constants or aggregates: Repeat(Col("x").Sum(), Len()) is the total on every row.
// A literal integer is Int64 here, where Polars makes it Int32.
func Repeat[V, N Operand](value V, n N) Expr {
	return explodeOf(horizontal(expr.FnRepeatBy, lift(value), lift(n))).Alias("repeat")
}

// Ones and Zeros are 1.0 and 0.0 n times, Float64 columns named "ones" and "zeros":
// Polars' ones and zeros. Cast them for another type.
func Ones[N Operand](n N) Expr {
	return explodeOf(horizontal(expr.FnRepeatBy, litNode(1.0), lift(n))).Alias("ones")
}

func Zeros[N Operand](n N) Expr {
	return explodeOf(horizontal(expr.FnRepeatBy, litNode(0.0), lift(n))).Alias("zeros")
}

// Temporal ranges (step 264, G2 of v0.6.6-scope.md): Polars' date_range,
// datetime_range and time_range, and their per-row forms. The k-th value is start
// plus k intervals, k = 0, 1, …, not each from the one before, so a month from
// January 31st is February 29th, then March 31st. every is an Interval, Every("1mo"),
// or a time.Duration. Its calendar parts move the wall clock in the ends' zone and the
// rest elapsed time, as OffsetBy moves them. Both ends belong unless closed says
// otherwise, and a negative interval gives none, as Polars gives none.

const nanosPerDay = int64(24 * time.Hour)

// temporalRange is a temporal range's call over start and end: the scalar form
// exploded, or the per-row form.
func temporalRange(fn expr.CallFn, start, end expr.Node, iv Interval, closed []Closed, scalar bool) Expr {
	op := expr.Generators[fn]
	if err := iv.Err(); err != nil {
		return wrap(&expr.Err{E: err})
	}
	if iv.IsZero() {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, op, "%s steps by an interval of zero", op)})
	}
	c, err := closedOf(op, closed)
	if err != nil {
		return wrap(&expr.Err{E: err})
	}
	call := horizontal(fn, start, end, litNode(int64(iv.Months())), litNode(int64(iv.Days())),
		litNode(iv.Nanos()), litNode(int64(c)))
	if scalar {
		return explodeOf(call)
	}
	return wrap(call)
}

// dateInterval is iv in whole days, which a Date steps by: Polars refuses a finer
// one.
func dateInterval(op string, iv Interval) (Interval, error) {
	if iv.Nanos()%nanosPerDay != 0 {
		return iv, uerr.New(uerr.KindValue, op, "%s steps by whole days, and %s is not", op, iv).
			Hint("a finer interval is DatetimeRange's")
	}
	return dtype.IntervalOf(iv.Months(), iv.Days()+int32(iv.Nanos()/nanosPerDay), 0), nil
}

func dateRange[S, E Operand, T Span](fn expr.CallFn, start S, end E, every T, closed []Closed, scalar bool) Expr {
	iv, err := dateInterval(expr.Generators[fn], span(every))
	if err != nil {
		return wrap(&expr.Err{E: err})
	}
	return temporalRange(fn, lift(start), lift(end), iv, closed, scalar)
}

// DateRange is the dates from start to end every interval, a Date column: Polars'
// date_range. A Datetime end is read as its UTC date, as a cast reads it. The
// interval is whole days, weeks, months or years.
//
//	lf.Select(DateRange(Lit(jan31).Cast(Date), Lit(may1).Cast(Date), Every("1mo")))
func DateRange[S, E Operand, T Span](start S, end E, every T, closed ...Closed) Expr {
	return dateRange(expr.FnDateRanges, start, end, every, closed, true)
}

// DateRanges is DateRange a row, a List(Date): Polars' date_ranges.
func DateRanges[S, E Operand, T Span](start S, end E, every T, closed ...Closed) Expr {
	return dateRange(expr.FnDateRanges, start, end, every, closed, false)
}

// DatetimeRange is the instants from start to end every interval: Polars'
// datetime_range. Its type is the ends', two Dates Datetime(us); cast the ends for
// another unit or zone, e.g. Lit(t).Cast(Datetime(Micro, "")).Dt().ReplaceTimeZone(z),
// and the range steps on z's wall clock.
func DatetimeRange[S, E Operand, T Span](start S, end E, every T, closed ...Closed) Expr {
	return temporalRange(expr.FnDatetimeRanges, lift(start), lift(end), span(every), closed, true)
}

// DatetimeRanges is DatetimeRange a row, a List: Polars' datetime_ranges.
func DatetimeRanges[S, E Operand, T Span](start S, end E, every T, closed ...Closed) Expr {
	return temporalRange(expr.FnDatetimeRanges, lift(start), lift(end), span(every), closed, false)
}

func timeRange[S, E Operand, T Span](start S, end E, every T, closed []Closed, scalar bool) Expr {
	iv := span(every)
	if iv.IsCalendar() {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "time_range",
			"time_range steps by a duration within a day, and %s has a calendar part", iv)})
	}
	return temporalRange(expr.FnTimeRanges, lift(start), lift(end), iv, closed, scalar)
}

// TimeRange is the times of day from start to end every interval, a Time column:
// Polars' time_range. Polars' defaults, midnight to 23:59:59.999999 by the hour,
// are spelled out here.
func TimeRange[S, E Operand, T Span](start S, end E, every T, closed ...Closed) Expr {
	return timeRange(start, end, every, closed, true)
}

// TimeRanges is TimeRange a row, a List(Time): Polars' time_ranges.
func TimeRanges[S, E Operand, T Span](start S, end E, every T, closed ...Closed) Expr {
	return timeRange(start, end, every, closed, false)
}
