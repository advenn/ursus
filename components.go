package ursus

import (
	"time"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Values from their components (step 265, G3 of v0.6.6-scope.md): Polars' pl.date,
// pl.datetime, pl.time, pl.duration, pl.from_epoch and pl.business_day_count, each
// component a constant or an expression. Date, Datetime and Duration already name
// the types, so the first four are MakeDate, MakeDatetime, MakeTime and MakeDuration.
//
// Components that name no date or time, February 30th or an hour 25, are refused
// naming them and their row, as Polars refuses them; a null component gives null.

// MakeDate is the date of year, month and day, integers, a column named "date":
// Polars' pl.date.
//
//	lf.Select(MakeDate(Col("y"), Col("m"), 1))   // the first of each row's month
func MakeDate[Y, M, D Operand](year Y, month M, day D) Expr {
	return wrap(horizontal(expr.FnMakeDate, lift(year), lift(month), lift(day))).Alias("date")
}

// MakeDatetime is the naive Datetime(us) of its components, a column named
// "datetime": Polars' pl.datetime, every component given. For another unit cast it;
// for a zone, Dt().ReplaceTimeZone(zone), whose options say what an ambiguous or
// skipped wall clock becomes, as Polars' ambiguous does.
func MakeDatetime[Y, M, D, H, Mi, S, U Operand](year Y, month M, day D, hour H, minute Mi, second S,
	microsecond U) Expr {
	return wrap(horizontal(expr.FnMakeDatetime, lift(year), lift(month), lift(day), lift(hour),
		lift(minute), lift(second), lift(microsecond))).Alias("datetime")
}

// MakeTime is the time of day of its components, a Time(ns) column named "time":
// Polars' pl.time, every component given.
func MakeTime[H, Mi, S, U Operand](hour H, minute Mi, second S, microsecond U) Expr {
	return wrap(horizontal(expr.FnMakeTime, lift(hour), lift(minute), lift(second), lift(microsecond))).
		Alias("time")
}

// DurationParts are a duration's parts for MakeDuration, Polars' pl.duration
// keywords. Each is nil, for none, a Go number, or an Expr of numbers.
type DurationParts struct {
	Weeks, Days, Hours, Minutes, Seconds, Milliseconds, Microseconds, Nanoseconds any
}

// MakeDuration is the Duration of its parts, a column named "duration": Polars'
// pl.duration. The unit is unit if given, else microseconds, or nanoseconds when
// Nanoseconds is set, as Polars chooses it.
//
//	lf.Select(MakeDuration(DurationParts{Days: Col("d"), Hours: 12}))
//
// Integer parts sum exactly, and a total past the type's range is refused, where
// Polars wraps. With a float part the total is summed as a float and truncated toward
// zero, as Polars sums it. Polars names a duration of columns "literal"; this is
// "duration" either way.
func MakeDuration(p DurationParts, unit ...TimeUnit) Expr {
	u := Micro
	switch {
	case len(unit) == 1:
		u = unit[0]
	case len(unit) > 1:
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "duration", "duration takes at most one unit")})
	case p.Nanoseconds != nil:
		u = Nano
	}
	args := make([]expr.Node, 0, 9)
	for _, v := range []any{p.Weeks, p.Days, p.Hours, p.Minutes, p.Seconds, p.Milliseconds,
		p.Microseconds, p.Nanoseconds} {
		switch x := v.(type) {
		case nil:
			args = append(args, litNode(int64(0)))
		case Expr:
			args = append(args, x.node())
		default:
			args = append(args, litNode(x))
		}
	}
	args = append(args, litNode(int64(u.Duration())))
	return wrap(horizontal(expr.FnDurationTicks, args...)).Cast(Duration(u)).Alias("duration")
}

// FromEpoch is the naive instant e units after 1970-01-01T00:00:00: Polars'
// from_epoch. Seconds, milliseconds and microseconds give a Datetime(us), nanoseconds
// a Datetime(ns). An integer count past the type's range is refused, where Polars
// wraps; a float one is truncated toward zero at the answer's unit, as Polars
// truncates it. Days since the epoch are e.Cast(Date).
func FromEpoch(e Expr, unit TimeUnit) Expr {
	out, factor := Micro, int64(1)
	switch unit {
	case Second:
		factor = 1_000_000
	case Milli:
		factor = 1_000
	case Micro:
	case Nano:
		out = Nano
	default:
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "from_epoch", "from_epoch counts in %s", unit).
			Hint("seconds, milliseconds, microseconds or nanoseconds; days are e.Cast(Date)")})
	}
	return wrap(horizontal(expr.FnEpochTicks, e.node(), litNode(factor))).Cast(Datetime(out, ""))
}

// BusinessCalendar is what a business day is for BusinessDayCount, and for the
// business-day functions of .dt: Polars' week_mask and holidays.
type BusinessCalendar struct {
	// WeekMask holds the days of the week that are business days, Monday first. All
	// false means Monday to Friday, Polars' default.
	WeekMask [7]bool
	// Holidays are dates that are not business days, read as their UTC date.
	Holidays []time.Time
}

// calendarArgs is a calendar's operands: its mask as an Int64 literal, Monday bit 0,
// and its holidays a List(Int64) literal of days since the epoch.
func calendarArgs(op string, cal []BusinessCalendar) ([]expr.Node, error) {
	var c BusinessCalendar
	switch len(cal) {
	case 0:
	case 1:
		c = cal[0]
	default:
		return nil, uerr.New(uerr.KindValue, op, "%s takes at most one BusinessCalendar", op)
	}
	var mask int64
	for i, b := range c.WeekMask {
		if b {
			mask |= 1 << i
		}
	}
	if mask == 0 {
		mask = 0b11111
	}
	days := make([]int64, len(c.Holidays))
	for i, h := range c.Holidays {
		days[i] = floorDiv(h.Unix(), 86_400)
	}
	return []expr.Node{litNode(mask), litNode(days)}, nil
}

// floorDiv is a/b rounded toward negative infinity.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// BusinessDayCount is the business days from start up to end, end excluded, an
// Int32: Polars' business_day_count. Where end is before start it is minus the
// business days from end up to start. The ends are Dates.
func BusinessDayCount[S, E Operand](start S, end E, cal ...BusinessCalendar) Expr {
	args, err := calendarArgs("business_day_count", cal)
	if err != nil {
		return wrap(&expr.Err{E: err})
	}
	return wrap(horizontal(expr.FnBusinessDayCount, append([]expr.Node{lift(start), lift(end)}, args...)...))
}
