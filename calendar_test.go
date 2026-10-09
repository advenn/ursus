package ursus_test

// The calendar (step 145): OffsetBy, Round, MonthStart, MonthEnd, IsLeapYear and
// ConvertTimeZone, and the wall-clock reading they share with Truncate and
// GroupByDynamic.
//
// The by-hand cases name each rule once, in a form a reader can check against a
// calendar. The sweeps then hold the rules across every transition of six zones,
// against an oracle that tries each offset a zone uses near a wall clock and knows
// nothing of how the kernel reads one.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// calColumn is one column ts of type dt. Each value is "null", a date for a Date
// column, a wall clock for a naive Datetime, or an RFC 3339 instant.
func calColumn(t *testing.T, dt dtype.DataType, vals ...string) *ursus.LazyFrame {
	t.Helper()
	if tz := dt.TimeZone(); tz != "" && tz != "UTC" {
		if _, err := time.LoadLocation(tz); err != nil {
			t.Skipf("no tzdata for %s: %v", tz, err)
		}
	}
	ticks := make([]int64, len(vals))
	valid := bitmap.NewBuilder(len(vals))
	for i, v := range vals {
		valid.Append(v != "null")
		if v == "null" {
			continue
		}
		layout := time.RFC3339Nano
		switch {
		case dt.ID() == dtype.TypeDate:
			layout = time.DateOnly
		case dt.TimeZone() == "":
			layout = "2006-01-02T15:04:05.999999999"
		}
		tm, err := time.Parse(layout, v)
		if err != nil {
			t.Fatal(err)
		}
		tick, ok := dt.FromTime(tm)
		if !ok {
			t.Fatalf("cannot store %s as %s", v, dt)
		}
		ticks[i] = tick
	}
	if dt.ID() == dtype.TypeDate {
		days := make([]int32, len(ticks))
		for i, v := range ticks {
			days[i] = int32(v)
		}
		return ursus.Frame(data.NewFixed("ts", dt, days, valid.Finish()))
	}
	return ursus.Frame(data.NewFixed("ts", dt, ticks, valid.Finish()))
}

// calRender reads column ts of lf as calColumn writes its values, in the zone of the
// type the column has.
func calRender(t *testing.T, lf *ursus.LazyFrame) []string {
	t.Helper()
	schema, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dt := schema.Field(0).Type
	df, err := lf.Select(ursus.Col("ts").Cast(ursus.Int64)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	col, err := df.Column[int64]("ts")
	if err != nil {
		t.Fatal(err)
	}
	var loc *time.Location
	if tz := dt.TimeZone(); tz != "" {
		if loc, err = time.LoadLocation(tz); err != nil {
			t.Fatal(err)
		}
	}
	out := make([]string, col.Len())
	for i := range out {
		v, ok := col.Get(i)
		if !ok {
			out[i] = "null"
			continue
		}
		tm, _ := dt.ToTime(v)
		switch {
		case dt.ID() == dtype.TypeDate:
			out[i] = tm.Format(time.DateOnly)
		case loc == nil:
			out[i] = tm.Format("2006-01-02T15:04:05.999999999")
		default:
			out[i] = tm.In(loc).Format(time.RFC3339Nano)
		}
	}
	return out
}

var (
	ny      = dtype.Datetime(dtype.Micro, "America/New_York")
	utcUs   = dtype.Datetime(dtype.Micro, "UTC")
	naiveUs = dtype.Datetime(dtype.Micro, "")
)

type calCase struct {
	name   string
	dt     dtype.DataType
	in     []string
	f      func(ursus.DtExpr) ursus.Expr
	want   []string // or
	refuse string   // a plan-time refusal naming this
}

func runCalCases(t *testing.T, cases []calCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lf := calColumn(t, c.dt, c.in...).Select(c.f(ursus.Col("ts").Dt()).Alias("ts"))
			if c.refuse != "" {
				_, err := lf.CollectSchema(t.Context())
				if err == nil || !strings.Contains(err.Error(), c.refuse) {
					t.Fatalf("want a plan-time refusal naming %q, got %v", c.refuse, err)
				}
				assertUserError(t, lf, c.refuse)
				return
			}
			got := calRender(t, lf)
			if !slices.Equal(got, c.want) {
				t.Errorf("got  %v\nwant %v", got, c.want)
			}
		})
	}
}

