package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// G2 of v0.6.6-scope.md (step 264): date_range, datetime_range and time_range. Their
// answers are Polars 2.1.0's, recorded in the daterange group of testdata/oracle and
// spelled in oracle_daterange_test.go since step 498, which took this file's table.
// What stays asserts the refusals, and that a range's rows do not depend on the batch
// size.

// dateLit is a Date literal, wallLit a naive Datetime(us) one, and clockLit a Time(us)
// one.
func dateLit(y int, m time.Month, d int) ursus.Expr {
	return ursus.Lit(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)).Cast(ursus.Date)
}

func wallLit(y int, m time.Month, d, h, mi int) ursus.Expr {
	return ursus.Lit(time.Date(y, m, d, h, mi, 0, 0, time.UTC)).Cast(ursus.Datetime(ursus.Micro, ""))
}

func clockLit(h, m int) ursus.Expr {
	return ursus.Lit(time.Date(1970, 1, 1, h, m, 0, 0, time.UTC)).Cast(ursus.TimeOf(ursus.Micro))
}

// TestTemporalRangesAgreeAcrossBatchSizes: a range's rows are the same under a batch
// of one row and one of 8192.
func TestTemporalRangesAgreeAcrossBatchSizes(t *testing.T) {
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2, 3}))
	for name, e := range map[string]ursus.Expr{
		"days":              ursus.DateRange(dateLit(2024, 1, 30), dateLit(2024, 2, 2), ursus.Every("1d")),
		"a month and a day": ursus.DatetimeRange(wallLit(2024, 1, 31, 0, 0), wallLit(2024, 6, 1, 0, 0), ursus.Every("1mo1d")),
		"times":             ursus.TimeRange(clockLit(9, 0), clockLit(11, 0), 30*time.Minute),
	} {
		one, err := lf.Select(e.Alias("v")).Collect(t.Context(), ursus.WithBatchSize(1))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		big, err := lf.Select(e.Alias("v")).Collect(t.Context(), ursus.WithBatchSize(8192))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if one.Height() == 0 {
			t.Errorf("%s: no rows", name)
		}
		ursustest.AssertFrameEqual(t, one, big)
	}
}

// TestTemporalRangesRefuse: an interval finer than a day for dates, as Polars refuses
// it; a calendar one for times; one of zero; and ends of two types.
func TestTemporalRangesRefuse(t *testing.T) {
	lf := ursus.Frame(ursus.Values("a", []int64{1}))
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"hours of dates":      {ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 2), ursus.Every("12h")), "whole days"},
		"months of times":     {ursus.TimeRange(clockLit(1, 0), clockLit(2, 0), ursus.Every("1mo")), "calendar part"},
		"an interval of zero": {ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 2), time.Duration(0)), "zero"},
		"two types":           {ursus.TimeRange(clockLit(1, 0), wallLit(2024, 1, 1, 0, 0), time.Hour), "two ends of its type"},
	} {
		if _, err := lf.Select(tc.e).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
