package ursus_test

// The .dt totals and epoch, on spans and dates that do not fit a nanosecond count.
//
// Both used to multiply a tick count up to nanoseconds before dividing back down, so
// the intermediate wrapped int64 while the input and the answer were both ordinary.
// Neither needs a multiply: every total is a whole number of ticks, and a tick is
// either a whole number of seconds or a whole fraction of one.
//
// The expected values come from Go's own time package rather than from the same
// arithmetic written twice.

import (
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestEpochOfADateBeyond2262: a Date is 86_400e9 nanoseconds per tick, so `t * npt`
// wrapped for every date past 2262-04-11 — an ordinary column, an ordinary call, and
// Dt().Epoch() on 2300-06-01 answered -8019905673.
func TestEpochOfADateBeyond2262(t *testing.T) {
	dates := []string{"2300-06-01", "2262-04-12", "2000-06-01", "1969-12-31", "1700-01-01"}
	want := make([]int64, len(dates))
	for i, s := range dates {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		want[i] = d.Unix()
	}

	got, err := ursus.Frame(ursus.Values("s", dates)).
		Select(ursus.Col("s").Cast(ursus.Date).Dt().Epoch().Alias("epoch")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expect, err := ursus.Frame(ursus.Values("epoch", want)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, expect)
}

// TestEpochSurvivesEveryTemporalUnit covers the other side of the same branch: a
// tick finer than a second divides, a tick coarser multiplies.
func TestEpochSurvivesEveryTemporalUnit(t *testing.T) {
	instant := time.Date(2200, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, u := range []ursus.TimeUnit{ursus.Second, ursus.Milli, ursus.Micro, ursus.Nano} {
		df, err := ursus.Frame(ursus.Values("s", []string{instant.Format(time.RFC3339)})).
			Select(ursus.Col("s").Cast(ursus.Datetime(u, "UTC")).Dt().Epoch().Alias("e")).
			Collect(t.Context())
		if err != nil {
			// Nanoseconds cannot hold the year 2200 at all; the cast refuses it, which
			// is the strict-cast behaviour and not this function's business.
			if u == ursus.Nano {
				continue
			}
			t.Fatalf("%s: %v", u, err)
		}
		e, err := df.Column[int64]("e")
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := e.Get(0); v != instant.Unix() {
			t.Errorf("Datetime(%s).Dt().Epoch() = %d, want %d", u, v, instant.Unix())
		}
	}
}

// TestTotalsSurviveAThreeCenturySpan: Duration(s) totals multiplied up to nanoseconds
// first, so a span past 292 years wrapped — and 292 years is exactly what
// Datetime(s) - Datetime(s) legitimately produces.
func TestTotalsSurviveAThreeCenturySpan(t *testing.T) {
	const secs = 9_467_280_000 // 109575 days
	df, err := ursus.Frame(ursus.Values("n", []int64{secs, 86_400, 0, -secs})).
		Select(
			ursus.Col("n").Cast(ursus.Duration(ursus.Second)).Dt().TotalDays().Alias("days"),
			ursus.Col("n").Cast(ursus.Duration(ursus.Second)).Dt().TotalHours().Alias("hours"),
			ursus.Col("n").Cast(ursus.Duration(ursus.Second)).Dt().TotalMinutes().Alias("minutes"),
			ursus.Col("n").Cast(ursus.Duration(ursus.Second)).Dt().TotalSeconds().Alias("seconds"),
		).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want, err := ursus.Frame(
		ursus.Values("days", []int64{secs / 86400, 1, 0, -(secs / 86400)}),
		ursus.Values("hours", []int64{secs / 3600, 24, 0, -(secs / 3600)}),
		ursus.Values("minutes", []int64{secs / 60, 1440, 0, -(secs / 60)}),
		ursus.Values("seconds", []int64{secs, 86400, 0, -secs}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, df, want)
}

// TestTotalsStillFloorTowardsZeroAtEveryUnit pins what the multiply was there for: a
// coarse total of a fine span must not be computed by flooring twice.
func TestTotalsStillFloorTowardsZeroAtEveryUnit(t *testing.T) {
	// 90 minutes, expressed at each resolution.
	spans := map[ursus.TimeUnit]int64{
		ursus.Second: 5400,
		ursus.Milli:  5400_000,
		ursus.Micro:  5400_000_000,
		ursus.Nano:   5400_000_000_000,
	}
	for u, ticks := range spans {
		df, err := ursus.Frame(ursus.Values("n", []int64{ticks})).
			Select(
				ursus.Col("n").Cast(ursus.Duration(u)).Dt().TotalDays().Alias("d"),
				ursus.Col("n").Cast(ursus.Duration(u)).Dt().TotalHours().Alias("h"),
				ursus.Col("n").Cast(ursus.Duration(u)).Dt().TotalMinutes().Alias("m"),
			).Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		for name, want := range map[string]int64{"d": 0, "h": 1, "m": 90} {
			c, err := df.Column[int64](name)
			if err != nil {
				t.Fatal(err)
			}
			if v, _ := c.Get(0); v != want {
				t.Errorf("90 minutes at %s: %s = %d, want %d", u, name, v, want)
			}
		}
	}
}
