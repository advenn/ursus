package expr

import (
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// DateTimeParts are dt.replace's components, in their operands' order.
var DateTimeParts = []string{"year", "month", "day", "hour", "minute", "second", "microsecond"}

// TotalUnits are the units a fractional total counts in, as their nanoseconds: days
// to nanoseconds. EpochUnits are epoch's: nanoseconds to seconds, and days.
var (
	TotalUnits = []int64{86_400e9, 3_600e9, 60e9, 1e9, 1e6, 1e3, 1}
	EpochUnits = []int64{1, 1e3, 1e6, 1e9, EpochDay}
)

// EpochDay is a day's nanoseconds, epoch's unit for days.
const EpochDay = 86_400e9

// dtCallOut takes the Call rather than the CallFn because truncate's answer depends
// on its INTERVAL and not only on its receiver. See ResolveCall's doc.
func dtCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	isDur := in.ID() == dtype.TypeDuration
	isInstant := in.ID() == dtype.TypeDate || in.ID() == dtype.TypeDatetime ||
		in.ID() == dtype.TypeTime

	switch fn {
	case FnDtIsoYear, FnDtCentury, FnDtMillennium:
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, in)
		}
		return dtype.Int32, nil

	case FnDtBaseUtcOffset, FnDtDstOffset:
		if in.ID() != dtype.TypeDatetime || in.TimeZone() == "" {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Datetime with a time zone, got %s", fn, in).
				Hint("give it one with .Dt().ReplaceTimeZone(tz)")
		}
		// Milliseconds whatever the value's unit, as Polars answers.
		return dtype.Duration(dtype.Milli), nil

	case FnDtWithTimeUnit, FnDtCastTimeUnit:
		args, err := CallArgs(c)
		if err != nil {
			return dtype.Null, err
		}
		unit := dtype.TimeUnit(callLitInt(args, 0))
		if len(args) != 1 || unit > dtype.Nano {
			return dtype.Null, uerr.Internalf("expr: %s without its unit", fn)
		}
		switch in.ID() {
		case dtype.TypeDatetime:
			return dtype.Datetime(unit, in.TimeZone()), nil
		case dtype.TypeDuration:
			return dtype.Duration(unit), nil
		}
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime or Duration operand, got %s", fn, in)

	case FnDtTotalMilliseconds, FnDtTotalMicroseconds, FnDtTotalNanoseconds, FnDtTotalFractional:
		if !isDur {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Duration operand, got %s", fn, in).
				Hint("subtract two instants to get a Duration")
		}
		if fn != FnDtTotalFractional {
			return dtype.Int64, nil
		}
		args, err := CallArgs(c)
		if err != nil {
			return dtype.Null, err
		}
		if per := callLitInt(args, 0); len(args) != 1 || !slices.Contains(TotalUnits, per) {
			return dtype.Null, uerr.Internalf("expr: %s in %d nanoseconds", fn, per)
		}
		return dtype.Float64, nil

	case FnDtSecondFractional:
		if in.ID() != dtype.TypeDatetime && in.ID() != dtype.TypeTime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Datetime or Time operand, got %s", fn, in)
		}
		return dtype.Float64, nil

	case FnDtEpochIn:
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, in)
		}
		args, err := CallArgs(c)
		if err != nil {
			return dtype.Null, err
		}
		per := callLitInt(args, 0)
		if len(args) != 1 || !slices.Contains(EpochUnits, per) {
			return dtype.Null, uerr.Internalf("expr: %s in %d nanoseconds", fn, per)
		}
		if per == EpochDay {
			return dtype.Int32, nil
		}
		return dtype.Int64, nil

	case FnDtTotalDays, FnDtTotalHours, FnDtTotalMinutes, FnDtTotalSeconds:
		if !isDur {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Duration operand, got %s", fn, in).
				Hint("subtract two instants to get a Duration")
		}
		return dtype.Int64, nil

	case FnDtTruncate, FnDtRound:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return truncateOut(c, in)

	case FnDtOffsetBy, FnDtMonthStart, FnDtMonthEnd, FnDtIsLeapYear, FnDtDaysInMonth:
		// A Time has no date to move or to read a year from.
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			e := uerr.New(uerr.KindType, "dt", "%s requires a Date or Datetime operand, got %s", fn, in)
			if fn == FnDtOffsetBy && (isDur || in.ID() == dtype.TypeTime) {
				e.Hint("add a Duration with Add instead")
			}
			return dtype.Null, e
		}
		switch fn {
		case FnDtIsLeapYear:
			return dtype.Bool, nil
		case FnDtDaysInMonth:
			// Int8, as Polars': a month has 28 to 31 days.
			return dtype.Int8, nil
		case FnDtOffsetBy:
			args, err := CallArgs(c)
			if err != nil {
				return dtype.Null, err
			}
			iv := dtype.IntervalOf(
				int32(callLitInt(args, 0)), int32(callLitInt(args, 1)), callLitInt(args, 2))
			if err := OffsetRefusal(iv, in); err != nil {
				return dtype.Null, err
			}
		}
		return in, nil

	case FnDtConvertTimeZone:
		return convertZoneOut(c, in)

	case FnDtReplaceTimeZone:
		return replaceZoneOut(c, in)

	case FnDtStrftime:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return strftimeOut(c, in)

	case FnDtEpoch:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return dtype.Int64, nil

	case FnDtDate:
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, in)
		}
		return dtype.Date, nil

	case FnDtTime:
		switch in.ID() {
		case dtype.TypeTime:
			return in, nil
		case dtype.TypeDatetime:
			// The Datetime's own unit: a time of day in it is exact, and Polars'
			// nanoseconds would overflow nothing but gain nothing either.
			return dtype.Time(in.TimeUnit()), nil
		}
		e := uerr.New(uerr.KindType, "dt", "%s requires a Time or Datetime operand, got %s", fn, in)
		if in.ID() == dtype.TypeDate {
			e.Hint("a Date has no time of day")
		}
		return dtype.Null, e

	default:
		if !isInstant {
			e := uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
			if isDur {
				e.Hint("a Duration is a length, not a point in time; it has no " +
					"calendar components — use TotalDays, TotalHours and friends")
			} else {
				e.Hint("cast the column first, e.g. .Cast(ursus.Datetime(ursus.Micro, \"UTC\"))")
			}
			return dtype.Null, e
		}
		// Components are small non-negative numbers except the year, which can be
		// negative. Int32 throughout keeps one kernel shape.
		return dtype.Int32, nil
	}
}

