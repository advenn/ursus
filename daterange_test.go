package ursus_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

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

// TestTemporalRanges is G2 of v0.6.6-scope.md (step 264): date_range,
// datetime_range and time_range. Every answer is Polars 1.44.1's.
func TestTemporalRanges(t *testing.T) {
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2, 3}))
	ymd, hm := "%Y-%m-%d", "%Y-%m-%d %H:%M"
	ny := func(e ursus.Expr) ursus.Expr { return e.Dt().ReplaceTimeZone("America/New_York") }
	for _, tc := range []struct {
		name   string
		e      ursus.Expr
		format string
		want   string
	}{
		{"days", ursus.DateRange(dateLit(2024, 1, 30), dateLit(2024, 2, 2), ursus.Every("1d")), ymd,
			"2024-01-30\n2024-01-31\n2024-02-01\n2024-02-02"},
		{"months, each from the start", ursus.DateRange(dateLit(2024, 1, 31), dateLit(2024, 5, 1), ursus.Every("1mo")), ymd,
			"2024-01-31\n2024-02-29\n2024-03-31\n2024-04-30"},
		{"weeks", ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 20), ursus.Every("1w")), ymd,
			"2024-01-01\n2024-01-08\n2024-01-15"},
		{"closed left", ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 3), ursus.Every("1d"), ursus.ClosedLeft), ymd,
			"2024-01-01\n2024-01-02"},
		{"closed none", ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 3), ursus.Every("1d"), ursus.ClosedNone), ymd,
			"2024-01-02"},
		{"reversed", ursus.DateRange(dateLit(2024, 1, 3), dateLit(2024, 1, 1), ursus.Every("1d")), ymd, ""},
		{"a negative interval", ursus.DateRange(dateLit(2024, 1, 3), dateLit(2024, 1, 1), ursus.Every("-1d")), ymd, ""},
		{"from an instant", ursus.DateRange(wallLit(2024, 1, 1, 12, 0), wallLit(2024, 1, 3, 0, 0), ursus.Every("1d")), ymd,
			"2024-01-01\n2024-01-02\n2024-01-03"},
		{"hours", ursus.DatetimeRange(wallLit(2024, 1, 1, 0, 0), wallLit(2024, 1, 1, 3, 0), time.Hour), hm,
			"2024-01-01 00:00\n2024-01-01 01:00\n2024-01-01 02:00\n2024-01-01 03:00"},
		{"ninety minutes", ursus.DatetimeRange(wallLit(2024, 1, 1, 0, 0), wallLit(2024, 1, 1, 3, 0), ursus.Every("90m")), hm,
			"2024-01-01 00:00\n2024-01-01 01:30\n2024-01-01 03:00"},
		{"from dates", ursus.DatetimeRange(dateLit(2024, 1, 1), dateLit(2024, 1, 2), ursus.Every("12h")), hm,
			"2024-01-01 00:00\n2024-01-01 12:00\n2024-01-02 00:00"},
		{"a day on the wall clock", ursus.DatetimeRange(ny(wallLit(2024, 3, 9, 12, 0)), ny(wallLit(2024, 3, 11, 12, 0)), ursus.Every("1d")), hm,
			"2024-03-09 12:00\n2024-03-10 12:00\n2024-03-11 12:00"},
		{"an hour elapsed", ursus.DatetimeRange(ny(wallLit(2024, 3, 10, 0, 0)), ny(wallLit(2024, 3, 10, 4, 0)), time.Hour), hm,
			"2024-03-10 00:00\n2024-03-10 01:00\n2024-03-10 03:00\n2024-03-10 04:00"},
		{"months at six", ursus.DatetimeRange(wallLit(2024, 1, 31, 6, 0), wallLit(2024, 4, 30, 6, 0), ursus.Every("1mo")), hm,
			"2024-01-31 06:00\n2024-02-29 06:00\n2024-03-31 06:00\n2024-04-30 06:00"},
		{"a month and a day", ursus.DatetimeRange(wallLit(2024, 1, 31, 0, 0), wallLit(2024, 6, 1, 0, 0), ursus.Every("1mo1d")), hm,
			"2024-01-31 00:00\n2024-03-01 00:00\n2024-04-02 00:00\n2024-05-03 00:00"},
		{"times", ursus.TimeRange(clockLit(9, 0), clockLit(11, 0), 30*time.Minute), "%H:%M",
			"09:00\n09:30\n10:00\n10:30\n11:00"},
		{"times, open", ursus.TimeRange(clockLit(9, 0), clockLit(11, 0), time.Hour, ursus.ClosedNone), "%H:%M", "10:00"},
	} {
		for _, size := range []int{1, 8192} {
			var got bytes.Buffer
			if err := lf.Select(tc.e.Dt().Strftime(tc.format).Alias("v")).WriteCSV(t.Context(), &got,
				ursus.WithBatchSize(size)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			want := "v\n" + tc.want
			if tc.want != "" {
				want += "\n"
			}
			if got.String() != want {
				t.Errorf("%s, batch %d:\ngot  %q\nwant %q", tc.name, size, got.String(), want)
			}
		}
	}

	// Beside the frame's columns, a range of its height; Polars [1, 2, 3].
	var got bytes.Buffer
	if err := lf.WithColumns(ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 3), ursus.Every("1d")).Dt().Day().Alias("d")).
		WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "a,d\n1,1\n2,2\n3,3\n"; got.String() != want {
		t.Errorf("in WithColumns:\ngot  %q\nwant %q", got.String(), want)
	}
}

// TestTemporalRangesAsLists: the per-row forms. Polars: date_ranges [[2024-01-01,
// 2024-01-02, 2024-01-03], None].
func TestTemporalRangesAsLists(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.ValuesNullable("s", []time.Time{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), {}}, []bool{true, false}),
		ursus.Values("e", []time.Time{time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)})).
		Select(c("s").Cast(ursus.Date), c("e").Cast(ursus.Date))
	q := lf.Select(ursus.DateRanges(c("s"), c("e"), ursus.Every("1d")).List().Len().Alias("n"),
		ursus.DateRanges(c("s"), c("e"), ursus.Every("1d")).List().Last().Dt().Day().Alias("last"))
	if got, want := csvOf(t, q), "n,last\n3,3\n,\n"; got != want {
		t.Errorf("got %q\nwant %q", got, want)
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
