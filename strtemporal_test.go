package ursus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestStrToTemporalByPatterns is step 356: Str().ToDate, ToDatetime and ToTime with no
// format, and Strptime with an empty one, read Polars' patterns, as Polars'
// str.to_date, str.to_datetime and str.to_time with format=None do. Their answers are
// Polars 2.1.0's, recorded in the strtemporal group of testdata/oracle and spelled in
// oracle_strtemporal_test.go since step 498, which took this file's table. What stays:
// two values of two patterns in one column, which Polars refuses, reading a column by
// its first value's pattern, and ursus reads each by its own (feature-gaps.md §9), held
// to Go's time package; and the strict refusals.
func TestStrToTemporalByPatterns(t *testing.T) {
	c := ursus.Col
	x := func(vals ...any) *ursus.LazyFrame { return ursus.Frame(strs("x", vals...)) }
	for _, tc := range []struct {
		name    string
		e       ursus.Expr
		dt      ursus.DataType
		texts   []string
		layouts []string
	}{
		{"two times' patterns", c("x").Str().ToTime("", true), ursus.TimeOf(ursus.Nano),
			[]string{"10:30", "11:00:01"}, []string{"15:04", "15:04:05"}},
		{"two dates' patterns", c("x").Str().ToDate(), ursus.Date,
			[]string{"2020-01-02", "02/01/2020"}, []string{time.DateOnly, "02/01/2006"}},
	} {
		vals := make([]any, len(tc.texts))
		for i, s := range tc.texts {
			vals[i] = s
		}
		q := x(vals...).Select(tc.e)
		schema, err := q.CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := schema.Field(0).Type; got != tc.dt {
			t.Errorf("%s: read as %s, want %s", tc.name, got, tc.dt)
		}
		got := typedCells[int64](t, q.Select(c("x").Cast(ursus.Int64)))
		if want := goParsedTicks(t, tc.dt, tc.texts, tc.layouts); !sameCells(got, want) {
			t.Errorf("%s: %v, Go's time package reads %v", tc.name, got, want)
		}
	}

	// Strict, as Polars' default, a value no pattern reads is refused, an instant's text
	// as a Date too: "could not find an appropriate format to parse dates".
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
	}{
		{"an instant as a Date", x("2020-01-02 03:04:05").Select(c("x").Str().Strptime(ursus.Date, "", true))},
		{"no pattern", x("abc").Select(c("x").Str().Strptime(ursus.Datetime(ursus.Micro, ""), "", true))},
		{"a date as a Time", x("2020-01-02").Select(c("x").Str().ToTime("", true))},
		{"a skipped wall clock", x("2024-03-10 02:30:00").Select(c("x").Str().Strptime(ursus.Datetime(ursus.Micro, "America/New_York"), "", true))},
	} {
		if _, err := tc.q.Collect(t.Context()); !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "Polars' patterns") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
