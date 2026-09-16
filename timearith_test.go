package ursus_test

// Time arithmetic, the second producer of a Time value.
//
// Every assertion here is about AGREEMENT BETWEEN CONSUMERS, because each consumer is
// individually plausible today and only the contradiction between them is visible.
// Measured before the fix:
//
//	23:00 + 2h    printed 01:00:00, sorted AFTER 14:00:00, compared UNEQUAL to 01:00
//	01:00 - 2h    printed 23:00:00, compared UNEQUAL to 23:00
//	+106751 days  gave 00:15:26.290448384 instead of 23:50:00
//
// The last one is the reason the wrap has its own kernel arm rather than a pass over
// arithNum's output: the sum had already wrapped int64 before any modulo could run.

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestTimePrintsSortsAndComparesConsistently needs all three assertions, because
// fixing any one alone leaves the defect: a genuine 01:00:00 and a rolled-over one
// rendered identically and compared unequal, so a group_by produced two groups with
// the same label.
func TestTimePrintsSortsAndComparesConsistently(t *testing.T) {
	lf := clock("t", []int64{at2300}).
		Select(ursus.Col("t").Add(2 * time.Hour).Alias("t"))

	t.Run("prints as 01:00:00", func(t *testing.T) {
		df, err := lf.Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		if got := df.String(); !strings.Contains(got, "01:00:00") {
			t.Errorf("23:00 + 2h does not render as 01:00:00:\n%s", got)
		}
	})

	t.Run("compares equal to a real 01:00", func(t *testing.T) {
		df, err := lf.Select(
			ursus.Col("t").Eq(ursus.Lit(at0100).Cast(ursus.TimeOf(ursus.Second))).
				Alias("same"),
		).Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		v, err := df.Column[bool]("same")
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := v.Get(0); !ok || !got {
			t.Errorf("23:00 + 2h does not compare equal to 01:00:00 — it renders "+
				"identically and compares unequal, so a group_by makes two groups "+
				"with one label (got %v, valid %v)", got, ok)
		}
	})

	t.Run("sorts where it prints", func(t *testing.T) {
		df, err := clock("t", []int64{at2300, 12 * 3600}).
			Select(ursus.Col("t").Add(2*time.Hour).Alias("t")).
			Sort(ursus.Asc(ursus.Col("t"))).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		out := df.String()
		first, second := strings.Index(out, "01:00:00"), strings.Index(out, "14:00:00")
		if first < 0 || second < 0 {
			t.Fatalf("expected 01:00:00 and 14:00:00 in:\n%s", out)
		}
		if first > second {
			t.Errorf("ascending sort put 01:00:00 after 14:00:00 — ordering is on "+
				"the raw tick and disagrees with what is printed:\n%s", out)
		}
	})
}

// TestTimeSubtractionWrapsBackwards is the other direction, and it is the half Go's
// truncated % gets wrong on its own: 01:00 - 2h is 23:00, not a negative tick that
// prints as 23:00 by rolling backwards through the epoch.
func TestTimeSubtractionWrapsBackwards(t *testing.T) {
	df, err := clock("t", []int64{at0100}).
		Select(ursus.Col("t").Sub(2*time.Hour).Alias("t")).
		Select(ursus.Col("t").Eq(ursus.Lit(at2300).Cast(ursus.TimeOf(ursus.Second))).
			Alias("same")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	v, err := df.Column[bool]("same")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.Get(0); !ok || !got {
		t.Errorf("01:00 - 2h does not compare equal to 23:00:00 (got %v, valid %v)",
			got, ok)
	}
}

// TestTimeArithmeticSurvivesAHugeDuration is the one a modulo alone cannot satisfy.
//
// A time.Duration is itself int64 nanoseconds, so the largest one is about 106752
// days. 106751 whole days is the largest whole-day duration there is, and at
// nanosecond resolution the sum overflows int64 for any tick past roughly 23:47:17 —
// so 23:50 is the case. Adding a whole number of days must leave the time of day
// exactly where it was.
//
// Without reducing the duration first the sum wraps int64 to a large negative number
// and the modulo returns 00:15:26.290448384 — in range, plausible, and wrong. No
// range check can catch that; only doing the reduction first can.
func TestTimeArithmeticSurvivesAHugeDuration(t *testing.T) {
	const huge = 106_751 * 24 * time.Hour
	const at2350 = int64(23*3600+50*60) * int64(time.Second)

	df, err := clockAt("t", ursus.Nano, []int64{at2350}).
		Select(ursus.Col("t").Add(huge).Alias("t")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.String(); !strings.Contains(got, "23:50:00") {
		t.Errorf("adding a whole number of days changed the time of day:\n%s", got)
	}
}

// TestTimeArithmeticWrapsAtEveryUnit, because the modulus is derived from the unit
// and a unit whose ticks-per-day was computed wrongly would be wrong quietly.
func TestTimeArithmeticWrapsAtEveryUnit(t *testing.T) {
	for _, c := range []struct {
		name   string
		unit   ursus.TimeUnit
		perSec int64
	}{
		{"second", ursus.Second, 1},
		{"milli", ursus.Milli, 1_000},
		{"micro", ursus.Micro, 1_000_000},
		{"nano", ursus.Nano, 1_000_000_000},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, err := clockAt("t", c.unit, []int64{at2300 * c.perSec}).
				Select(ursus.Col("t").Add(2*time.Hour).Alias("t")).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			if got := df.String(); !strings.Contains(got, "01:00:00") {
				t.Errorf("23:00 + 2h at %s resolution:\n%s", c.name, got)
			}
		})
	}
}

// TestTimeDifferenceStaysASignedDuration guards the SCOPE of the wrap.
//
// Time - Time yields a Duration, which is signed and unbounded by design. Wrapping it
// too would be the same mistake one type over — 01:00 minus 23:00 is minus twenty-two
// hours, not two.
func TestTimeDifferenceStaysASignedDuration(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("a", []int64{at0100}),
		ursus.Values("b", []int64{at2300}),
	).
		Select(
			ursus.Col("a").Cast(ursus.TimeOf(ursus.Second)).Alias("a"),
			ursus.Col("b").Cast(ursus.TimeOf(ursus.Second)).Alias("b"),
		).
		Select(ursus.Col("a").Sub(ursus.Col("b")).Alias("d")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	v, err := df.Column[int64]("d")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.Get(0)
	if !ok {
		t.Fatal("the difference is null")
	}
	if want := at0100 - at2300; got != want {
		t.Errorf("01:00 - 23:00 = %d, want %d — a Duration is signed and must not "+
			"be wrapped into a day", got, want)
	}
}