func TestOffsetByByHand(t *testing.T) {
	month, day, year := ursus.Every("1mo"), ursus.Every("1d"), ursus.Every("1y")
	runCalCases(t, []calCase{
		// Months clamp to the end of the month, years too, and back as well.
		{name: "a month after the 31st", dt: dtype.Date,
			in:   []string{"2024-01-31", "2023-01-31", "2024-03-31", "null"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(month) },
			want: []string{"2024-02-29", "2023-02-28", "2024-04-30", "null"}},
		{name: "a year after the 29th of February", dt: dtype.Date,
			in:   []string{"2024-02-29"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(year) },
			want: []string{"2025-02-28"}},
		{name: "a month before the 31st", dt: utcUs,
			in:   []string{"2024-03-31T10:15:00Z"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(ursus.Every("-1mo")) },
			want: []string{"2024-02-29T10:15:00Z"}},
		{name: "a Date by days, and by a whole number of days as a Duration", dt: dtype.Date,
			in:   []string{"2024-12-31"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(48 * time.Hour) },
			want: []string{"2025-01-02"}},
		{name: "a naive Datetime moves its wall clock", dt: naiveUs,
			in:   []string{"2024-03-10T01:30:00"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(ursus.Every("1d2h")) },
			want: []string{"2024-03-11T03:30:00"}},

		// A day is the wall clock's, across a daylight-saving change: 23 hours here.
		{name: "a day across spring forward keeps 09:00", dt: ny,
			in:   []string{"2024-03-09T09:00:00-05:00"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(day) },
			want: []string{"2024-03-10T09:00:00-04:00"}},
		// An hour is elapsed time, both spellings.
		{name: "an hour across spring forward is elapsed time", dt: ny,
			in:   []string{"2024-03-10T01:30:00-05:00"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(ursus.Every("1h")) },
			want: []string{"2024-03-10T03:30:00-04:00"}},
		{name: "minus ninety minutes", dt: ny,
			in:   []string{"2024-03-10T03:30:00-04:00"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(-90 * time.Minute) },
			want: []string{"2024-03-10T01:00:00-05:00"}},
		// A wall clock read twice, in a fold, keeps the input's own offset.
		{name: "a day into a fold, from summer time", dt: ny,
			in:   []string{"2024-11-02T01:30:00-04:00"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(day) },
			want: []string{"2024-11-03T01:30:00-04:00"}},
		{name: "a day back into a fold, from winter time", dt: ny,
			in:   []string{"2024-11-04T01:30:00-05:00"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(ursus.Every("-1d")) },
			want: []string{"2024-11-03T01:30:00-05:00"}},
		// A wall clock never read, in a gap, is the instant the gap ends. Santiago's
		// 2024-09-08 began at 01:00: AddDate went back an hour, to the same date.
		{name: "a day into a midnight that does not exist", dt: dtype.Datetime(dtype.Micro, "America/Santiago"),
			in:   []string{"2024-09-07T00:00:00-04:00", "2024-09-07T00:30:00-04:00"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(day) },
			want: []string{"2024-09-08T01:00:00-03:00", "2024-09-08T01:00:00-03:00"}},
		{name: "a month into a gap", dt: ny,
			in:   []string{"2024-02-10T02:30:00-05:00"},
			f:    func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(month) },
			want: []string{"2024-03-10T03:00:00-04:00"}},

		// Refused while the query is planned.
		{name: "an hour on a Date", dt: dtype.Date, in: []string{"2024-01-01"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(time.Hour) },
			refuse: "finer than Date holds"},
		{name: "a nanosecond on microseconds", dt: utcUs, in: []string{"2024-01-01T00:00:00Z"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(time.Nanosecond) },
			refuse: "finer than"},
		{name: "a Time", dt: dtype.Time(dtype.Nano), in: nil,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(time.Hour) },
			refuse: "requires a Date or Datetime"},
		{name: "an interval that does not parse", dt: utcUs, in: nil,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(ursus.Every("1mo-1d")) },
			refuse: "cannot parse interval"},
	})
}

