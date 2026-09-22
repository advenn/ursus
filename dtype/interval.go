package dtype

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/advenn/ursus/internal/uerr"
)

// Interval is a calendar-aware span of time.
//
// # Why time.Duration is not enough
//
// A Duration is a fixed number of nanoseconds. "1 month" is not: it is 28, 29, 30 or
// 31 days depending on where you start. Neither is "1 day", in any zone with daylight
// saving — it is 23, 24 or 25 hours depending on the date. A type that stores only
// nanoseconds cannot express either, so it cannot express the windows a temporal
// group-by is asked for.
//
// # Three fields, and they are not interchangeable
//
// months, days and nanos are stored separately and applied in that order, because
// each obeys a different rule:
//
//	months  calendar arithmetic with END-OF-MONTH CLAMPING
//	days    calendar days — 23, 24 or 25 hours across a DST boundary
//	nanos   absolute elapsed time
//
// So Every("1d") is NOT FromDuration(24*time.Hour), and the difference is visible on
// exactly two days a year in any DST zone. Storing 1 day as 86,400,000,000,000 nanos
// would collapse the distinction at construction time, where it can never be
// recovered. This is Arrow's month-day-nano interval layout, chosen for the same
// reason.
//
// # Errors are carried, not returned
//
// Every("banana") has to do something, and returning (Interval, error) would break
// the one call shape the API is for — an option struct field. So a parse failure is
// carried in the value and surfaced when the query is built, which is the deferred
// error rule the rest of the library already follows.
type Interval struct {
	months int32
	days   int32
	nanos  int64
	err    error
}

// Months, Days and Nanos are the three components. Exported as accessors rather than
// fields so the zero value stays meaningful and nobody constructs a half-valid
// Interval that skipped Every's validation.
func (i Interval) Months() int32 { return i.months }
func (i Interval) Days() int32   { return i.days }
func (i Interval) Nanos() int64  { return i.nanos }

// Err reports a parse failure from Every. Nil for every Interval built any other way.
func (i Interval) Err() error { return i.err }

// IsZero reports whether the interval spans nothing.
func (i Interval) IsZero() bool { return i.months == 0 && i.days == 0 && i.nanos == 0 }

// IsCalendar reports whether the interval's length depends on WHERE IT STARTS.
//
// True for months and for days, and the days half is the part that is easy to get
// wrong: a day is a calendar day, so in a DST zone it is not a fixed span either.
// Anything built from a Duration is never calendar.
func (i Interval) IsCalendar() bool { return i.months != 0 || i.days != 0 }

// Negative reports whether the interval runs backwards.
func (i Interval) Negative() bool {
	return i.months < 0 || i.days < 0 || i.nanos < 0
}

// Neg reverses the interval. Used to walk a window grid backwards and to turn a
// window's width into the offset from its end to its start.
//
// Total, with no check, and that rests on an invariant rather than on luck: no
// interval holds a component at its type's minimum. Every bounds its accumulators
// by each type's MAXIMUM, so a negated total lands in [-max, 0]; IntervalOf and
// FromDuration refuse the minimum outright. A guard here could not fire, and this
// codebase treats an unreachable guard as protection that is not there.
func (i Interval) Neg() Interval {
	return Interval{months: -i.months, days: -i.days, nanos: -i.nanos, err: i.err}
}

// FromDuration builds an absolute interval. The result is never calendar-aware —
// 24*time.Hour is twenty-four hours, not one day.
//
// The single most negative Duration is refused, and it is the only one. MinInt64 has
// no negation, so an interval holding it would be its own Neg() — see the invariant
// on Neg. Refusing the one value keeps that invariant true of every interval that
// exists, which is worth more than a Duration nobody means: it is about -292 years,
// and every consumer refuses it as negative anyway.
func FromDuration(d time.Duration) Interval {
	if int64(d) == math.MinInt64 {
		return Interval{err: uerr.New(uerr.KindValue, "interval",
			"a duration of %d nanoseconds has no negation", int64(d)).
			Hint("the most negative Duration cannot be reversed; use -(MaxInt64) " +
				"if a bound is what you want")}
	}
	return Interval{nanos: int64(d)}
}

