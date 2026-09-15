package ursus_test

// dt.truncate's refusals, from the outside.
//
// All four of these type-checked, planned and rendered in Explain before step 56, and
// then failed at Collect. The contract matrix found them because its call arm drives
// truncate with several intervals rather than one — the argument is what decides,
// and a matrix with one fixed argument per call would have seen none of them.

import (
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// timeFrame has a Time column and a Datetime column over the same instants, so every
// case below can be asked of both and the answers compared.
func timeFrame() *ursus.LazyFrame {
	ts := []time.Time{
		time.Date(2024, 3, 15, 13, 45, 30, 0, time.UTC),
		time.Date(2024, 3, 16, 9, 5, 0, 0, time.UTC),
	}
	return ursus.Frame(ursus.Values("ts", ts)).
		WithColumns(ursus.Col("ts").Cast(ursus.TimeOf(ursus.Nano)).Alias("tm"))
}

// TestTruncateByACalendarIntervalNeedsADate is the finding, and it asserts BOTH
// directions on purpose.
//
// Refusing truncate on a Time outright would pass a one-sided test and delete a
// working feature: flooring a wall clock to the hour is exactly what a Time is for.
// It is the CALENDAR grid that has no meaning without a date, which is why the
// receiver's type alone could never decide this.
func TestTruncateByACalendarIntervalNeedsADate(t *testing.T) {
	t.Run("a calendar interval on a Time is refused", func(t *testing.T) {
		assertUserError(t,
			timeFrame().Select(ursus.Col("tm").Dt().Truncate(ursus.Every("1mo"))),
			"Time")
	})

	t.Run("a sub-day interval on the same column still works", func(t *testing.T) {
		df, err := timeFrame().
			Select(ursus.Col("tm").Dt().Truncate(time.Hour).Alias("h")).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatalf("flooring a wall clock to the hour is what a Time is for: %v", err)
		}
		if df.Height() != 2 {
			t.Fatalf("height = %d, want 2", df.Height())
		}
	})

	t.Run("a calendar interval on a Datetime still works", func(t *testing.T) {
		df, err := timeFrame().
			Select(ursus.Col("ts").Dt().Truncate(ursus.Every("1mo")).Alias("m")).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatalf("a Datetime has a date, so a month grid is defined: %v", err)
		}
		if df.Height() != 2 {
			t.Fatalf("height = %d, want 2", df.Height())
		}
	})
}

// TestTruncateNeedsAPositiveInterval.
//
// Both spellings reach the builder with no error, because dtype.FromDuration does not
// validate — it is `Interval{nanos: int64(d)}` and nothing else — so DtExpr.Truncate's
// iv.Err() check passes and the refusal used to wait for the kernel.
func TestTruncateNeedsAPositiveInterval(t *testing.T) {
	for _, c := range []struct {
		name  string
		every time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertUserError(t,
				timeFrame().Select(ursus.Col("ts").Dt().Truncate(c.every)),
				"positive interval")
		})
	}
}