func TestOffsetByPastTheRangeIsRefused(t *testing.T) {
	ns := dtype.Datetime(dtype.Nano, "")
	for _, c := range []struct {
		name string
		by   ursus.Expr
	}{
		{"a month", ursus.Col("ts").Dt().OffsetBy(ursus.Every("1mo"))},
		{"a Duration", ursus.Col("ts").Dt().OffsetBy(30 * 24 * time.Hour)},
		{"the month's end", ursus.Col("ts").Dt().MonthEnd()},
	} {
		_, err := calColumn(t, ns, "2262-04-01T00:00:00").Select(c.by).Collect(t.Context())
		if err == nil || !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "outside the range") {
			t.Errorf("%s past 2262-04-11 at Datetime(ns): want a value refusal, got %v", c.name, err)
		}
	}
	// A Date's ticks are an int32 of days, and FromTime's are an int64: 2024-01-01 is
	// day 19,723, and 2,147,483,000 days later is past 2^31. A Duration holds 106,751
	// days at most, so only an Interval reaches it.
	_, err := calColumn(t, dtype.Date, "2024-01-01").
		Select(ursus.Col("ts").Dt().OffsetBy(ursus.Every("2147483000d"))).Collect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "outside the range") {
		t.Errorf("2,147,483,000 days on a Date: want a range refusal, got %v", err)
	}
}

func TestMonthStartAndEndByHand(t *testing.T) {
	start := func(d ursus.DtExpr) ursus.Expr { return d.MonthStart() }
	end := func(d ursus.DtExpr) ursus.Expr { return d.MonthEnd() }
	tokyo := dtype.Datetime(dtype.Micro, "Asia/Tokyo")
	runCalCases(t, []calCase{
		{name: "dates", dt: dtype.Date, f: start,
			in:   []string{"2024-02-10", "2023-12-31", "null"},
			want: []string{"2024-02-01", "2023-12-01", "null"}},
		{name: "dates, the end", dt: dtype.Date, f: end,
			in:   []string{"2024-02-10", "2023-02-10", "1900-02-01", "2000-02-01"},
			want: []string{"2024-02-29", "2023-02-28", "1900-02-28", "2000-02-29"}},
		{name: "the time of day is kept", dt: utcUs, f: start,
			in:   []string{"2024-02-10T13:45:01.5Z"},
			want: []string{"2024-02-01T13:45:01.5Z"}},
		// 2024-01-31T20:00Z is February 1st in Tokyo: its month is February.
		{name: "the month is the zone's", dt: tokyo, f: end,
			in:   []string{"2024-01-31T20:00:00Z"},
			want: []string{"2024-02-29T05:00:00+09:00"}},
		{name: "back across spring forward", dt: ny, f: start,
			in:   []string{"2024-03-31T09:00:00-04:00"},
			want: []string{"2024-03-01T09:00:00-05:00"}},
		// Asunción's 2023-10-01 began at 01:00, so 00:30 on it was never read.
		{name: "into a gap", dt: dtype.Datetime(dtype.Micro, "America/Asuncion"), f: start,
			in:   []string{"2023-10-15T00:30:00-03:00"},
			want: []string{"2023-10-01T01:00:00-03:00"}},
		{name: "a Time", dt: dtype.Time(dtype.Nano), f: start,
			refuse: "requires a Date or Datetime"},
	})
}

func TestIsLeapYear(t *testing.T) {
	lf := calColumn(t, dtype.Date, "1900-06-01", "2000-06-01", "2023-06-01", "2024-06-01",
		"2100-06-01", "0004-06-01", "null")
	df, err := lf.Select(ursus.Col("ts").Dt().IsLeapYear()).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := df.Column[bool]("ts")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"false", "true", "false", "true", "false", "true", "null"}
	for i, w := range want {
		v, ok := got.Get(i)
		s := fmt.Sprint(v)
		if !ok {
			s = "null"
		}
		if s != w {
			t.Errorf("row %d: %s, want %s", i, s, w)
		}
	}

	// The year is the zone's: the last half hour of 2024 in UTC is 2025 in Tokyo.
	for zone, want := range map[string]bool{"UTC": true, "Asia/Tokyo": false} {
		df, err := calColumn(t, dtype.Datetime(dtype.Micro, zone), "2024-12-31T23:30:00Z").
			Select(ursus.Col("ts").Dt().IsLeapYear()).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		got, _ := df.Column[bool]("ts")
		if v, _ := got.Get(0); v != want {
			t.Errorf("%s: %v, want %v", zone, v, want)
		}
	}
	assertUserError(t, calColumn(t, dtype.Duration(dtype.Nano), "null").
		Select(ursus.Col("ts").Dt().IsLeapYear()), "requires a Date or Datetime")
}

