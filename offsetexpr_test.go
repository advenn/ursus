package ursus_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestOffsetByAnExpression is E5's last part (step 256): dt.offset_by with an offset
// a row. Every answer is Polars 1.44.1's, naive and in America/New_York, where a day
// across the change to daylight time is a day of the wall clock and two hours are
// elapsed time: 2024-03-09 12:00 by "1d2h" is 2024-03-10 14:00 in both.
func TestOffsetByAnExpression(t *testing.T) {
	c := ursus.Col
	at := func(m, d, h int) time.Time { return time.Date(2024, time.Month(m), d, h, 0, 0, 0, time.UTC) }
	lf := ursus.Frame(
		ursus.ValuesNullable("t", []time.Time{at(1, 31, 0), at(3, 9, 12), at(1, 1, 0), at(1, 1, 0),
			at(3, 31, 6), at(1, 1, 0), at(1, 1, 0), at(1, 1, 0)},
			[]bool{true, true, false, true, true, true, true, true}),
		ursus.ValuesNullable("o", []string{"1mo", "1d2h", "1d", "", "-1mo", "90m", "1w", "1y"},
			[]bool{true, true, true, false, true, true, true, true}),
	).WithColumns(c("t").Cast(ursus.Datetime(ursus.Micro, "")))
	want := "t\n2024-02-29 00:00\n2024-03-10 14:00\n\n\n2024-02-29 06:00\n2024-01-01 01:30\n" +
		"2024-01-08 00:00\n2025-01-01 00:00\n"
	for name, e := range map[string]ursus.Expr{
		"naive": c("t"),
		"zoned": c("t").Dt().ReplaceTimeZone("America/New_York"),
	} {
		for _, size := range []int{1, 3, 8192} {
			var got bytes.Buffer
			if err := lf.Select(e.Dt().OffsetBy(c("o")).Dt().Strftime("%Y-%m-%d %H:%M")).
				WriteCSV(t.Context(), &got, ursus.WithBatchSize(size)); err != nil {
				t.Fatalf("%s, batch %d: %v", name, size, err)
			}
			if got.String() != want {
				t.Errorf("%s, batch %d:\ngot  %q\nwant %q", name, size, got.String(), want)
			}
		}
	}

	// A Date, a month and a year on: Polars [2024-02-29, 2025-02-28, None]. A literal
	// offset is one row long.
	var got bytes.Buffer
	if err := ursus.Frame(
		ursus.ValuesNullable("d", []time.Time{at(1, 31, 0), at(2, 29, 0), at(1, 1, 0)}, []bool{true, true, false}),
		ursus.Values("o", []string{"1mo", "1y", "1d"}),
	).Select(c("d").Cast(ursus.Date).Dt().OffsetBy(c("o")),
		c("d").Cast(ursus.Date).Dt().OffsetBy(ursus.Lit("1d")).Alias("lit")).
		WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "d,lit\n2024-02-29,2024-02-01\n2025-02-28,2024-03-01\n,\n"; got.String() != want {
		t.Errorf("a Date:\ngot  %q\nwant %q", got.String(), want)
	}
}

// TestOffsetByAnExpressionRefuses: an offset that does not parse, or that a Date
// cannot hold, names its row when it is read; a Time and an integer offset are
// refused while the query is planned.
func TestOffsetByAnExpressionRefuses(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("t", []time.Time{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}),
		ursus.Values("bad", []string{"xx"}), ursus.Values("hour", []string{"1h"}),
		ursus.Values("n", []int64{1}))
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"an unparsed offset": {c("t").Dt().OffsetBy(c("bad")), `offset_by "xx" in row 0`},
		"an hour on a Date":  {c("t").Cast(ursus.Date).Dt().OffsetBy(c("hour")), "finer than"},
		"a Time":             {c("t").Cast(ursus.TimeOf(ursus.Micro)).Dt().OffsetBy(c("hour")), "Date or Datetime"},
		"an integer offset":  {c("t").Dt().OffsetBy(c("n")), "as Strings"},
	} {
		_, err := lf.Select(tc.e).Collect(t.Context())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
