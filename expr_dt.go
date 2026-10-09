package ursus

import (
	"github.com/advenn/ursus/internal/expr"
)

// DtExpr is the temporal namespace: `Col("ts").Dt().Year()`.
//
// # Components are read in the column's own timezone
//
// 2024-01-01T00:00:00+09:00 is hour 0 in Tokyo and hour 15 in UTC. A Datetime
// carries its zone, and every component below is read in it — otherwise `hour`
// would be wrong for every row of any non-UTC column, silently.
//
// # Durations have no components
//
// `.Year()` on a Duration is refused at plan time. A duration is a length, not a
// point in time; "the year of 90 minutes" has no answer. Use the Total* family.
type DtExpr struct{ e Expr }

// Dt opens the temporal namespace.
func (e Expr) Dt() DtExpr { return DtExpr{e} }

func (d DtExpr) call(fn expr.CallFn, args ...any) Expr {
	nodes := make([]expr.Node, 0, len(args)+1)
	nodes = append(nodes, d.e.node())
	for _, a := range args {
		nodes = append(nodes, litNode(a))
	}
	return wrap(&expr.Call{Fn: fn, Args: nodes})
}

func (d DtExpr) Year() Expr        { return d.call(expr.FnDtYear) }
func (d DtExpr) Month() Expr       { return d.call(expr.FnDtMonth) }
func (d DtExpr) Day() Expr         { return d.call(expr.FnDtDay) }
func (d DtExpr) Hour() Expr        { return d.call(expr.FnDtHour) }
func (d DtExpr) Minute() Expr      { return d.call(expr.FnDtMinute) }
func (d DtExpr) Second() Expr      { return d.call(expr.FnDtSecond) }
func (d DtExpr) Millisecond() Expr { return d.call(expr.FnDtMillisecond) }
func (d DtExpr) Microsecond() Expr { return d.call(expr.FnDtMicrosecond) }
func (d DtExpr) Nanosecond() Expr  { return d.call(expr.FnDtNanosecond) }

// Weekday is ISO: Monday is 1 and Sunday is 7. Go's time.Weekday puts Sunday at 0,
// and passing that through is the classic off-by-one in date libraries.
func (d DtExpr) Weekday() Expr { return d.call(expr.FnDtWeekday) }

// OrdinalDay is the day of the year, 1-366.
func (d DtExpr) OrdinalDay() Expr { return d.call(expr.FnDtOrdinalDay) }

// Quarter is 1-4.
func (d DtExpr) Quarter() Expr { return d.call(expr.FnDtQuarter) }

// Week is the ISO 8601 week number, which is not "day of year / 7" — the first
// week of a year can begin in the previous one.
func (d DtExpr) Week() Expr { return d.call(expr.FnDtWeek) }

// Epoch is whole seconds since 1970-01-01T00:00:00Z, floored: the second an
// instant lies in, so half a second before the epoch is -1.
func (d DtExpr) Epoch() Expr { return d.call(expr.FnDtEpoch) }

// Truncate floors an instant to a multiple of every, toward negative infinity so
// instants before the epoch move backwards like every other instant.
//
// # A Duration floors ABSOLUTELY; an Interval floors on the CALENDAR
//
// The two spellings answer different questions and neither is a special case of the
// other:
//
//	Truncate(time.Hour)     the instant, floored to a whole hour since the epoch
//	Truncate(Every("1d"))   the start of the LOCAL day, in the column's own zone
//	Truncate(Every("1h"))   the start of the LOCAL hour — 10:00 for 10:47 at +05:30,
//	                        where time.Hour gives 10:30, the hour since the epoch
//
// An Interval floors the wall clock at every size. Where the floored wall clock
// falls into a spring-forward gap, the answer is the instant the gap ends; in a
// fall-back fold, the input keeps its own offset, so the second 01:30 floors to the
// second 01:00. Both are Polars' answers.
//
// A Duration is a fixed span of elapsed time, so flooring by one is zone-independent
// by definition — `Truncate(24*time.Hour)` on a New York column lands on 00:00 UTC,
// which is 19:00 or 20:00 local, and that is the correct answer to the question a
// Duration asks. It is almost never the question a user grouping by day is asking,
// which is why Every("1d") exists and reads the column's timezone.
func (d DtExpr) Truncate[T Span](every T) Expr {
	iv := span(every)
	if err := iv.Err(); err != nil {
		return wrap(&expr.Err{E: err})
	}
	// The fourth argument says which spelling it was: an Interval floors the wall
	// clock and a Duration the instant, and both arrive as the same three numbers.
	wall := int64(0)
	if _, ok := any(every).(Interval); ok {
		wall = 1
	}
	return d.call(expr.FnDtTruncate,
		int64(iv.Months()), int64(iv.Days()), iv.Nanos(), wall)
}