func TestRoundByHand(t *testing.T) {
	round := func(every any) func(ursus.DtExpr) ursus.Expr {
		return func(d ursus.DtExpr) ursus.Expr {
			if iv, ok := every.(ursus.Interval); ok {
				return d.Round(iv)
			}
			return d.Round(every.(time.Duration))
		}
	}
	kolkata := dtype.Datetime(dtype.Micro, "Asia/Kolkata")
	runCalCases(t, []calCase{
		// Halfway rounds up, and only halfway.
		{name: "fifteen minutes", dt: utcUs, f: round(15 * time.Minute),
			in:   []string{"2024-01-01T10:07:30Z", "2024-01-01T10:07:29.999999Z", "null"},
			want: []string{"2024-01-01T10:15:00Z", "2024-01-01T10:00:00Z", "null"}},
		{name: "before the epoch", dt: naiveUs, f: round(ursus.Every("1h")),
			in:   []string{"1969-12-31T23:30:00", "1969-12-31T23:29:59"},
			want: []string{"1970-01-01T00:00:00", "1969-12-31T23:00:00"}},
		// A month's middle depends on its length: February 2024 has 29 days, so its
		// middle is the 15th at noon, and January's the 16th at noon.
		{name: "a month", dt: utcUs, f: round(ursus.Every("1mo")),
			in:   []string{"2024-02-15T12:00:00Z", "2024-02-15T11:59:59Z"},
			want: []string{"2024-03-01T00:00:00Z", "2024-02-01T00:00:00Z"}},
		{name: "a month, on dates", dt: dtype.Date, f: round(ursus.Every("1mo")),
			in:   []string{"2024-01-16", "2024-01-17", "2023-02-15"},
			want: []string{"2024-01-01", "2024-02-01", "2023-03-01"}},
		// A 23-hour day's middle is 11:30 after its start: 12:30 EDT.
		{name: "a day of 23 hours", dt: ny, f: round(ursus.Every("1d")),
			in:   []string{"2024-03-10T12:29:59-04:00", "2024-03-10T12:30:00-04:00"},
			want: []string{"2024-03-10T00:00:00-05:00", "2024-03-11T00:00:00-04:00"}},
		// The wall clock's hour for an Interval, the epoch's for a Duration, as
		// Truncate.
		{name: "the local hour at +05:30", dt: kolkata, f: round(ursus.Every("1h")),
			in:   []string{"2024-06-01T10:47:00+05:30"},
			want: []string{"2024-06-01T11:00:00+05:30"}},
		{name: "the epoch's hour at +05:30", dt: kolkata, f: round(time.Hour),
			in:   []string{"2024-06-01T10:47:00+05:30"},
			want: []string{"2024-06-01T10:30:00+05:30"}},
		// Across a fold the next hour is the one the clock reaches.
		{name: "an hour in a fold", dt: ny, f: round(ursus.Every("1h")),
			in:   []string{"2024-11-03T01:30:00-04:00", "2024-11-03T01:29:00-04:00"},
			want: []string{"2024-11-03T01:00:00-05:00", "2024-11-03T01:00:00-04:00"}},

		{name: "a Time", dt: dtype.Time(dtype.Nano), f: round(time.Hour),
			refuse: "round is not defined for Time"},
		{name: "a zero interval", dt: utcUs, f: round(time.Duration(0)),
			refuse: "round needs a positive interval"},
		{name: "a Duration column", dt: dtype.Duration(dtype.Nano), f: round(time.Hour),
			refuse: "requires a Date, Time or Datetime"},
	})
}

