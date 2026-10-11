package ursus_test

// Step 184: a grid of weeks starts on a Monday, as Polars' does; "7d" counts from the
// epoch, a Thursday. Found by the Polars oracle: ursus read "1w" as seven days and
// started its weeks on Thursdays. The answers are Polars 1.44.1's.

import (
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

func TestWeeksStartOnMonday(t *testing.T) {
	c := ursus.Col
	at := func(y int, m time.Month, d, h int) string {
		return time.Date(y, m, d, h, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05")
	}
	runCalCases(t, []calCase{
		{name: "truncate 1w", dt: naiveUs,
			in: []string{at(1970, 1, 1, 0), at(1970, 1, 8, 5), at(2024, 1, 3, 0), at(2024, 1, 1, 12)},
			f:  func(d ursus.DtExpr) ursus.Expr { return d.Truncate(ursus.Every("1w")) },
			want: []string{"1969-12-29T00:00:00", "1970-01-05T00:00:00", "2024-01-01T00:00:00",
				"2024-01-01T00:00:00"}},
		{name: "truncate 7d, from the epoch", dt: naiveUs,
			in: []string{at(1970, 1, 1, 0), at(1970, 1, 8, 5), at(2024, 1, 3, 0), at(2024, 1, 1, 12)},
			f:  func(d ursus.DtExpr) ursus.Expr { return d.Truncate(ursus.Every("7d")) },
			want: []string{"1970-01-01T00:00:00", "1970-01-08T00:00:00", "2023-12-28T00:00:00",
				"2023-12-28T00:00:00"}},
		{name: "truncate 2w", dt: naiveUs,
			in:   []string{at(1970, 1, 1, 0), at(1970, 1, 8, 5), at(2024, 1, 3, 0)},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.Truncate(ursus.Every("2w")) },
			want: []string{"1969-12-22T00:00:00", "1970-01-05T00:00:00", "2023-12-25T00:00:00"}},
		{name: "round 1w", dt: naiveUs,
			in: []string{at(1970, 1, 1, 0), at(1970, 1, 8, 5), at(2024, 1, 3, 0), at(2024, 1, 1, 12)},
			f:  func(d ursus.DtExpr) ursus.Expr { return d.Round(ursus.Every("1w")) },
			want: []string{"1969-12-29T00:00:00", "1970-01-05T00:00:00", "2024-01-01T00:00:00",
				"2024-01-01T00:00:00"}},
		{name: "a Date's week", dt: dtype.Date, in: []string{"1970-01-01", "2024-01-03"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.Truncate(ursus.Every("1w")) },
			want: []string{"1969-12-29", "2024-01-01"}},
		{name: "a week in London", dt: dtype.Datetime(dtype.Micro, "Europe/London"),
			in:   []string{"2024-03-27T12:00:00Z"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.Truncate(ursus.Every("1w")) },
			want: []string{"2024-03-25T00:00:00Z"}},
	})

	// GroupByDynamic's windows: every "1w" from Monday, "7d" from Thursday.
	frame := ursus.Frame(ursus.Values("t", []time.Time{time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 9, 0, 0, 0, 0, time.UTC)}), ursus.Values("v", []int64{1, 2})).
		Select(c("t").Cast(naiveUs), c("v"))
	for every, want := range map[string][]string{
		"1w": {"2024-01-01T00:00:00", "2024-01-08T00:00:00"},
		"7d": {"2023-12-28T00:00:00", "2024-01-04T00:00:00"},
	} {
		lf := frame.GroupByDynamic(c("t"), ursus.DynamicOptions{Every: ursus.Every(every)}).Agg(c("v").Sum()).
			Select(c("t").Alias("ts"))
		if got := calRender(t, lf); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("group_by_dynamic every %s: %v, want %v", every, got, want)
		}
	}
}

func TestWeeksAndDaysRenderApart(t *testing.T) {
	for in, want := range map[string]string{"1w": "1w", "2w": "2w", "7d": "7d", "14d": "14d", "1w1d": "8d", "-1w": "-1w"} {
		if got := ursus.Every(in).String(); got != want {
			t.Errorf("Every(%q) renders %q, want %q", in, got, want)
		}
	}
}