// The Total family converts a Duration to a whole number of units, truncating.
// They are the only temporal functions defined on a Duration.
func (d DtExpr) TotalDays() Expr    { return d.call(expr.FnDtTotalDays) }
func (d DtExpr) TotalHours() Expr   { return d.call(expr.FnDtTotalHours) }
func (d DtExpr) TotalMinutes() Expr { return d.call(expr.FnDtTotalMinutes) }
func (d DtExpr) TotalSeconds() Expr { return d.call(expr.FnDtTotalSeconds) }

// ToString formats using the same ISO 8601 layouts the CSV writer emits, so a
// frame printed to a terminal and a frame written to a file agree.
func (d DtExpr) ToString() Expr { return d.e.Cast(String) }

// Strftime formats a Date, Datetime or Time with a strftime-style format, in a
// zoned Datetime's own wall clock — Polars' dt.strftime.
//
//	Col("day").Dt().Strftime("%A, %e %B %Y")   // "Friday,  9 February 2024"
//
// The directives are Strptime's. A format that asks for what the value does not
// have is refused while the query is planned: a date from a Time, or an offset or
// zone name (%z, %Z) from anything but a zoned Datetime.
func (d DtExpr) Strftime(format string) Expr { return d.call(expr.FnDtStrftime, format) }

// OffsetBy moves each Date or Datetime by an interval, keeping its type: Polars'
// dt.offset_by.
//
//	Col("due").Dt().OffsetBy(Every("1mo"))      // the same day next month, clamped
//	Col("ts").Dt().OffsetBy(-90 * time.Minute)  // ninety minutes earlier
//
// Months and days move the WALL CLOCK in the column's own zone: one day after 09:00
// is 09:00 the next day across a daylight-saving change, and January 31st plus a
// month is the last day of February. Nanoseconds, and a time.Duration, move elapsed
// time. A wall clock the zone skips gives the instant the gap ends; one it repeats,
// the reading at the input's own offset when that offset holds there.
//
// An offset the type cannot hold is refused while the query is planned: an hour on a
// Date, a nanosecond on Datetime(us). An answer past the type's range is refused
// when it is computed. A Time has no date to move: add a Duration to it instead.
func (d DtExpr) OffsetBy[T Span](by T) Expr {
	iv := span(by)
	if err := iv.Err(); err != nil {
		return wrap(&expr.Err{E: err})
	}
	return d.call(expr.FnDtOffsetBy, int64(iv.Months()), int64(iv.Days()), iv.Nanos())
}

// Round rounds a Date or Datetime to the nearer boundary of Truncate's grid, and a
// value exactly halfway up: Polars' dt.round.
//
//	Col("ts").Dt().Round(15 * time.Minute)   // 10:07:30 → 10:15, 10:07:29 → 10:00
//	Col("ts").Dt().Round(Every("1mo"))       // the nearer first of a month
//
// The grid is Truncate's, in both spellings, and nearer is measured in elapsed time:
// the middle of February 2024 is 2024-02-15T12:00, and the middle of a 23-hour day
// 11:30 after its start. A Time is refused, because it can round up to 24:00, which
// it cannot hold.
func (d DtExpr) Round[T Span](every T) Expr {
	iv := span(every)
	if err := iv.Err(); err != nil {
		return wrap(&expr.Err{E: err})
	}
	wall := int64(0)
	if _, ok := any(every).(Interval); ok {
		wall = 1
	}
	return d.call(expr.FnDtRound, int64(iv.Months()), int64(iv.Days()), iv.Nanos(), wall)
}

// MonthStart moves a Date or Datetime to the first day of its month, keeping the
// time of day on its wall clock: Polars' dt.month_start. A Datetime's month is the
// one in its own zone.
func (d DtExpr) MonthStart() Expr { return d.call(expr.FnDtMonthStart) }

// MonthEnd moves a Date or Datetime to the last day of its month, keeping the time
// of day, as MonthStart does: Polars' dt.month_end. February 2024 ends on the 29th.
func (d DtExpr) MonthEnd() Expr { return d.call(expr.FnDtMonthEnd) }

// IsLeapYear reports whether a Date or Datetime falls in a year of 366 days, the year
// read in the column's own zone.
func (d DtExpr) IsLeapYear() Expr { return d.call(expr.FnDtIsLeapYear) }

// ConvertTimeZone reads a zoned Datetime's instants in another zone: the same
// instants, whose components, Strftime and rendering now follow tz. Polars'
// dt.convert_time_zone.
//
//	Col("ts").Dt().ConvertTimeZone("Asia/Tokyo")   // 09:00 UTC reads as 18:00
//
// No value changes, so it costs nothing per row. A naive Datetime names no instant
// to convert and is refused; if its wall clocks are UTC, cast it to
// Datetime(unit, "UTC") first. A zone the system does not know is refused while the
// query is planned.
func (d DtExpr) ConvertTimeZone(tz string) Expr { return d.call(expr.FnDtConvertTimeZone, tz) }