func TestConvertTimeZone(t *testing.T) {
	lf := calColumn(t, utcUs, "2024-06-01T09:00:00Z", "null").
		Select(ursus.Col("ts").Dt().ConvertTimeZone("Asia/Tokyo").Alias("ts"))
	if got := calRender(t, lf); !slices.Equal(got, []string{"2024-06-01T18:00:00+09:00", "null"}) {
		t.Errorf("got %v", got)
	}
	schema, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := schema.Field(0).Type, dtype.Datetime(dtype.Micro, "Asia/Tokyo"); got != want {
		t.Errorf("the type is %s, want %s", got, want)
	}
	// Components follow the new zone, and the instants do not move.
	df, err := lf.Select(ursus.Col("ts").Dt().Hour().Alias("h"),
		ursus.Col("ts").Dt().ConvertTimeZone("UTC").Cast(ursus.Int64).Alias("t")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	h, _ := df.Column[int32]("h")
	if v, _ := h.Get(0); v != 18 {
		t.Errorf("the hour in Tokyo is %d, want 18", v)
	}
	ticks, _ := df.Column[int64]("t")
	if v, _ := ticks.Get(0); v != time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC).UnixMicro() {
		t.Errorf("the instant moved: %d", v)
	}

	for _, c := range []struct {
		name string
		dt   dtype.DataType
		tz   string
		want string
	}{
		{"a naive Datetime", naiveUs, "UTC", "requires a Datetime with a time zone"},
		{"a Date", dtype.Date, "UTC", "requires a Datetime"},
		{"an unknown zone", utcUs, "Mars/Olympus_Mons", "not known"},
		{"no zone", utcUs, "", "needs a time zone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertUserError(t, calColumn(t, c.dt).Select(ursus.Col("ts").Dt().ConvertTimeZone(c.tz)), c.want)
		})
	}
}

