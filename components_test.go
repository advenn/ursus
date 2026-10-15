package ursus_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestValuesFromComponents is G3 of v0.6.6-scope.md (step 265): Polars' pl.date,
// pl.datetime, pl.time, pl.duration and pl.from_epoch. Every answer is Polars
// 1.44.1's on the same frame, a duration's as its ticks.
func TestValuesFromComponents(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.ValuesNullable("y", []int64{2024, 2024, 0, 2024}, []bool{true, true, false, true}),
		ursus.Values("m", []int64{2, 2, 1, 1}), ursus.Values("d", []int64{29, 3, 1, 1}),
		ursus.Values("h", []int64{1, 0, 0, 0}),
		ursus.ValuesNullable("f", []float64{1.5, 0.25, 0, -1}, []bool{true, true, false, true}),
		ursus.ValuesNullable("s", []int64{0, 1704067200, 0, -86400}, []bool{true, true, false, true}),
		ursus.Values("ms", []int64{0, 1704067200123, 1, -1}))
	// A duration as its ticks, named as it is; a name is read at the root, so the
	// cast is named again.
	ticks := func(e ursus.Expr) ursus.Expr { return e.Cast(ursus.Int64) }
	dticks := func(e ursus.Expr) ursus.Expr { return e.Cast(ursus.Int64).Alias("duration") }
	// %.f is the fraction in nine digits, and nothing for a whole second.
	full := "%Y-%m-%d %H:%M:%S%.f"
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"date", ursus.MakeDate(c("y"), c("m"), c("d")).Dt().Strftime("%Y-%m-%d"),
			"date\n2024-02-29\n2024-02-03\n\n2024-01-01\n"},
		{"date of an Int8 month", ursus.MakeDate(c("y"), c("m").Cast(ursus.Int8), 3).Dt().Strftime("%Y-%m-%d"),
			"date\n2024-02-03\n2024-02-03\n\n2024-01-03\n"},
		{"datetime", ursus.MakeDatetime(c("y"), c("m"), c("d"), c("h"), 30, 15, 250).Dt().Strftime(full),
			"datetime\n2024-02-29 01:30:15.000250000\n2024-02-03 00:30:15.000250000\n\n2024-01-01 00:30:15.000250000\n"},
		{"time", ursus.MakeTime(c("h"), 5, 7, 250).Dt().Strftime("%H:%M:%S%.f"),
			"time\n01:05:07.000250000\n00:05:07.000250000\n00:05:07.000250000\n00:05:07.000250000\n"},
		{"duration, every part", dticks(ursus.MakeDuration(ursus.DurationParts{Weeks: 1, Days: 2, Hours: 3,
			Minutes: 4, Seconds: 5, Milliseconds: 6, Microseconds: 7})),
			"duration\n788645006007\n788645006007\n788645006007\n788645006007\n"},
		{"duration in nanoseconds", dticks(ursus.MakeDuration(ursus.DurationParts{Nanoseconds: 5})),
			"duration\n5\n5\n5\n5\n"},
		{"duration in milliseconds", dticks(ursus.MakeDuration(ursus.DurationParts{Seconds: 1}, ursus.Milli)),
			"duration\n1000\n1000\n1000\n1000\n"},
		{"duration of float hours", dticks(ursus.MakeDuration(ursus.DurationParts{Hours: c("f")})),
			"duration\n5400000000\n900000000\n\n-3600000000\n"},
		{"duration of columns", dticks(ursus.MakeDuration(ursus.DurationParts{Days: c("d"), Hours: c("h")})),
			"duration\n2509200000000\n259200000000\n86400000000\n86400000000\n"},
		{"a negative duration", dticks(ursus.MakeDuration(ursus.DurationParts{Days: -1, Hours: 2})),
			"duration\n-79200000000\n-79200000000\n-79200000000\n-79200000000\n"},
		{"fractions summed, then truncated", dticks(ursus.MakeDuration(ursus.DurationParts{Microseconds: 0.6,
			Milliseconds: 0.0004})), "duration\n1\n1\n1\n1\n"},
		{"truncated toward zero", dticks(ursus.MakeDuration(ursus.DurationParts{Microseconds: -0.6})),
			"duration\n0\n0\n0\n0\n"},
		{"no part", dticks(ursus.MakeDuration(ursus.DurationParts{})), "duration\n0\n0\n0\n0\n"},
		{"from seconds", ursus.FromEpoch(c("s"), ursus.Second).Dt().Strftime(full),
			"s\n1970-01-01 00:00:00\n2024-01-01 00:00:00\n\n1969-12-31 00:00:00\n"},
		{"from milliseconds", ursus.FromEpoch(c("ms"), ursus.Milli).Dt().Strftime(full),
			"ms\n1970-01-01 00:00:00\n2024-01-01 00:00:00.123000000\n1970-01-01 00:00:00.001000000\n" +
				"1969-12-31 23:59:59.999000000\n"},
		{"from microseconds", ticks(ursus.FromEpoch(c("ms"), ursus.Micro)), "ms\n0\n1704067200123\n1\n-1\n"},
		{"from nanoseconds", ursus.FromEpoch(c("ms"), ursus.Nano).Dt().Strftime("%H:%M:%S%.f"),
			"ms\n00:00:00\n00:28:24.067200123\n00:00:00.000000001\n23:59:59.999999999\n"},
		{"from float seconds", ticks(ursus.FromEpoch(ursus.Lit(1.0000009), ursus.Second)), "literal\n1000000\n1000000\n1000000\n1000000\n"},
		{"toward zero", ticks(ursus.FromEpoch(ursus.Lit(-1.0000009), ursus.Second)), "literal\n-1000000\n-1000000\n-1000000\n-1000000\n"},
	} {
		var got bytes.Buffer
		if err := lf.Select(tc.e).WriteCSV(t.Context(), &got); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.String() != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got.String(), tc.want)
		}
	}

	// A zone by ReplaceTimeZone, whose options are Polars' ambiguous: 2024-03-10 12:00
	// in New York, and 01:30 on 2024-11-03, which the clock reads twice, the earlier.
	var got bytes.Buffer
	if err := lf.Head(1).Select(
		ursus.MakeDatetime(2024, 3, 10, 12, 0, 0, 0).Dt().ReplaceTimeZone("America/New_York").Dt().Strftime("%H:%M %z").Alias("a"),
		ursus.MakeDatetime(2024, 11, 3, 1, 30, 0, 0).Dt().ReplaceTimeZone("America/New_York", ursus.AmbiguousEarliest).
			Dt().Strftime("%H:%M %z").Alias("b")).WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "a,b\n12:00 -0400,01:30 -0400\n"; got.String() != want {
		t.Errorf("in a zone:\ngot  %q\nwant %q", got.String(), want)
	}
}