// IntervalOf rebuilds an interval from its three components.
//
// It exists for the kernel. An expression argument travels as a *expr.Lit and
// litreflect only builds those from primitive kinds, so an Interval crosses that
// boundary as three plain integers and is reassembled here — rather than teaching the
// literal path about a struct type, which would be a second construction route for a
// value whose whole point is that its three fields are not interchangeable.
func IntervalOf(months, days int32, nanos int64) Interval {
	// Two invariants, and this is the only construction route that could break
	// them, now that Every bounds its accumulation and the two unvalidated
	// helpers beside this one are gone.
	//
	// No component at its type's minimum, so every interval can be negated. And no
	// MIXED sign, because Negative() is an OR over the three fields while String()
	// hoists a single leading '-' — so an interval of one month minus one day would
	// render "--1mo1d", which Every cannot read back. A mixed span is a real
	// quantity and some databases store one; it is simply not in this notation, and
	// printing it as though it were is the lie.
	if months == math.MinInt32 || days == math.MinInt32 || nanos == math.MinInt64 {
		return Interval{err: uerr.New(uerr.KindValue, "interval",
			"interval component {%d, %d, %d} is at its type's minimum and has no "+
				"negation", months, days, nanos)}
	}
	pos := months > 0 || days > 0 || nanos > 0
	neg := months < 0 || days < 0 || nanos < 0
	if pos && neg {
		return Interval{err: uerr.New(uerr.KindValue, "interval",
			"interval {%d months, %d days, %d nanoseconds} mixes signs",
			months, days, nanos).
			Hint("an interval runs one way; build the two parts separately if you " +
				"need to add one and subtract the other")}
	}
	return Interval{months: months, days: days, nanos: nanos}
}

// intervalUnits is ordered LONGEST FIRST, because the units are prefixes of each
// other: "mo" must be tried before "m", or three months parses as three minutes and
// the query silently aggregates over the wrong window.
var intervalUnits = []struct {
	suffix string
	months int32
	days   int32
	nanos  int64
}{
	{"ns", 0, 0, 1},
	{"us", 0, 0, int64(time.Microsecond)},
	{"ms", 0, 0, int64(time.Millisecond)},
	{"mo", 1, 0, 0},
	{"y", 12, 0, 0},
	{"q", 3, 0, 0},
	{"w", 0, 7, 0},
	{"d", 0, 1, 0},
	{"h", 0, 0, int64(time.Hour)},
	{"m", 0, 0, int64(time.Minute)},
	{"s", 0, 0, int64(time.Second)},
}

// Every parses an interval string: "1y", "3mo", "1w", "2d", "6h", "15m", "30s",
// "1ns". Components may be concatenated — "1h30m" — and a leading "-" negates the
// whole thing.
//
// Units: ns, us, ms, s, m, h, d, w, mo, q, y. Note "m" is MINUTES and "mo" is months,
// which is the one ambiguity in the notation and the reason the unit table is ordered
// longest-first.
func Every(s string) Interval {
	raw := s
	var out Interval
	neg := false
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		neg, s = true, rest
	}
	if s == "" {
		return Interval{err: everyErr(raw, "it is empty")}
	}

	for s != "" {
		// A number, then a unit. Both must be present, and the number must be
		// non-negative — the sign belongs to the whole interval, not to a component,
		// so "1h-30m" is a typo rather than thirty minutes before the hour.
		n := 0
		for n < len(s) && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		if n == 0 {
			return Interval{err: everyErr(raw, "%q does not begin with a number", s)}
		}
		v, err := strconv.ParseInt(s[:n], 10, 32)
		if err != nil {
			return Interval{err: everyErr(raw, "%q is not a 32-bit count", s[:n])}
		}
		s = s[n:]

		u, ok := cutUnit(s)
		if !ok {
			if s == "" {
				return Interval{err: everyErr(raw, "%d has no unit", v)}
			}
			return Interval{err: everyErr(raw, "%q is not a unit", s)}
		}
		s = s[len(u.suffix):]

		months, ok := addMul(int64(out.months), v, int64(u.months), math.MaxInt32)
		if !ok {
			return Interval{err: everyErr(raw, "%d%s overflows the month count",
				v, u.suffix)}
		}
		days, ok := addMul(int64(out.days), v, int64(u.days), math.MaxInt32)
		if !ok {
			return Interval{err: everyErr(raw, "%d%s overflows the day count",
				v, u.suffix)}
		}
		nanos, ok := addMul(out.nanos, v, u.nanos, math.MaxInt64)
		if !ok {
			return Interval{err: everyErr(raw, "%d%s overflows the nanosecond count",
				v, u.suffix)}
		}
		out.months, out.days, out.nanos = int32(months), int32(days), nanos
	}

	// No overflow check on the negation, and that is not an omission. addMul bounds
	// every component to [0, limit] where limit is the type's MAXIMUM, so negating
	// lands in [-limit, 0] and can never reach MinInt32 or MinInt64 — the one place
	// negation is a fixed point. Bounding the accumulation is what makes this line
	// safe, which is why the bound is stated as the max rather than the min.
	if neg {
		out.months, out.days, out.nanos = -out.months, -out.days, -out.nanos
	}
	if out.IsZero() {
		return Interval{err: everyErr(raw, "it spans no time")}
	}
	return out
}