// TestADayStartsWhereItsMidnightDoesNotExist: Santiago's 2024-09-08 and Havana's
// 2024-03-10 begin at 01:00, and Asunción's October 2023 too. Truncate and
// GroupByDynamic put the start of each at 23:00 the day before, a whole day off,
// until step 145.
func TestADayStartsWhereItsMidnightDoesNotExist(t *testing.T) {
	santiago := dtype.Datetime(dtype.Micro, "America/Santiago")
	runCalCases(t, []calCase{
		{name: "Santiago, a day", dt: santiago,
			f:    func(d ursus.DtExpr) ursus.Expr { return d.Truncate(ursus.Every("1d")) },
			in:   []string{"2024-09-08T10:00:00-03:00"},
			want: []string{"2024-09-08T01:00:00-03:00"}},
		{name: "Havana, a day", dt: dtype.Datetime(dtype.Micro, "America/Havana"),
			f:    func(d ursus.DtExpr) ursus.Expr { return d.Truncate(ursus.Every("1d")) },
			in:   []string{"2024-03-10T10:00:00-04:00"},
			want: []string{"2024-03-10T01:00:00-04:00"}},
		{name: "Asunción, a month", dt: dtype.Datetime(dtype.Micro, "America/Asuncion"),
			f:    func(d ursus.DtExpr) ursus.Expr { return d.Truncate(ursus.Every("1mo")) },
			in:   []string{"2023-10-15T10:00:00-03:00"},
			want: []string{"2023-10-01T01:00:00-03:00"}},
	})

	// The windows of a day: the row at 23:30 on the 7th is the 7th's.
	df, err := calColumn(t, santiago,
		"2024-09-07T12:00:00-04:00", "2024-09-07T23:30:00-04:00", "2024-09-08T12:00:00-03:00").
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1d")}).
		Agg(ursus.Len().Alias("n")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	starts := calRender(t, df.Lazy().Select(ursus.Col("ts")))
	n, _ := df.Column[uint64]("n")
	var got []string
	for i, s := range starts {
		v, _ := n.Get(i)
		got = append(got, fmt.Sprintf("%s n=%d", s, v))
	}
	want := []string{"2024-09-07T00:00:00-04:00 n=2", "2024-09-08T01:00:00-03:00 n=1"}
	if !slices.Equal(got, want) {
		t.Errorf("windows\n got %v\nwant %v", got, want)
	}
}

// readWallOracle is the instant at which loc's wall clock reads w, a wall clock
// written as a UTC time. It tries each offset loc uses within a day and a half of w,
// and keeps those under which w is a real reading: one is the answer; of two, a
// fold, the one at offset prefer; of none, a gap, the first instant whose wall clock
// is past w, which is where the gap ends. ok is false for a fold where neither
// offset is prefer.
func readWallOracle(w time.Time, loc *time.Location, prefer int) (time.Time, bool) {
	offsets := map[int]bool{}
	for u := w.Add(-36 * time.Hour); u.Before(w.Add(36 * time.Hour)); u = u.Add(15 * time.Minute) {
		_, o := u.In(loc).Zone()
		offsets[o] = true
	}
	var real []time.Time
	for o := range offsets {
		u := w.Add(-time.Duration(o) * time.Second)
		if _, uo := u.In(loc).Zone(); uo == o {
			real = append(real, u)
		}
	}
	switch len(real) {
	case 1:
		return real[0], true
	case 0:
		for u := w.Add(-15 * time.Hour).Truncate(time.Minute); ; u = u.Add(time.Minute) {
			if wallSeconds(u, loc) > w.Unix() {
				return u, true
			}
		}
	}
	for _, u := range real {
		if _, o := u.In(loc).Zone(); o == prefer {
			return u, true
		}
	}
	return time.Time{}, false
}

// TestCalendarAgainstTheOracle sweeps OffsetBy, MonthStart, MonthEnd and Round over
// the instants around every 2024 transition of seven zones, and around the ends of
// months, against readWallOracle. The zones have gaps at 02:00 and at midnight,
// half-hour offsets, and a 30-minute daylight shift.
func TestCalendarAgainstTheOracle(t *testing.T) {
	zones := []string{"UTC", "America/New_York", "America/Santiago", "America/Havana",
		"America/Asuncion", "Australia/Lord_Howe", "Asia/Kolkata"}
	type op struct {
		name   string
		e      ursus.Expr
		oracle func(s time.Time, loc *time.Location) (time.Time, bool)
	}
	// wallOf is s's wall clock in loc, written as a UTC time, and s's offset there.
	wallOf := func(s time.Time, loc *time.Location) (time.Time, int) {
		l := s.In(loc)
		_, off := l.Zone()
		return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(),
			l.Nanosecond(), time.UTC), off
	}
	offset := func(spelled string, months, days int, d time.Duration) op {
		return op{"offset_by " + spelled, ursus.Col("ts").Dt().OffsetBy(ursus.Every(spelled)),
			func(s time.Time, loc *time.Location) (time.Time, bool) {
				w, off := wallOf(s, loc)
				y, m, day := w.Date()
				if months != 0 {
					first := time.Date(y, m+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
					y, m = first.Year(), first.Month()
					day = min(day, time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day())
				}
				w = time.Date(y, m, day+days, w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), time.UTC)
				u, ok := readWallOracle(w, loc, off)
				return u.Add(d), ok
			}}
	}
	onDay := func(name string, e ursus.Expr, last bool) op {
		return op{name, e, func(s time.Time, loc *time.Location) (time.Time, bool) {
			w, off := wallOf(s, loc)
			day := 1
			if last {
				day = time.Date(w.Year(), w.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
			}
			return readWallOracle(time.Date(w.Year(), w.Month(), day, w.Hour(), w.Minute(),
				w.Second(), w.Nanosecond(), time.UTC), loc, off)
		}}
	}
	// roundCalendar's window is from the start of s's day or month to the start of
	// the next, and s goes to the nearer, the later when halfway.
	roundCalendar := func(spelled string, month bool) op {
		return op{"round " + spelled, ursus.Col("ts").Dt().Round(ursus.Every(spelled)),
			func(s time.Time, loc *time.Location) (time.Time, bool) {
				w, off := wallOf(s, loc)
				lo := time.Date(w.Year(), w.Month(), w.Day(), 0, 0, 0, 0, time.UTC)
				hi := lo.AddDate(0, 0, 1)
				if month {
					lo = time.Date(w.Year(), w.Month(), 1, 0, 0, 0, 0, time.UTC)
					hi = lo.AddDate(0, 1, 0)
				}
				start, ok1 := readWallOracle(lo, loc, off)
				end, ok2 := readWallOracle(hi, loc, off)
				if s.Sub(start) >= end.Sub(s) {
					return end, ok1 && ok2
				}
				return start, ok1 && ok2
			}}
	}
	// A sub-day grid rounds s plus half the interval down, on the wall clock.
	roundClock := func(every time.Duration, spelled string) op {
		return op{"round " + spelled, ursus.Col("ts").Dt().Round(ursus.Every(spelled)),
			func(s time.Time, loc *time.Location) (time.Time, bool) {
				return gridFloorOracle(s.Add(every/2), loc, every)
			}}
	}
	ops := []op{
		offset("1d", 0, 1, 0), offset("-1d", 0, -1, 0), offset("1w", 0, 7, 0),
		offset("1mo", 1, 0, 0), offset("-1mo", -1, 0, 0), offset("1y", 12, 0, 0),
		offset("1mo1d", 1, 1, 0), offset("2d3h", 0, 2, 3*time.Hour),
		offset("-90m", 0, 0, -90*time.Minute),
		onDay("month_start", ursus.Col("ts").Dt().MonthStart(), false),
		onDay("month_end", ursus.Col("ts").Dt().MonthEnd(), true),
		roundCalendar("1d", false), roundCalendar("1mo", true),
		roundClock(time.Hour, "1h"), roundClock(90*time.Minute, "90m"),
	}

	var checked int
	for _, zone := range zones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no tzdata for %s: %v", zone, err)
		}
		dt := dtype.Datetime(dtype.Micro, zone)
		var stamps []time.Time
		for _, tr := range transitions(loc) {
			for d := -26 * time.Hour; d <= 26*time.Hour; d += 50 * time.Minute {
				stamps = append(stamps, tr.Add(d))
			}
		}
		for m := time.January; m <= time.December; m++ {
			last := time.Date(2024, m+1, 0, 0, 0, 0, 0, loc)
			stamps = append(stamps, last.Add(30*time.Minute), last.Add(23*time.Hour+59*time.Minute),
				time.Date(2024, m, 15, 12, 0, 0, 0, loc), time.Date(2024, m, 16, 11, 59, 59, 0, loc))
		}
		vals := make([]string, len(stamps))
		for i, s := range stamps {
			vals[i] = s.Format(time.RFC3339Nano)
		}
		exprs := make([]ursus.Expr, len(ops))
		for i, o := range ops {
			exprs[i] = o.e.Cast(ursus.Int64).Alias(fmt.Sprint(i))
		}
		df, err := calColumn(t, dt, vals...).Select(exprs...).Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", zone, err)
		}
		for i, o := range ops {
			col, err := df.Column[int64](fmt.Sprint(i))
			if err != nil {
				t.Fatal(err)
			}
			var wrong []string
			for j, s := range stamps {
				want, ok := o.oracle(s, loc)
				if !ok {
					t.Fatalf("%s %s: the oracle has no answer for %s", zone, o.name, s.In(loc))
				}
				checked++
				tick, _ := col.Get(j)
				if got, _ := dt.ToTime(tick); !got.Equal(want) {
					wrong = append(wrong, fmt.Sprintf("%s -> %s, want %s", s.In(loc).Format(time.RFC3339),
						got.In(loc).Format(time.RFC3339), want.In(loc).Format(time.RFC3339)))
				}
			}
			if len(wrong) > 0 {
				t.Errorf("%s %s: %d wrong of %d, e.g. %v", zone, o.name, len(wrong), len(stamps),
					wrong[:min(3, len(wrong))])
			}
		}
	}
	if checked < 10_000 {
		t.Errorf("only %d answers checked", checked)
	}
	t.Logf("%d answers checked", checked)
}

