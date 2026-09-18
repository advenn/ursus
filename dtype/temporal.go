package dtype

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// The one place that converts between a stored tick count and Go's time package.
//
// Four things need this conversion — rendering a frame, the .dt namespace, the CSV
// reader and the CSV writer — and step 2's audit found the running-schema walk had
// been written four times with two copies already diverged. So it is written once,
// here, at the level both dtype's users and the kernels can reach.

// ToTime converts a stored tick count of type d into a time.Time in UTC.
//
// Date is days since the epoch; Datetime and Time are tick counts at d's unit.
// Duration is NOT an instant and is rejected — use ToDuration.
func (d DataType) ToTime(ticks int64) (time.Time, bool) {
	npt, ok := d.NanosPerTick()
	if !ok || d.ID() == TypeDuration {
		return time.Time{}, false
	}
	// Split before multiplying so a nanosecond-resolution instant far from the
	// epoch does not overflow: seconds and nanos are accumulated separately.
	perSec := int64(time.Second) / npt
	if perSec == 0 {
		// Coarser than a second — only Date, whose tick is a whole day.
		return time.Unix(ticks*(npt/int64(time.Second)), 0).UTC(), true
	}
	sec, rem := ticks/perSec, ticks%perSec
	if rem < 0 {
		sec--
		rem += perSec
	}
	return time.Unix(sec, rem*npt).UTC(), true
}

// FromTime converts an instant into a tick count of type d, flooring toward
// negative infinity so the mapping stays monotone across the epoch.
//
// It reports false for an instant d cannot hold, and callers must honour that. The
// fine-unit branch used to call t.UnixNano(), whose result Go documents as UNDEFINED
// before 1678 or after 2262, and then returned true regardless — so every caller that
// dutifully checked the flag was told a wrapped value was fine, and 2300-06-01 became
// 1715-11-11T00:25:26.290448384Z through Values, through Lit and through a cast.
//
// Deriving the ticks from Unix SECONDS rather than from a nanosecond count also
// widens what actually fits: a millisecond instant in the year 3000 is an ordinary
// tick count and only the intermediate was ever the problem.
func (d DataType) FromTime(t time.Time) (int64, bool) {
	npt, ok := d.NanosPerTick()
	if !ok || d.ID() == TypeDuration {
		return 0, false
	}
	const nsPerSec = int64(time.Second)
	sec := t.Unix()

	if npt >= nsPerSec {
		// Date, and second-resolution instants: whole seconds per tick.
		per := npt / nsPerSec
		q := sec / per
		if sec%per != 0 && sec < 0 {
			q--
		}
		return q, true
	}

	// Whole ticks per second, so the instant is sec*perSec plus the sub-second part.
	// Only the multiply can leave int64, and it is checked rather than assumed.
	perSec := nsPerSec / npt
	if sec > math.MaxInt64/perSec || sec < math.MinInt64/perSec {
		return 0, false
	}
	ticks := sec * perSec
	sub := int64(t.Nanosecond()) / npt // Nanosecond() is in [0, 1e9), so this floors
	if ticks > math.MaxInt64-sub {
		return 0, false
	}
	return ticks + sub, true
}

// TicksPerDay is 24 hours expressed in d's own unit.
//
// It is the modulus that makes a Time a time of day, and it lives here so there is
// one definition of a day: the arithmetic kernel that wraps, the cast that
// normalises, and data's boundary check all ask this rather than each deriving it.
// Two copies of a modulus is two chances to disagree about whether midnight belongs
// to today or tomorrow.
//
// Reports false for a type with no tick length. Date has one — 24h exactly — so this
// answers 1 for it, which is true and useless; callers gate on TypeTime.
func TicksPerDay(d DataType) (int64, bool) {
	npt, ok := d.NanosPerTick()
	if !ok || npt <= 0 {
		return 0, false
	}
	return int64(24*time.Hour) / npt, true
}

// ToDuration converts a stored tick count of a Duration type into a
// time.Duration. Reports false for any other type, and for a span time.Duration
// cannot hold.
//
// # The unrepresentable case is not hypothetical
//
// `ticks * npt` was an unchecked int64 multiply five lines below ToTime, which
// documents and implements exactly this defence. A time.Duration IS int64
// nanoseconds, so it spans about 292 years — and a Duration(Second) column reaches
// that at 9.2e9 ticks, which `Datetime - Datetime` produces from two instants three
// centuries apart. The product wrapped and the span rendered NEGATIVE.
//
// Reporting false rather than saturating, because FormatTemporal's fallback is the
// raw tick count: an integer a reader can see is unusual is a better answer than a
// duration that looks ordinary and has the wrong sign.
func (d DataType) ToDuration(ticks int64) (time.Duration, bool) {
	if d.ID() != TypeDuration {
		return 0, false
	}
	npt, ok := d.NanosPerTick()
	if !ok || npt <= 0 {
		return 0, false
	}
	if ticks > math.MaxInt64/npt || ticks < math.MinInt64/npt {
		return 0, false
	}
	return time.Duration(ticks * npt), true
}