// addMul returns acc + v*mult, or false if that leaves [0, limit].
//
// Every term is non-negative — the parser takes digits only, and the sign belongs to
// the whole interval rather than to a component — so one comparison bounds the
// product and one bounds the sum, with neither able to overflow on the way. The
// multiply and the add are SEPARATE failures: "178956970y" fits and "178956970y" a
// second time does not, which a check on the product alone would miss.
//
// limit is the type's maximum rather than its minimum, deliberately: that is what
// keeps the negation at the end of Every a total operation. See the comment there.
func addMul(acc, v, mult, limit int64) (int64, bool) {
	if mult == 0 {
		return acc, true
	}
	if v > limit/mult { // floor division, so this is exactly v*mult > limit
		return 0, false
	}
	p := v * mult
	if acc > limit-p {
		return 0, false
	}
	return acc + p, true
}

func cutUnit(s string) (struct {
	suffix string
	months int32
	days   int32
	nanos  int64
}, bool) {
	for _, u := range intervalUnits {
		if strings.HasPrefix(s, u.suffix) {
			return u, true
		}
	}
	return intervalUnits[0], false
}

func everyErr(raw, format string, args ...any) error {
	return uerr.New(uerr.KindValue, "every",
		"cannot parse interval %q: "+format, append([]any{raw}, args...)...).
		Hint(`an interval is a count and a unit, e.g. "1h", "15m", "3mo", "1y"`).
		Hint(`units are ns, us, ms, s, m (minutes), h, d, w, mo (months), q, y`)
}

// String renders the interval in Every's own notation, so String and Every round-trip
// over every interval that can be built.
//
// It hoists ONE leading '-' and then negates all three components, which is correct
// because no interval mixes signs — IntervalOf refuses that pair, and Every cannot
// produce it since the parser gives the sign to the whole interval rather than to a
// component. And the negation cannot be a fixed point, by the invariant on Neg.
// Both halves used to be false: IntervalOf(1, -1, 0) rendered "--1mo1d", and a
// count that wrapped to MinInt32 months rendered "--178956970y-8mo".
func (i Interval) String() string {
	if i.err != nil {
		return "<invalid>"
	}
	if i.IsZero() {
		return "0s"
	}
	months, days, nanos := i.months, i.days, i.nanos
	var b strings.Builder
	if i.Negative() {
		b.WriteByte('-')
		months, days, nanos = -months, -days, -nanos
	}
	write := func(v int64, unit string) {
		if v != 0 {
			b.WriteString(strconv.FormatInt(v, 10))
			b.WriteString(unit)
		}
	}
	write(int64(months/12), "y")
	write(int64(months%12), "mo")
	write(int64(days/7), "w")
	write(int64(days%7), "d")
	write(nanos/int64(time.Hour), "h")
	nanos %= int64(time.Hour)
	write(nanos/int64(time.Minute), "m")
	nanos %= int64(time.Minute)
	write(nanos/int64(time.Second), "s")
	nanos %= int64(time.Second)
	write(nanos/int64(time.Millisecond), "ms")
	nanos %= int64(time.Millisecond)
	write(nanos/int64(time.Microsecond), "us")
	write(nanos%int64(time.Microsecond), "ns")
	return b.String()
}

// --- arithmetic -------------------------------------------------------------------

// AddTo advances t by the interval, in t's own location.
//
// The three components are applied in order — months, then days, then nanos — and the
// order is part of the contract, not an implementation detail. Adding one month and
// one day to January 31st gives February 29th then March 1st in a leap year; the other
// order gives February 1st then March 1st. Both are defensible; only one can be the
// answer, and this is the one Arrow, Polars and PostgreSQL all give.
func (i Interval) AddTo(t time.Time) time.Time {
	if i.months != 0 {
		t = addMonths(t, int(i.months))
	}
	if i.days != 0 {
		// AddDate, not a nanosecond count. It keeps the WALL CLOCK, so crossing a
		// daylight-saving boundary moves the instant by 23 or 25 hours and midnight
		// stays midnight — which is what a calendar day means.
		t = t.AddDate(0, 0, int(i.days))
	}
	if i.nanos != 0 {
		t = t.Add(time.Duration(i.nanos))
	}
	return t
}

