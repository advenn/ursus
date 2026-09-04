package dtype

import (
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
func (d DataType) FromTime(t time.Time) (int64, bool) {
	npt, ok := d.NanosPerTick()
	if !ok || d.ID() == TypeDuration {
		return 0, false
	}
	if npt >= int64(time.Second) {
		// Date, and second-resolution instants: derive from Unix seconds so that
		// dates far from the epoch do not route through a nanosecond count.
		per := npt / int64(time.Second)
		sec := t.Unix()
		q := sec / per
		if sec%per != 0 && sec < 0 {
			q--
		}
		return q, true
	}
	ns := t.UnixNano()
	q := ns / npt
	if ns%npt != 0 && ns < 0 {
		q--
	}
	return q, true
}

// ToDuration converts a stored tick count of a Duration type into a
// time.Duration. Reports false for any other type.
func (d DataType) ToDuration(ticks int64) (time.Duration, bool) {
	if d.ID() != TypeDuration {
		return 0, false
	}
	npt, _ := d.NanosPerTick()
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
		dur, _ := d.ToDuration(ticks)
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
