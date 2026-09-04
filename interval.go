package ursus

import (
	"time"

	"ursus/dtype"
)

// Interval is a calendar-aware span: months, days and nanoseconds, kept apart because
// each obeys a different rule. See dtype.Interval for why a time.Duration cannot
// express "1 month" or, in a daylight-saving zone, "1 day".
type Interval = dtype.Interval

// Every parses an interval: "1y", "3mo", "1w", "2d", "6h", "15m", "30s", "1ns".
// Components may be concatenated ("1h30m") and a leading "-" negates the whole thing.
//
// A parse failure is carried in the value rather than returned, and surfaces when the
// query is built — the deferred-error rule the rest of the library follows. Note "m"
// is minutes and "mo" is months.
func Every(s string) Interval { return dtype.Every(s) }

// FromDuration builds an ABSOLUTE interval. Deliberately not the same as the calendar
// spelling: FromDuration(24*time.Hour) is twenty-four hours, Every("1d") is one day,
// and on the two days a year a zone changes its offset those are different instants.
func FromDuration(d time.Duration) Interval { return dtype.FromDuration(d) }

// Span is an interval in either spelling — the calendar-aware Interval, or a plain
// time.Duration for the absolute case.
//
// The same union shape as Operand, and for the same reason: it lets one method accept
// both forms with no wrapper at the call site, so `Truncate(time.Hour)` keeps
// compiling now that `Truncate(Every("1mo"))` is also legal.
type Span interface {
	Interval | time.Duration
}

// span converts either spelling into an Interval.
func span[T Span](v T) Interval {
	if iv, ok := any(v).(Interval); ok {
		return iv
	}
	return FromDuration(any(v).(time.Duration))
}
