package ursus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// dt.days_in_month and dt.combine (step 230). Their answers are Polars 2.1.0's,
// recorded in the dtcombine group of testdata/oracle and spelled in
// oracle_dtcombine_test.go since step 498, which took this file's values. What stays
// asserts the refusals, and holds to Go's time package the day a zoned Datetime
// combines, read on its own wall clock (Combine's doc): Polars 2.1 reads it in UTC,
// and the recorded combine_zoned_ny asks it on the wall clock (step 507; feature-gaps.md
// §9).

// nyWall is a one-row frame ts of the wall clock s in New York, Datetime(ns).
func nyWall(t *testing.T, s string) *ursus.LazyFrame {
	t.Helper()
	tm, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatal(err)
	}
	return ursus.Frame(ursus.Values("ts", []time.Time{tm})).
		WithColumns(ursus.Col("ts").Dt().ReplaceTimeZone("America/New_York").Alias("ts"))
}

// timeOfDay is a Time(ns) literal.
func timeOfDay(h, m int64) ursus.Expr {
	return ursus.Lit((h*3600 + m*60) * 1e9).Cast(dtype.Time(dtype.Nano))
}

// TestCombineRefuses: a wall clock the zone skips, a day and a time past Datetime(ns)'s
// range, a Time with no day to combine, and an Int64 that is no Time of day.
func TestCombineRefuses(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	// 2024-03-10 with 02:30 is a wall clock New York's clocks skipped.
	_, err := nyWall(t, "2024-03-10 12:00:00").Select(c("ts").Dt().Combine(timeOfDay(2, 30))).Collect(ctx)
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "02:30") {
		t.Errorf("a skipped wall clock should be refused, naming it: %v", err)
	}
	// 23:59 on 2262-04-11 is past Datetime(ns)'s last instant, 23:47:16.854775807.
	late := ursus.Frame(ursus.Values("ts", []time.Time{time.Date(2262, 4, 11, 0, 0, 0, 0, time.UTC)}))
	_, err = late.Select(c("ts").Dt().Combine(timeOfDay(23, 59))).Collect(ctx)
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "outside the range") {
		t.Errorf("a day and a time past Datetime(ns)'s range should be refused: %v", err)
	}
	frame := ursus.Frame(ursus.Values("tn", []int64{3600e9})).WithColumns(c("tn").Cast(dtype.Time(dtype.Nano)).Alias("t"))
	if _, err := frame.Select(c("t").Dt().Combine(c("t"))).Collect(ctx); !errors.Is(err, uerr.ErrType) {
		t.Errorf("a Time has no day to combine: %v", err)
	}
	if _, err := frame.Select(c("tn").Cast(ursus.Datetime(ursus.Micro, "")).Dt().Combine(c("tn"))).Collect(ctx); !errors.Is(err, uerr.ErrType) {
		t.Errorf("an Int64 is no Time of day: %v", err)
	}
}

// TestCombineReadsTheDayOnTheWallClock: 22:00 on 2024-03-11 in New York is 02:00 on
// the 12th in UTC; combined with 13:15 it is 13:15 on the 11th, the day on New York's
// wall clock, as Go's time package reads it, and the type stays Datetime(ns, New York).
// Polars 2.1 reads the day in UTC, the 12th (the dtcombine group's combine_zoned_ny,
// recorded and not asserted).
func TestCombineReadsTheDayOnTheWallClock(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	ny := dtype.Datetime(dtype.Nano, "America/New_York")
	for _, wall := range []string{"2024-03-11 22:00:00", "2024-02-29 21:00:00", "2024-07-04 00:30:00", "2024-12-31 23:59:59"} {
		q := nyWall(t, wall).Select(ursus.Col("ts").Dt().Combine(timeOfDay(13, 15)))
		schema, err := q.CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := schema.Field(0).Type; got != ny {
			t.Errorf("%s combines to %s, want %s", wall, got, ny)
		}
		tm, _ := time.Parse("2006-01-02 15:04:05", wall)
		day := time.Date(tm.Year(), tm.Month(), tm.Day(), 13, 15, 0, 0, loc)
		got := typedCells[int64](t, q.Select(ursus.Col("ts").Cast(ursus.Int64)))
		if !sameCells(got, []any{day.UnixNano()}) {
			t.Errorf("%s in New York with 13:15: %v, want %v (%s)", wall, got, day.UnixNano(), day)
		}
	}
}