// truncateOut types dt.truncate and dt.round, whose refusals depend on their
// argument rather than only on their receiver.
//
// # It asks the kernel's rule, it does not paraphrase it
//
// The interval is rebuilt with dtype.IntervalOf exactly as truncateTemporal does, and
// the refusal is GridRefusal, which the kernel asks too. A plan-time check that
// drifted from the kernel would be worse than no check at all, which is the Binding
// lesson one file over. Until step 145 the two were copies, held together by
// TestTruncateRefusalsAgree in internal/kernel, which still runs.
//
// A missing argument reads as zero, which the first refusal then catches — the same
// answer the kernel's argInt gives, and a truncate node with no interval is not a
// thing any builder produces.
func truncateOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	args, err := CallArgs(c)
	if err != nil {
		return dtype.Null, err
	}
	iv := dtype.IntervalOf(
		int32(callLitInt(args, 0)), int32(callLitInt(args, 1)), callLitInt(args, 2))

	if err := GridRefusal(c.Fn, iv, in); err != nil {
		return dtype.Null, err
	}
	return in, nil
}

// GridRefusal is dt.truncate's and dt.round's refusal of their interval, for a
// receiver of type in. truncateOut asks it at plan time and the kernel at run time,
// which kernel.DtCall's callers reach without resolving, so the two give one answer
// (TestTruncateRefusalsAgree).
func GridRefusal(fn CallFn, iv dtype.Interval, in dtype.DataType) error {
	verb, method := "truncate", "Truncate"
	if fn == FnDtRound {
		verb, method = "round", "Round"
	}
	// Reachable from the public API, which is why it is checked at plan time.
	// dtype.FromDuration does no validation, so `Truncate(time.Duration(0))` and
	// `Truncate(-time.Hour)` carry no error out of DtExpr.Truncate's iv.Err() check —
	// they planned, rendered in Explain, and failed at Collect.
	//
	// Err() is checked first, because an interval that failed to BUILD renders as
	// <invalid> and the message below would then say nothing useful. Ordering is the
	// whole of it: after, this would be unreachable, since a refused interval is
	// zero-valued and IsZero fires first.
	if err := iv.Err(); err != nil {
		return err
	}
	if iv.IsZero() || iv.Negative() {
		return uerr.New(uerr.KindValue, "dt",
			"%s needs a positive interval, got %s", verb, iv).
			Hint(`pass a time.Duration or an interval, e.g. .Dt().%s(time.Hour) `+
				`or .Dt().%s(ursus.Every("1mo"))`, method, method)
	}
	if in.ID() != dtype.TypeTime {
		return nil
	}
	// A Time rounded up from 23:59 to the hour is 24:00, which a Time cannot hold, and
	// wrapping it to 00:00 would put it before the value it rounds.
	if fn == FnDtRound {
		return uerr.New(uerr.KindType, "dt", "round is not defined for %s", in).
			Hint("a Time can round up to 24:00, which it cannot hold").
			Hint("use Truncate, or cast to Datetime first")
	}
	// A Time is a wall clock with no date. A sub-day interval on the same column is
	// fine, which is exactly why the receiver's type alone could never decide it.
	if iv.IsCalendar() {
		return uerr.New(uerr.KindType, "dt",
			"truncate by %s is not defined for %s", iv, in).
			Hint("a Time has no date, so it cannot be floored to a day or a month").
			Hint("use a sub-day interval, or cast to Datetime first")
	}
	return nil
}

