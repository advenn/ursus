package ursus_test

// Step 187: GroupByDynamic's Label, StartBy and IncludeBoundaries, which were declared
// only to be refused. The answers are Polars 1.44.1's group_by_dynamic, from the
// bench's own environment. ursus keeps a dynamic group's empty windows, where Polars
// drops them, so only windows that hold a row are compared.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

func dynamicRows(t *testing.T, ts []time.Time, o ursus.DynamicOptions) string {
	t.Helper()
	c := ursus.Col
	v := make([]int64, len(ts)) // 1, 2, 3, …: a window's sum says which rows it holds
	for i := range v {
		v[i] = int64(i + 1)
	}
	frame := ursus.Frame(ursus.Values("t", ts), ursus.Values("v", v)).Select(c("t").Cast(naiveUs), c("v"))
	cols := []string{"t", "v"}
	if o.IncludeBoundaries {
		cols = []string{"_lower_boundary", "_upper_boundary", "t", "v"}
	}
	var shown []ursus.Expr
	for _, name := range cols {
		if name == "v" {
			shown = append(shown, c("v").Cast(ursus.Int64))
			continue
		}
		shown = append(shown, c(name).Dt().Strftime("%m-%d %H:%M"))
	}
	df, err := frame.GroupByDynamic(c("t"), o).Agg(c("v").Sum().Alias("v"), ursus.Len().Alias("n")).
		Filter(c("n").Gt(0)).Select(shown...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for i := range df.Height() {
		var cells []string
		for _, name := range cols {
			if name == "v" {
				sum, _, err := df.At[int64](i, "v")
				if err != nil {
					t.Fatal(err)
				}
				cells = append(cells, strconv.FormatInt(sum, 10))
				continue
			}
			v, _, err := df.At[string](i, name)
			if err != nil {
				t.Fatal(err)
			}
			cells = append(cells, v)
		}
		rows = append(rows, strings.Join(cells, " "))
	}
	return strings.Join(rows, ", ")
}

func TestDynamicOptionsAnswerAsPolars(t *testing.T) {
	at := func(m time.Month, d, h, mi int) time.Time { return time.Date(2024, m, d, h, mi, 0, 0, time.UTC) }
	hourly := []time.Time{at(1, 1, 0, 30), at(1, 1, 1, 10), at(1, 1, 2, 45), at(1, 3, 9, 0)}
	weekly := []time.Time{at(1, 4, 10, 30), at(1, 9, 1, 10), at(1, 20, 2, 45)}
	every := ursus.Every
	for _, tc := range []struct {
		name string
		ts   []time.Time
		o    ursus.DynamicOptions
		want string
	}{
		{"label left", hourly, ursus.DynamicOptions{Every: every("1h")},
			"01-01 00:00 1, 01-01 01:00 2, 01-01 02:00 3, 01-03 09:00 4"},
		{"label right", hourly, ursus.DynamicOptions{Every: every("1h"), Label: "right"},
			"01-01 01:00 1, 01-01 02:00 2, 01-01 03:00 3, 01-03 10:00 4"},
		{"label datapoint", hourly, ursus.DynamicOptions{Every: every("1h"), Label: "datapoint"},
			"01-01 00:30 1, 01-01 01:10 2, 01-01 02:45 3, 01-03 09:00 4"},
		{"boundaries", hourly, ursus.DynamicOptions{Every: every("1h"), IncludeBoundaries: true},
			"01-01 00:00 01-01 01:00 01-01 00:00 1, 01-01 01:00 01-01 02:00 01-01 01:00 2, " +
				"01-01 02:00 01-01 03:00 01-01 02:00 3, 01-03 09:00 01-03 10:00 01-03 09:00 4"},
		{"a period of 2h, right, with boundaries", hourly,
			ursus.DynamicOptions{Every: every("1h"), Period: every("2h"), Label: "right", IncludeBoundaries: true},
			"01-01 00:00 01-01 02:00 01-01 02:00 3, 01-01 01:00 01-01 03:00 01-01 03:00 5, " +
				"01-01 02:00 01-01 04:00 01-01 04:00 3, 01-03 08:00 01-03 10:00 01-03 10:00 4, " +
				"01-03 09:00 01-03 11:00 01-03 11:00 4"},
		{"start by datapoint, hourly", hourly, ursus.DynamicOptions{Every: every("1h"), StartBy: "datapoint"},
			"01-01 00:30 3, 01-01 02:30 3, 01-03 08:30 4"},
		{"weeks from Monday", weekly, ursus.DynamicOptions{Every: every("1w")},
			"01-01 00:00 1, 01-08 00:00 2, 01-15 00:00 3"},
		{"weeks from Wednesday", weekly, ursus.DynamicOptions{Every: every("1w"), StartBy: "wednesday"},
			"01-03 00:00 3, 01-17 00:00 3"},
		{"weeks from Sunday", weekly, ursus.DynamicOptions{Every: every("1w"), StartBy: "Sunday"},
			"12-31 00:00 1, 01-07 00:00 2, 01-14 00:00 3"},
		{"fortnights from Friday", weekly, ursus.DynamicOptions{Every: every("2w"), StartBy: "friday"},
			"12-22 00:00 1, 01-05 00:00 2, 01-19 00:00 3"},
		{"weeks from the first row", weekly, ursus.DynamicOptions{Every: every("1w"), StartBy: "datapoint"},
			"01-04 10:30 3, 01-18 10:30 3"},
		{"weeks from Wednesday, offset 3h", weekly,
			ursus.DynamicOptions{Every: every("1w"), StartBy: "wednesday", Offset: every("3h")},
			"01-03 03:00 3, 01-17 03:00 3"},
		{"days from the first row: the offset ignored", weekly,
			ursus.DynamicOptions{Every: every("1d"), StartBy: "datapoint", Offset: every("2h")},
			"01-04 10:30 1, 01-08 10:30 2, 01-19 10:30 3"},
		{"days from the first row, right", weekly, ursus.DynamicOptions{Every: every("1d"), StartBy: "datapoint", Label: "right"},
			"01-05 10:30 1, 01-09 10:30 2, 01-20 10:30 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dynamicRows(t, tc.ts, tc.o); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestDynamicOptionsRefuseWhatPolarsRefuses(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("t", []time.Time{time.Unix(0, 0)}), ursus.Values("v", []int64{1}))
	for _, tc := range []struct {
		o    ursus.DynamicOptions
		want string
	}{
		{ursus.DynamicOptions{Every: ursus.Every("1h"), Label: "middle"}, "is not one of"},
		{ursus.DynamicOptions{Every: ursus.Every("1h"), StartBy: "midweek"}, "is not one of"},
		// Polars documents a weekday for a grid of weeks alone, and answers others by
		// a rule it does not document: every 5h from Friday at 20:00.
		{ursus.DynamicOptions{Every: ursus.Every("5h"), StartBy: "friday"}, "only a grid of weeks"},
		{ursus.DynamicOptions{Every: ursus.Every("7d"), StartBy: "monday"}, "only a grid of weeks"},
	} {
		assertUserError(t, frame.GroupByDynamic(c("t"), tc.o).Agg(c("v").Sum()), tc.want)
	}
}