// FormatTemporal renders one stored tick count of type d.
//
// The formats are ISO 8601, which is what every other tool in the ecosystem reads
// and what the CSV writer emits — so a frame printed to a terminal and a frame
// written to a file agree, and neither shows the raw integer that this function
// exists to stop appearing.
func FormatTemporal(d DataType, ticks int64) string {
	switch d.ID() {
	case TypeDate:
		t, ok := d.ToTime(ticks)
		if !ok {
			return strconv.FormatInt(ticks, 10)
		}
		return t.Format("2006-01-02")

	case TypeTime:
		t, ok := d.ToTime(ticks)
		if !ok {
			return strconv.FormatInt(ticks, 10)
		}
		return t.Format(timeLayout(d.TimeUnit()))

	case TypeDatetime:
		t, ok := d.ToTime(ticks)
		if !ok {
			return strconv.FormatInt(ticks, 10)
		}
		if tz := d.TimeZone(); tz != "" && tz != "UTC" {
			if loc, err := time.LoadLocation(tz); err == nil {
				t = t.In(loc)
			}
		}
		return t.Format("2006-01-02T15:04:05" + fracLayout(d.TimeUnit()) + "Z07:00")

	case TypeDuration:
		dur, ok := d.ToDuration(ticks)
		if !ok {
			// Past time.Duration's ~292-year span. The raw tick count with its unit
			// is unusual enough to read as unusual, which a wrapped negative span is
			// not. The `_` this used to discard is why it rendered as one.
			return strconv.FormatInt(ticks, 10) + d.TimeUnit().String()
		}
		return dur.String()

	default:
		return strconv.FormatInt(ticks, 10)
	}
}

// timeLayout is a wall-clock layout at the given resolution.
func timeLayout(u TimeUnit) string { return "15:04:05" + fracLayout(u) }

// fracLayout is the fractional-seconds part of a layout. Go spells a fixed-width
// fraction with zeros, so a millisecond column always shows three digits — which
// makes the unit visible in the output rather than something to infer.
func fracLayout(u TimeUnit) string {
	switch u {
	case Milli:
		return ".000"
	case Micro:
		return ".000000"
	case Nano:
		return ".000000000"
	default:
		return ""
	}
}

// ParseTemporal reads a textual instant into a tick count of type d.
//
// It accepts the layouts FormatTemporal produces, plus the common relaxations:
// a space instead of the T separator, and a missing timezone (read as UTC). That
// asymmetry is deliberate — a writer should be strict and a reader forgiving,
// because the reader is the one handed files it did not create.
func ParseTemporal(d DataType, s string) (int64, bool) {
	switch d.ID() {
	case TypeDate:
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return 0, false
		}
		return d.FromTime(t)

	case TypeTime:
		for _, layout := range []string{"15:04:05.999999999", "15:04:05", "15:04"} {
			if t, err := time.Parse(layout, s); err == nil {
				return d.FromTime(t.AddDate(1970, 0, 0))
			}
		}
		return 0, false

	case TypeDatetime:
		for _, layout := range datetimeLayouts {
			if t, err := time.Parse(layout, s); err == nil {
				return d.FromTime(t)
			}
		}
		return 0, false

	case TypeDuration:
		dur, err := time.ParseDuration(s)
		if err != nil {
			return 0, false
		}
		npt, _ := d.NanosPerTick()
		return int64(dur) / npt, true

	default:
		return 0, false
	}
}

// datetimeLayouts is ordered most-specific first, so a string carrying an offset
// is not silently read as naive UTC.
var datetimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// LooksTemporal reports whether s could be one of the layouts ParseTemporal
// accepts for d. Used by CSV schema inference, which must be conservative: a
// column of bare integers like 20240101 must NOT become dates.
func LooksTemporal(d DataType, s string) bool {
	if len(s) < 8 || !strings.ContainsAny(s, "-:") {
		// Every accepted instant layout carries a separator. Requiring one is what
		// stops an integer column being inferred as temporal.
		return false
	}
	_, ok := ParseTemporal(d, s)
	return ok
}