// OffsetRefusal is dt.offset_by's refusal of its interval, for a Date or Datetime of
// type in, asked at plan time and by the kernel as GridRefusal is.
//
// The answer has the receiver's type, so an offset it cannot hold is refused rather
// than rounded: an hour is no number of days, and a nanosecond no number of
// microseconds.
func OffsetRefusal(iv dtype.Interval, in dtype.DataType) error {
	if err := iv.Err(); err != nil {
		return err
	}
	if iv.Index() {
		// A row's "3i", which Polars reads as no offset at all (step 312).
		return uerr.New(uerr.KindValue, "dt", "offset_by takes an interval of time, and %s counts an integer index", iv)
	}
	npt, ok := in.NanosPerTick()
	if !ok {
		return uerr.Internalf("dt: offset_by on %s", in)
	}
	if iv.Nanos()%npt == 0 {
		return nil
	}
	e := uerr.New(uerr.KindType, "dt", "offset_by %s is finer than %s holds", iv, in)
	if in.ID() == dtype.TypeDate {
		return e.Hint("a Date moves by whole days; cast it to a Datetime first")
	}
	return e.Hint("cast it to a finer unit first, e.g. .Cast(ursus.Datetime(ursus.Nano, zone))")
}

// convertZoneOut types dt.convert_time_zone: the same instants, labelled with another
// zone, so their wall clocks are read there. A naive Datetime is a wall clock in no
// zone and names no instant, so it is refused, as Polars refuses it.
func convertZoneOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	if in.ID() != dtype.TypeDatetime {
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime operand, got %s", c.Fn, in)
	}
	if in.TimeZone() == "" {
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime with a time zone, got %s", c.Fn, in).
			Hint("a naive Datetime names no instant to convert; if its wall clocks " +
				"are UTC, cast it first: .Cast(ursus.Datetime(unit, \"UTC\"))")
	}
	tz, ok := callLitString(c, 1)
	if !ok || tz == "" {
		return dtype.Null, uerr.New(uerr.KindValue, "dt",
			"%s needs a time zone, such as \"Europe/London\"", c.Fn)
	}
	if tz != "UTC" {
		if _, err := dtype.Location(tz); err != nil {
			return dtype.Null, uerr.New(uerr.KindValue, "dt",
				"the time zone %q is not known: %v", tz, err)
		}
	}
	return dtype.Datetime(in.TimeUnit(), tz), nil
}

// The policies replace_time_zone takes for a wall clock its new zone reads twice,
// in a fall-back fold, or never, in a spring-forward gap: Polars' own words, which
// the plan carries as its third and fourth args.
const (
	ZoneRaise    = "raise"    // refuse the query, naming the wall clock; the default
	ZoneEarliest = "earliest" // a fold's first reading, at the offset before it
	ZoneLatest   = "latest"   // a fold's second reading
	ZoneNull     = "null"     // a null
)

// Rolls are add_business_days' answers to a value not on a business day, in Polars'
// words (step 272).
const (
	RollRaise    = "raise"
	RollForward  = "forward"
	RollBackward = "backward"
)

// replaceZoneOut types replace_time_zone: a Datetime, naive or zoned, keeps its unit
// and takes the zone its first arg names, "" making it naive. The zone and both
// policies are checked here, so a query that names an unknown one is refused while
// it is planned.
func replaceZoneOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	if in.ID() != dtype.TypeDatetime {
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime operand, got %s", c.Fn, in)
	}
	tz, ok := callLitString(c, 1)
	if !ok {
		return dtype.Null, uerr.New(uerr.KindValue, "dt",
			"%s needs a time zone, such as \"Europe/London\", or \"\" for none", c.Fn)
	}
	if tz != "" && tz != "UTC" {
		if _, err := dtype.Location(tz); err != nil {
			return dtype.Null, uerr.New(uerr.KindValue, "dt",
				"the time zone %q is not known: %v", tz, err)
		}
	}
	for i, allowed := range [][]string{
		{ZoneRaise, ZoneEarliest, ZoneLatest, ZoneNull}, // ambiguous
		{ZoneRaise, ZoneNull},                           // non_existent
	} {
		p, ok := callLitString(c, 2+i)
		if !ok || !slices.Contains(allowed, p) {
			return dtype.Null, uerr.New(uerr.KindValue, "dt",
				"%s's %s policy is %q; it is one of %q", c.Fn,
				[]string{"ambiguous", "non_existent"}[i], p, allowed)
		}
	}
	return dtype.Datetime(in.TimeUnit(), tz), nil
}