// TestACalendarRefusalIsNotPushedPastAJoin: offset_by, round, month_start and
// month_end refuse an answer the type cannot hold, so, like a strict cast, they must
// not run on rows the join removes before them (internal/plan/fallible.go). Pushed
// into the join's left side, each meets a row it refuses.
func TestACalendarRefusalIsNotPushedPastAJoin(t *testing.T) {
	ns := dtype.Datetime(dtype.Nano, "")
	var ticks []int64
	for _, s := range []string{"2024-01-15T00:00:00", "2262-04-11T23:00:00", "1677-09-21T12:00:00"} {
		tm, _ := time.Parse("2006-01-02T15:04:05", s)
		tick, _ := ns.FromTime(tm)
		ticks = append(ticks, tick)
	}
	right := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("r", []int64{9}))
	for _, e := range []ursus.Expr{
		ursus.Col("ts").Dt().OffsetBy(ursus.Every("1mo")),
		ursus.Col("ts").Dt().Round(ursus.Every("1d")),
		ursus.Col("ts").Dt().MonthStart(),
		ursus.Col("ts").Dt().MonthEnd(),
	} {
		df, err := ursus.Frame(data.NewFixed("ts", ns, ticks, bitmap.AllSet(3)),
			ursus.Values("k", []int64{1, 2, 3})).
			Join(right, ursus.JoinOn(ursus.Col("k"))).
			Filter(e.Dt().Year().Gt(2000)).
			Collect(t.Context())
		if err != nil {
			t.Errorf("%s: %v", e, err)
			continue
		}
		if df.Height() != 1 {
			t.Errorf("%s: %d rows, want 1", e, df.Height())
		}
	}
}