// TestBusinessDayCount: the business days from start up to end. Polars: [5, 0, -7,
// None]; Monday to Saturday [6, 1, -8, None]; with January 2nd and 6th holidays [4, 0,
// -6, None].
func TestBusinessDayCount(t *testing.T) {
	c := ursus.Col
	d := func(day int) time.Time { return time.Date(2024, 1, day, 0, 0, 0, 0, time.UTC) }
	lf := ursus.Frame(ursus.ValuesNullable("s", []time.Time{d(1), d(6), d(10), {}}, []bool{true, true, true, false}),
		ursus.Values("e", []time.Time{d(8), d(8), d(1), d(2)})).
		Select(c("s").Cast(ursus.Date), c("e").Cast(ursus.Date))
	for name, tc := range map[string]struct {
		cal  []ursus.BusinessCalendar
		want string
	}{
		"Monday to Friday":   {nil, "s\n5\n0\n-7\n\n"},
		"Monday to Saturday": {[]ursus.BusinessCalendar{{WeekMask: [7]bool{true, true, true, true, true, true, false}}}, "s\n6\n1\n-8\n\n"},
		"with holidays":      {[]ursus.BusinessCalendar{{Holidays: []time.Time{d(2), d(6)}}}, "s\n4\n0\n-6\n\n"},
	} {
		if got := csvOf(t, lf.Select(ursus.BusinessDayCount(c("s"), c("e"), tc.cal...))); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", name, got, tc.want)
		}
	}
}

// TestValuesFromComponentsRefuse: components that name no date or time, as Polars
// refuses them, a float component, and instants where business days take dates.
func TestValuesFromComponentsRefuse(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("y", []int64{2023}), ursus.Values("t", []time.Time{time.Now()}))
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"February 29th, 2023": {ursus.MakeDate(c("y"), 2, 29), "date's components (2023, 2, 29) in row 0 name no date"},
		"a month 13":          {ursus.MakeDate(2024, 13, 1), "name no date"},
		"an hour 25":          {ursus.MakeDatetime(2024, 1, 1, 25, 0, 0, 0), "name no time"},
		"a time of 25 hours":  {ursus.MakeTime(25, 0, 0, 0), "name no time"},
		"a float component":   {ursus.MakeDate(2024.5, 1, 1), "integer components"},
		"a string part":       {ursus.MakeDuration(ursus.DurationParts{Days: "x"}), "takes numbers"},
		"instants":            {ursus.BusinessDayCount(c("t"), c("t")), "between Dates"},
		"a day unit":          {ursus.FromEpoch(c("y"), ursus.TimeUnit(99)), "from_epoch counts in"},
	} {
		if _, err := lf.Select(tc.e).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
