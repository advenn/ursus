package ursus

import (
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Interval is a calendar-aware span: months, days and nanoseconds, kept apart because
// each obeys a different rule. See dtype.Interval for why a time.Duration cannot
// express "1 month" or, in a daylight-saving zone, "1 day".
type Interval = dtype.Interval

// Every parses an interval: "1y", "3mo", "1w", "2d", "6h", "15m", "30s", "1ns".
// Components may be concatenated ("1h30m") and a leading "-" negates the whole thing.
//
// A grid of weeks starts on a Monday, as Polars': Truncate(Every("1w")) and a
// GroupByDynamic every "1w" give Mondays, where "7d" counts days from the epoch, a
// Thursday. Weeks mixed with any other unit count from the epoch too.
//
// "3i" is Polars' integer interval, three steps of an integer index: a
// GroupByDynamic, a Rolling or an Upsample over an Int32 or an Int64 index steps by
// it, as do the windows by such a column. It mixes with no unit of time, and is
// refused where a date is moved: by Truncate, Round, OffsetBy, a range or an as-of
// tolerance.
//
// A parse failure is carried in the value rather than returned, and surfaces when the
// query is built — the deferred-error rule the rest of the library follows. Note "m"
// is minutes and "mo" is months.
func Every(s string) Interval { return dtype.Every(s) }

// timeOnly refuses an integer interval, "3i", where op moves a date or an instant
// (step 312). Polars takes it in some of them and moves nothing, or panics.
func timeOnly(op string, iv Interval) error {
	if !iv.Index() {
		return nil
	}
	return uerr.New(uerr.KindValue, op, "%s takes an interval of time, and %s counts an integer index", op, iv).
		Hint(`"i" is for a temporal group, an upsample or a window by an integer column`)
}

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
