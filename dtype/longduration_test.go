package dtype_test

// ToDuration's unchecked multiply, found while making Time a time of day.
//
// A time.Duration IS int64 nanoseconds, so it spans about 292 years. A
// Duration(Second) column reaches that at 9.2e9 ticks — which `Datetime - Datetime`
// produces from two instants three centuries apart, an ordinary enough query — and
// `ticks * npt` wrapped, so the span rendered NEGATIVE.
//
// It sat five lines below ToTime, which documents and implements exactly this
// defence: "Split before multiplying so a nanosecond-resolution instant far from the
// epoch does not overflow."

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
)

func TestToDurationRefusesWhatItCannotHold(t *testing.T) {
	for _, c := range []struct {
		name  string
		unit  dtype.TimeUnit
		ticks int64
		ok    bool
	}{
		// Three centuries of seconds. Representable as ticks, not as a time.Duration.
		{"seconds past the span", dtype.Second, 10_000_000_000, false},
		{"seconds inside the span", dtype.Second, 1_000_000_000, true},
		{"negative past the span", dtype.Second, -10_000_000_000, false},
		// Nanoseconds are the unit time.Duration itself uses, so every tick fits.
		{"nanoseconds always fit", dtype.Nano, math.MaxInt64, true},
		// The exact boundary, one tick either side. MaxInt64/1e6 milliseconds is the
		// largest span that fits; the engine was right and this fixture was wrong
		// about it the first time.
		{"milliseconds at the boundary", dtype.Milli, math.MaxInt64 / 1_000_000, true},
		{"milliseconds one past it", dtype.Milli, math.MaxInt64/1_000_000 + 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := dtype.Duration(c.unit).ToDuration(c.ticks)
			if ok != c.ok {
				t.Fatalf("ToDuration(%d) ok = %v, want %v (got %v)",
					c.ticks, ok, c.ok, got)
			}
			if !ok {
				return
			}
			// And when it does fit, it must be exact rather than merely non-negative.
			npt := int64(dtype.Duration(c.unit).TimeUnit().Duration())
			if want := time.Duration(c.ticks * npt); got != want {
				t.Errorf("ToDuration(%d) = %v, want %v", c.ticks, got, want)
			}
		})
	}
}

// TestFormatDurationNeverRendersAWrappedSpan is the consumer half. FormatTemporal
// discarded ToDuration's second return value, which is why an unrepresentable span
// printed as an ordinary negative one instead of as something a reader can see is
// unusual.
func TestFormatDurationNeverRendersAWrappedSpan(t *testing.T) {
	const threeCenturiesOfSeconds = 10_000_000_000

	got := dtype.FormatTemporal(dtype.Duration(dtype.Second), threeCenturiesOfSeconds)
	if strings.HasPrefix(got, "-") {
		t.Errorf("a positive span rendered as %q — the multiply wrapped", got)
	}
	// The fallback keeps the unit, so the number cannot be mistaken for nanoseconds.
	if !strings.Contains(got, "10000000000") || !strings.HasSuffix(got, "s") {
		t.Errorf("the fallback should be the raw tick count with its unit, got %q", got)
	}

	// The ordinary case is unchanged: anything time.Duration can hold still renders
	// through its String method, so this fallback cannot creep into normal output.
	if got := dtype.FormatTemporal(dtype.Duration(dtype.Second), 90); got != "1m30s" {
		t.Errorf("an ordinary span rendered as %q, want 1m30s", got)
	}
}
