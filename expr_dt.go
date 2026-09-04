package ursus

import (
	"ursus/internal/expr"
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
	nodes = append(nodes, d.e.n)
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

// Epoch is whole seconds since 1970-01-01T00:00:00Z.
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
	return d.call(expr.FnDtTruncate,
		int64(iv.Months()), int64(iv.Days()), iv.Nanos())
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
