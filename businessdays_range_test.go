package ursus_test

// Step 419: a business day's Add past any Date or Datetime is refused, where the
// week count wrapped and answered a wrong date.

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// TestAddBusinessDaysPastTheRangeIsRefused: a count of 10^18 business days from
// Monday 2024-01-01 wrapped d+7w and answered a Datetime of 2023-12-31T04:44:24, a
// silent wrong answer; past any Date or Datetime it is now refused, as a Date's add
// past its range already was, forward and back, for a Date and a Datetime of each
// unit. Polars 1.44.1 answers null (D10: ursus refuses, as for a Date). Five business
// days still land on the next Monday.
func TestAddBusinessDaysPastTheRangeIsRefused(t *testing.T) {
	mon := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	cols := map[string]ursus.Expr{
		"Date":         ursus.Col("t").Cast(ursus.Date),
		"Datetime(ns)": ursus.Col("t"),
		"Datetime(ms)": ursus.Col("t").Cast(dtype.Datetime(dtype.Milli, "")),
	}
	for name, col := range cols {
		for _, n := range []int64{1e18, -1e18, math.MaxInt64, math.MinInt64 + 1, 1 << 41} {
			_, err := ursus.Frame(ursus.Values("t", []time.Time{mon})).
				Select(col.Dt().AddBusinessDays(n, ursus.RollForward)).Collect(t.Context())
			if err == nil || !errors.Is(err, ursus.ErrValue) {
				t.Errorf("%s + %d business days: got %v, want refused", name, n, err)
			}
		}
		df, err := ursus.Frame(ursus.Values("t", []time.Time{mon})).
			Select(col.Dt().AddBusinessDays(5, ursus.RollForward).Cast(ursus.Date).Cast(ursus.String)).Collect(t.Context())
		if err != nil {
			t.Fatalf("%s + 5: %v", name, err)
		}
		s, _ := df.Column[string]("t")
		if v, _ := s.Get(0); v != "2024-01-08" {
			t.Errorf("%s + 5 business days: got %q, want 2024-01-08", name, v)
		}
	}
}