// addMonths advances by whole months, CLAMPING to the end of the target month.
//
// This is the one piece that cannot delegate to the standard library. time.Date
// NORMALISES an out-of-range day — time.Date(2024, 2, 31, …) is March 2nd — so
// AddDate(0, 1, 0) on January 31st answers March 2nd, skipping February entirely.
// Every calendar system in use clamps instead: January 31st plus one month is the last
// day of February.
//
// It is also the single most common bug in date arithmetic, which is why it gets a
// differential test against a hand-written table rather than against AddDate.
func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	total := (y*12 + int(m) - 1) + n
	ny, nm := floorDiv(total, 12), floorMod(total, 12)+1
	if last := daysInMonth(ny, time.Month(nm)); d > last {
		d = last
	}
	hh, mm, ss := t.Clock()
	return time.Date(ny, time.Month(nm), d, hh, mm, ss, t.Nanosecond(), t.Location())
}

// daysInMonth is the length of a month, leap years included.
//
// Derived from time.Date rather than from a table plus a leap-year rule: day 0 of
// month m+1 is the last day of month m, and the standard library owns the calendar.
// A table would be a second copy of a rule that already exists.
func daysInMonth(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// secondsPerDay is exact for a UTC midnight, which is the only place it is used:
// a UTC midnight's Unix time is always a whole multiple of it, so the division that
// derives a day number is exact on both sides of the epoch. It is deliberately NOT
// used to advance a local instant, where a day is 23, 24 or 25 hours.
const secondsPerDay = 24 * 60 * 60

// floorDiv and floorMod divide toward NEGATIVE INFINITY rather than toward zero.
//
// Go's / and % truncate, so -1/12 is 0 and -1%12 is -1 — which would put January of
// year -1 into year 0, month 0. Every window grid in this package is anchored at an
// epoch and must stay monotone on both sides of it, which is the same reason
// FromTime and truncateTemporal floor rather than truncate.
func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func floorMod(a, b int) int {
	r := a % b
	if r != 0 && (r < 0) != (b < 0) {
		r += b
	}
	return r
}

// TruncateTo floors t to the start of the window containing it, on a grid of this
// interval anchored at the Unix epoch, in t's own location.
//
// # Local, not UTC
//
// The grid is built from wall-clock components, so truncating by "1d" in
// America/New_York gives local midnight rather than 00:00 UTC. That is what makes
// Every("1d") different from FromDuration(24*time.Hour), which floors the absolute
// instant and is left alone precisely because a Duration IS absolute.
//
// # Mixed intervals are refused by the caller
//
// An interval with two non-zero components has no well-defined grid — "every 1 month
// and 3 days" does not tile the line — so a window grid must be built from exactly
// one component. GroupByDynamic rejects the rest; this function uses the coarsest
// component present so it is total rather than panicking on input it cannot get.
func (i Interval) TruncateTo(t time.Time) time.Time {
	switch {
	case i.months != 0:
		y, m, _ := t.Date()
		total := y*12 + int(m) - 1
		start := int(i.months) * floorDiv(total, int(i.months))
		return time.Date(floorDiv(start, 12), time.Month(floorMod(start, 12)+1), 1,
			0, 0, 0, 0, t.Location())

	case i.days != 0:
		// The day NUMBER is computed on the calendar, in UTC, and only then read back
		// as a local date. Subtracting two local midnights and dividing by 24h does
		// NOT work: across a daylight-saving boundary the elapsed time is a whole
		// number of days minus an hour, so the quotient is one too small for every
		// date after the transition. That bug is invisible for eight months a year.
		y, m, d := t.Date()
		day := int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / secondsPerDay)
		start := int(i.days) * floorDiv(day, int(i.days))
		sy, sm, sd := time.Unix(int64(start)*secondsPerDay, 0).UTC().Date()
		return time.Date(sy, sm, sd, 0, 0, 0, 0, t.Location())

	default:
		// A pure nanosecond grid: floor the absolute instant, which needs no zone.
		ns := t.UnixNano()
		q := ns / i.nanos
		if ns%i.nanos != 0 && (ns < 0) != (i.nanos < 0) {
			q--
		}
		return time.Unix(0, q*i.nanos).In(t.Location())
	}
}
