package ursus_test

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// floatsOf is a one-column answer's values, exactly, NaN for a null. The lines are
// split by hand: encoding/csv skips an empty line, which is a null here.
func floatsOf(t *testing.T, q *ursus.LazyFrame) []float64 {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(csvOf(t, q), "\n"), "\n")
	out := make([]float64, 0, len(lines)-1)
	for _, r := range lines[1:] {
		if r == "" {
			out = append(out, math.NaN())
			continue
		}
		f, err := strconv.ParseFloat(r, 64)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

func sameFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] && !(math.IsNaN(a[i]) && math.IsNaN(b[i])) {
			return false
		}
	}
	return true
}

// ymdFrame is a Date column d of y-m-d, a null where ok is false.
func ymdFrame(ymd [][3]int64, ok []bool) *ursus.LazyFrame {
	c := ursus.Col
	ys, ms, ds := make([]int64, len(ymd)), make([]int64, len(ymd)), make([]int64, len(ymd))
	for i, v := range ymd {
		ys[i], ms[i], ds[i] = v[0], v[1], v[2]
	}
	y := ursus.Values("y", ys)
	if ok != nil {
		y = ursus.ValuesNullable("y", ys, ok)
	}
	return ursus.Frame(y, ursus.Values("m", ms), ursus.Values("dd", ds)).
		Select(ursus.MakeDate(c("y"), c("m"), c("dd")).Alias("d"))
}

// TestIsoYearCenturyMillennium is D of v0.6.6-scope.md (step 272). Every answer is
// Polars 1.44.1's.
func TestIsoYearCenturyMillennium(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	lf := ymdFrame([][3]int64{{2024, 12, 30}, {2021, 1, 1}, {1, 1, 1}, {2000, 6, 15}, {2001, 1, 1}, {0, 1, 1}, {100, 1, 1}},
		[]bool{true, true, true, true, true, false, true})
	if got, want := csvOf(t, lf.Select(d.IsoYear().Alias("iso"), d.Century().Alias("c"), d.Millennium().Alias("m"))),
		"iso,c,m\n2025,21,3\n2020,21,3\n1,1,1\n2000,20,2\n2001,21,3\n,,\n99,1,1\n"; got != want {
		t.Errorf("dates:\ngot  %q\nwant %q", got, want)
	}
	// Polars' century and millennium of the first of January of each year: year 0 is
	// in neither, and the centuries before it count down from -1.
	years := []int64{-1101, -1100, -1099, -1001, -1000, -999, -201, -200, -199, -101, -100, -99, -1, 0, 1, 99, 100, 101,
		999, 1000, 1001, 1999, 2000, 2001}
	ymd := make([][3]int64, len(years))
	var want strings.Builder
	want.WriteString("c,m\n")
	for i, y := range years {
		ymd[i] = [3]int64{y, 1, 1}
		want.WriteString(strconv.Itoa(int(math.Floor(float64(y+99)/100))) + "," +
			strconv.Itoa(int(math.Floor(float64(y+999)/1000))) + "\n")
	}
	if got := csvOf(t, ymdFrame(ymd, nil).Select(d.Century().Alias("c"), d.Millennium().Alias("m"))); got != want.String() {
		t.Errorf("around year 0:\ngot  %q\nwant %q", got, want.String())
	}
	dtm := ursus.Frame(ursus.Values("y", []int64{2024})).Select(ursus.MakeDatetime(c("y"), 12, 30, 5, 0, 0, 0).Alias("d"))
	if got := csvOf(t, dtm.Select(d.IsoYear())); got != "d\n2025\n" {
		t.Errorf("a Datetime's ISO year: %q", got)
	}
	tm := ursus.Frame(ursus.Values("y", []int64{1})).Select(ursus.MakeTime(c("y"), 0, 0, 0).Alias("d"))
	if _, err := tm.Select(d.Century()).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "Date or Datetime") {
		t.Errorf("a Time's century: %v", err)
	}
}

// TestBusinessDays: is_business_day and add_business_days, on Polars' dates: Friday
// the 5th, Saturday the 6th and Sunday the 7th of January 2024, a null, and
// Christmas, a Wednesday.
func TestBusinessDays(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	lf := ymdFrame([][3]int64{{2024, 1, 5}, {2024, 1, 6}, {2024, 1, 7}, {2024, 1, 1}, {2024, 12, 25}},
		[]bool{true, true, true, false, true})
	christmas := ursus.BusinessCalendar{Holidays: []time.Time{time.Date(2024, 12, 25, 0, 0, 0, 0, time.UTC)}}
	monSat := ursus.BusinessCalendar{WeekMask: [7]bool{true, true, true, true, true, true, false}}
	f := func(e ursus.Expr) ursus.Expr { return e.Dt().Strftime("%m-%d") }
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"is_business_day", d.IsBusinessDay(), "true\nfalse\nfalse\n\ntrue"},
		{"a holiday", d.IsBusinessDay(christmas), "true\nfalse\nfalse\n\nfalse"},
		{"Monday to Saturday", d.IsBusinessDay(monSat), "true\ntrue\nfalse\n\ntrue"},
		{"one forward", f(d.AddBusinessDays(1, ursus.RollForward)), "01-08\n01-09\n01-09\n\n12-26"},
		{"one backward", f(d.AddBusinessDays(1, ursus.RollBackward)), "01-08\n01-08\n01-08\n\n12-26"},
		{"one back, forward", f(d.AddBusinessDays(-1, ursus.RollForward)), "01-04\n01-05\n01-05\n\n12-24"},
		{"none forward", f(d.AddBusinessDays(0, ursus.RollForward)), "01-05\n01-08\n01-08\n\n12-25"},
		{"none backward", f(d.AddBusinessDays(0, ursus.RollBackward)), "01-05\n01-05\n01-05\n\n12-25"},
		{"a count a row", f(d.AddBusinessDays(ursus.Lit(int64(1)).Add(ursus.IntRange(0, ursus.Len(), 1)),
			ursus.RollForward)), "01-08\n01-10\n01-11\n\n01-01"}, // Christmas and five: New Year's Day
	} {
		if got, want := csvOf(t, lf.Select(tc.e.Alias("r"))), "r\n"+tc.want+"\n"; got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, want)
		}
	}
	nulls := ursus.Frame(ursus.ValuesNullable("n", []int64{1, 0}, []bool{true, false})).
		Select(ursus.MakeDate(2024, 1, 5).Alias("d"), c("n"))
	if got := csvOf(t, nulls.Select(f(d.AddBusinessDays(c("n"), ursus.RollRaise)).Alias("r"))); got != "r\n01-08\n\n" {
		t.Errorf("a null count: %q", got)
	}
	// Past a holiday, a thousand on and back, and a Datetime keeping its time of day.
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"past Christmas", ymdFrame([][3]int64{{2024, 12, 24}}, nil).Select(d.AddBusinessDays(1, ursus.RollRaise, christmas)), "d\n2024-12-26\n"},
		{"a thousand on", ymdFrame([][3]int64{{2024, 1, 1}}, nil).Select(d.AddBusinessDays(1000, ursus.RollRaise)), "d\n2027-11-01\n"},
		{"a thousand back", ymdFrame([][3]int64{{2024, 1, 1}}, nil).Select(d.AddBusinessDays(-1000, ursus.RollRaise)), "d\n2020-03-02\n"},
		{"a Datetime", ursus.Frame(ursus.Values("y", []int64{2024})).
			Select(ursus.MakeDatetime(c("y"), 1, 5, 13, 30, 0, 0).Alias("d")).
			Select(d.AddBusinessDays(1, ursus.RollRaise).Dt().Strftime("%F %T")), "d\n2024-01-08 13:30:00\n"},
		{"a Saturday's Datetime", ursus.Frame(ursus.Values("y", []int64{2024})).
			Select(ursus.MakeDatetime(c("y"), 1, 6, 13, 30, 0, 0).Alias("d")).Select(d.IsBusinessDay()), "d\nfalse\n"},
		// 23:30 on a Friday in Tokyo is 14:30 UTC: its local date moves, to Monday.
		{"a zoned Datetime", ursus.Frame(ursus.Values("y", []int64{2024})).
			Select(ursus.MakeDatetime(c("y"), 1, 5, 23, 30, 0, 0).Dt().ReplaceTimeZone("Asia/Tokyo").Alias("d")).
			Select(d.AddBusinessDays(1, ursus.RollRaise).Dt().Strftime("%F %T")), "d\n2024-01-08 23:30:00\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// Whole weeks jumped across holidays, forward and back, and a mask of five days
	// that are not Monday to Friday.
	winter := ursus.BusinessCalendar{Holidays: []time.Time{time.Date(2024, 12, 25, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}}
	odd := winter
	odd.WeekMask = [7]bool{true, true, true, false, true, true, false}
	for _, tc := range []struct {
		name string
		from [3]int64
		n    int
		cal  ursus.BusinessCalendar
		want string
	}{
		{"ten over two holidays", [3]int64{2024, 12, 20}, 10, winter, "2025-01-07"},
		{"ten back over them", [3]int64{2025, 1, 7}, -10, winter, "2024-12-20"},
		{"Thursdays off", [3]int64{2024, 12, 20}, 23, odd, "2025-01-24"},
	} {
		q := ymdFrame([][3]int64{tc.from}, nil).Select(d.AddBusinessDays(tc.n, ursus.RollRaise, tc.cal))
		if got := csvOf(t, q); got != "d\n"+tc.want+"\n" {
			t.Errorf("%s: got %q, want %s", tc.name, got, tc.want)
		}
	}
	all7 := ursus.BusinessCalendar{WeekMask: [7]bool{true, true, true, true, true, true, true}}
	ny := func(day, hour int64) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("y", []int64{2024})).
			Select(ursus.MakeDatetime(c("y"), 11, day, hour, 30, 0, 0).Dt().ReplaceTimeZone("America/New_York").Alias("d"))
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"a Saturday, raised":  {lf.Select(d.AddBusinessDays(1, ursus.RollRaise)), "2024-01-06 is not on a business day"},
		"into a fold":         {ny(2, 1).Select(d.AddBusinessDays(1, ursus.RollRaise, all7)), "ambiguous"},
		"a roll that is none": {lf.Select(d.AddBusinessDays(1, "sideways")), "not a roll"},
		"a float count":       {lf.Select(d.AddBusinessDays(1.5, ursus.RollForward)), "integer count"},
		"a Time":              {ursus.Frame(ursus.Values("y", []int64{1})).Select(ursus.MakeTime(c("y"), 0, 0, 0).Alias("d").Dt().IsBusinessDay()), "Date or Datetime"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestReplace: dt.replace. Every answer is Polars 1.44.1's.
func TestReplace(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	lf := ursus.Frame(ursus.ValuesNullable("y", []int64{2024, 2023, 0}, []bool{true, true, false}),
		ursus.Values("m", []int64{1, 2, 1}), ursus.Values("dd", []int64{31, 28, 1}),
		ursus.Values("h", []int64{10, 0, 0}), ursus.Values("mi", []int64{20, 0, 0}), ursus.Values("s", []int64{30, 0, 0}),
		ursus.Values("us", []int64{123456, 0, 0}), ursus.ValuesNullable("day", []int64{1, 0, 3}, []bool{true, false, true})).
		Select(ursus.MakeDatetime(c("y"), c("m"), c("dd"), c("h"), c("mi"), c("s"), c("us")).Alias("d"), c("day"))
	text := func(e ursus.Expr) ursus.Expr { return e.Dt().Strftime("%F %T%.6f").Alias("d") }
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"hour and minute", d.Replace(ursus.DateTimeParts{Hour: 23, Minute: 0}),
			"2024-01-31 23:00:30.123456\n2023-02-28 23:00:00.000000\n"},
		{"the microsecond", d.Replace(ursus.DateTimeParts{Microsecond: 5}),
			"2024-01-31 10:20:30.000005\n2023-02-28 00:00:00.000005\n"},
		// A null in the column keeps the value's own day.
		{"a day a row", d.Replace(ursus.DateTimeParts{Day: c("day")}),
			"2024-01-01 10:20:30.123456\n2023-02-28 00:00:00.000000\n"},
		{"year 0", d.Replace(ursus.DateTimeParts{Year: 0, Month: 3}),
			"0000-03-31 10:20:30.123456\n0000-03-28 00:00:00.000000\n"},
	} {
		if got, want := csvOf(t, lf.Select(text(tc.e))), "d\n"+tc.want+"\n"; got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, want)
		}
	}
	// Year 0 is a leap year, as Polars' calendar has it: its ticks are Polars'.
	if got, want := csvOf(t, lf.Select(d.Replace(ursus.DateTimeParts{Year: 0, Month: 3}).Cast(ursus.Int64))),
		"d\n-62159405969876544\n-62159702400000000\n\n"; got != want {
		t.Errorf("year 0's ticks:\ngot  %q\nwant %q", got, want)
	}
	// A Datetime(ns) keeps its nanoseconds, but where the microsecond is set; a Date's
	// time is not read.
	ns := ursus.Frame(ursus.Values("d", []time.Time{time.Date(2024, 1, 1, 0, 0, 1, 123456789, time.UTC)})).
		Select(c("d").Dt().ReplaceTimeZone(""))
	ticks := func(e ursus.Expr) ursus.Expr { return e.Cast(ursus.Int64).Alias("d") }
	if got := csvOf(t, ns.Select(ticks(d.Replace(ursus.DateTimeParts{Hour: 5})))); got != "d\n1704085201123456789\n" {
		t.Errorf("nanoseconds kept: %q", got)
	}
	if got := csvOf(t, ns.Select(ticks(d.Replace(ursus.DateTimeParts{Microsecond: 5})))); got != "d\n1704067201000005000\n" {
		t.Errorf("nanoseconds set: %q", got)
	}
	ms := ns.Select(c("d").Cast(ursus.Datetime(ursus.Milli, "")))
	if got := csvOf(t, ms.Select(ticks(d.Replace(ursus.DateTimeParts{Microsecond: 999999})))); got != "d\n1704067201999\n" {
		t.Errorf("milliseconds: %q", got)
	}
	date := ymdFrame([][3]int64{{2024, 1, 31}}, nil)
	if got := csvOf(t, date.Select(d.Replace(ursus.DateTimeParts{Hour: 4, Day: 2}))); got != "d\n2024-01-02\n" {
		t.Errorf("a Date: %q", got)
	}
	// In New York 01:30 on the 3rd of November is read twice.
	nyFold := ursus.Frame(ursus.Values("y", []int64{2024})).
		Select(ursus.MakeDatetime(c("y"), 11, 3, 0, 30, 0, 0).Dt().ReplaceTimeZone("America/New_York").Alias("d"))
	utc := func(e ursus.Expr) ursus.Expr {
		return e.Dt().ConvertTimeZone("UTC").Dt().Strftime("%F %T").Alias("d")
	}
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"earliest": {utc(d.Replace(ursus.DateTimeParts{Hour: 1}, ursus.AmbiguousEarliest)), "d\n2024-11-03 05:30:00\n"},
		"latest":   {utc(d.Replace(ursus.DateTimeParts{Hour: 1}, ursus.AmbiguousLatest)), "d\n2024-11-03 06:30:00\n"},
		"null":     {utc(d.Replace(ursus.DateTimeParts{Hour: 1}, ursus.AmbiguousNull)), "d\n\n"},
	} {
		if got := csvOf(t, nyFold.Select(tc.e)); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", name, got, tc.want)
		}
	}
	nyGap := ursus.Frame(ursus.Values("y", []int64{2024})).
		Select(ursus.MakeDatetime(c("y"), 3, 10, 1, 30, 0, 0).Dt().ReplaceTimeZone("America/New_York").Alias("d"))
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"the 30th of February": {lf.Select(d.Replace(ursus.DateTimeParts{Day: 30})), "year 2023, month 2, day 30"},
		"February the 31st":    {lf.Select(d.Replace(ursus.DateTimeParts{Month: 2})), "year 2024, month 2, day 31"},
		"a 13th month":         {lf.Select(d.Replace(ursus.DateTimeParts{Month: 13})), "month 13"},
		"a 60th second":        {lf.Select(d.Replace(ursus.DateTimeParts{Second: 60})), "second 60"},
		"an hour before 0":     {lf.Select(d.Replace(ursus.DateTimeParts{Hour: -1})), "hour -1"},
		"a Date's April 31st":  {date.Select(d.Replace(ursus.DateTimeParts{Month: 4})), "month 4, day 31"},
		"a fold":               {nyFold.Select(d.Replace(ursus.DateTimeParts{Hour: 1})), "ambiguous"},
		"a gap":                {nyGap.Select(d.Replace(ursus.DateTimeParts{Hour: 2})), "does not exist"},
		"a float":              {lf.Select(d.Replace(ursus.DateTimeParts{Hour: 1.5})), "integer"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
	if got := csvOf(t, nyGap.Select(utc(d.Replace(ursus.DateTimeParts{Hour: 2}, ursus.NonExistentNull)))); got != "d\n\n" {
		t.Errorf("a gap made null: %q", got)
	}
}

// TestZoneOffsets: base_utc_offset and dst_offset, in milliseconds. Every answer is
// Polars 1.44.1's, Dublin's negative winter daylight-saving time among them.
func TestZoneOffsets(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	at := func(zone string) *ursus.LazyFrame {
		return ursus.Frame(ursus.ValuesNullable("m", []int64{1, 7, 1}, []bool{true, true, false})).
			Select(ursus.MakeDatetime(2024, c("m"), 15, 12, 0, 0, 0).Dt().ReplaceTimeZone(zone).Alias("d"))
	}
	ms := func(e ursus.Expr, name string) ursus.Expr { return e.Cast(ursus.Int64).Alias(name) }
	const h = 3_600_000
	for _, tc := range []struct {
		zone string
		want string
	}{
		{"America/New_York", "b,o\n-18000000,0\n-18000000,3600000\n,\n"},
		{"Europe/Dublin", "b,o\n3600000,-3600000\n3600000,0\n,\n"},
		{"Australia/Lord_Howe", "b,o\n37800000,1800000\n37800000,0\n,\n"},
		{"Asia/Kolkata", "b,o\n19800000,0\n19800000,0\n,\n"},
		{"UTC", "b,o\n0,0\n0,0\n,\n"},
	} {
		if got := csvOf(t, at(tc.zone).Select(ms(d.BaseUtcOffset(), "b"), ms(d.DstOffset(), "o"))); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.zone, got, tc.want)
		}
	}
	_ = h
	// Casablanca's clocks go back an hour in Ramadan: 2024-03-20 is in it.
	casa := ursus.Frame(ursus.Values("y", []int64{2024})).
		Select(ursus.MakeDatetime(c("y"), 3, 20, 12, 0, 0, 0).Dt().ReplaceTimeZone("Africa/Casablanca").Alias("d"))
	if got := csvOf(t, casa.Select(ms(d.BaseUtcOffset(), "b"), ms(d.DstOffset(), "o"))); got != "b,o\n3600000,-3600000\n" {
		t.Errorf("Ramadan: %q", got)
	}
	got, err := at("UTC").Select(d.DstOffset()).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Duration(ursus.Milli) {
		t.Errorf("a Duration(ms): %v, %v", got, err)
	}
	naive := ursus.Frame(ursus.Values("y", []int64{2024})).Select(ursus.MakeDatetime(c("y"), 1, 1, 0, 0, 0, 0).Alias("d"))
	if _, err := naive.Select(d.BaseUtcOffset()).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "time zone") {
		t.Errorf("a naive Datetime: %v", err)
	}
}

// TestTimeUnitsTotalsAndEpochs: with_time_unit, the totals and their fractions,
// second with its fraction, and epoch in each unit. Every answer is Polars 1.44.1's,
// every float to the bit.
func TestTimeUnitsTotalsAndEpochs(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	day := ursus.Frame(ursus.Values("y", []int64{2024})).Select(ursus.MakeDatetime(c("y"), 1, 1, 0, 0, 0, 0).Alias("d"))
	for _, tc := range []struct {
		unit ursus.TimeUnit
		want string
	}{{ursus.Milli, "1704067200000000"}, {ursus.Nano, "1704067200000000"}} {
		e := d.WithTimeUnit(tc.unit)
		if got := csvOf(t, day.Select(e.Cast(ursus.Int64))); got != "d\n"+tc.want+"\n" {
			t.Errorf("with_time_unit(%s): %q", tc.unit, got)
		}
		got, err := day.Select(e).Collect(t.Context())
		if err != nil || got.Schema().Field(0).Type != ursus.Datetime(tc.unit, "") {
			t.Errorf("with_time_unit(%s)'s type: %v, %v", tc.unit, got, err)
		}
	}
	got, err := day.Select(d.ReplaceTimeZone("Asia/Tokyo").Dt().WithTimeUnit(ursus.Milli)).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Datetime(ursus.Milli, "Asia/Tokyo") {
		t.Errorf("with_time_unit keeps the zone: %v, %v", got, err)
	}
	if got := csvOf(t, ursus.Frame(ursus.Values("s", []int64{1})).
		Select(ursus.MakeDuration(ursus.DurationParts{Seconds: c("s")}).Dt().WithTimeUnit(ursus.Milli).Dt().TotalSeconds())); got != "duration\n1000\n" {
		t.Errorf("a Duration's unit: %q", got)
	}

	// Durations of microseconds: 1s 500us, a microsecond short of minus a day, a null,
	// and seven more.
	us := []int64{1_000_500, -86_399_999_999, 0, 1, 3, 7, 999_999, 123_456_789, -1, 1_000_000_000_001}
	ok := []bool{true, true, false, true, true, true, true, true, true, true}
	du := ursus.Frame(ursus.ValuesNullable("us", us, ok)).
		Select(ursus.MakeDuration(ursus.DurationParts{Microseconds: c("us")}).Alias("d"))
	if got, want := csvOf(t, du.Head(3).Select(d.TotalMilliseconds().Alias("ms"), d.TotalMicroseconds().Alias("us"),
		d.TotalNanoseconds().Alias("ns"))), "ms,us,ns\n1000,1000500,1000500000\n-86399999,-86399999999,-86399999999000\n,,\n"; got != want {
		t.Errorf("totals:\ngot  %q\nwant %q", got, want)
	}
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want []float64
	}{
		{"days", d.TotalDaysFractional(), []float64{1.1579861111111111e-05, -0.9999999999884259}},
		{"hours", d.TotalHoursFractional(), []float64{0.00027791666666666665, -23.99999999972222}},
		{"minutes", d.TotalMinutesFractional(), []float64{0.016675, -1439.9999999833333}},
		{"seconds", d.TotalSecondsFractional(), []float64{1.0005, -86399.99999899999, math.NaN(), 1e-06, 3e-06, 7e-06,
			0.999999, 123.456789, -1e-06, 1000000.000001}},
		{"milliseconds", d.TotalMillisecondsFractional(), []float64{1000.5, -86399999.999}},
		{"microseconds", d.TotalMicrosecondsFractional(), []float64{1000500.0, -86399999999.0}},
		{"nanoseconds", d.TotalNanosecondsFractional(), []float64{1000500000.0, -86399999999000.0}},
	} {
		got := floatsOf(t, du.Select(tc.e))[:len(tc.want)]
		if !sameFloats(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	// A Duration(ms): a millisecond's reciprocal, and nanoseconds by multiplying.
	dms := du.Head(2).Select(c("d").Cast(ursus.Duration(ursus.Milli)))
	if got := floatsOf(t, dms.Select(d.TotalSecondsFractional())); !sameFloats(got, []float64{1.0, -86399.999}) {
		t.Errorf("seconds of milliseconds: %v", got)
	}
	if got := csvOf(t, dms.Select(d.TotalNanoseconds())); got != "d\n1000000000\n-86399999000000\n" {
		t.Errorf("nanoseconds of milliseconds: %q", got)
	}

	// second with its fraction, in microseconds and nanoseconds, and of a Time.
	sec := ursus.Frame(ursus.ValuesNullable("s", []int64{1, 59, 0}, []bool{true, true, false}),
		ursus.Values("u", []int64{500, 999999, 0})).
		Select(ursus.MakeDatetime(2024, 1, 1, 0, 0, c("s"), c("u")).Alias("d"))
	if got := floatsOf(t, sec.Select(d.SecondFractional())); !sameFloats(got, []float64{1.0005, 59.999999, math.NaN()}) {
		t.Errorf("second, fractional: %v", got)
	}
	nanos := []int64{123456789, 999999999, 1, 500000000, 100000001, 333333333, 0}
	times := make([]time.Time, len(nanos))
	for i, n := range nanos {
		times[i] = time.Date(2024, 1, 1, 0, 1, 0, int(n), time.UTC)
	}
	if got := floatsOf(t, ursus.Frame(ursus.Values("d", times)).Select(d.SecondFractional())); !sameFloats(got,
		[]float64{0.12345678900000001, 0.999999999, 1e-09, 0.5, 0.100000001, 0.333333333, 0.0}) {
		t.Errorf("second, fractional, of nanoseconds: %v", got)
	}
	tm := ursus.Frame(ursus.Values("h", []int64{1})).Select(ursus.MakeTime(c("h"), 2, 3, 250000).Alias("d"))
	if got := floatsOf(t, tm.Select(d.SecondFractional())); !sameFloats(got, []float64{3.25}) {
		t.Errorf("second, fractional, of a Time: %v", got)
	}

	// epoch, floored: a microsecond before 1970 is -1 in every coarser unit.
	ep := ursus.Frame(ursus.ValuesNullable("y", []int64{2024, 1969, 0}, []bool{true, true, false}),
		ursus.Values("m", []int64{1, 12, 1}), ursus.Values("dd", []int64{1, 31, 1}),
		ursus.Values("h", []int64{0, 23, 0}), ursus.Values("mi", []int64{0, 59, 0}), ursus.Values("s", []int64{1, 59, 0}),
		ursus.Values("u", []int64{500, 999999, 0})).
		Select(ursus.MakeDatetime(c("y"), c("m"), c("dd"), c("h"), c("mi"), c("s"), c("u")).Alias("d"))
	if got, want := csvOf(t, ep.Select(d.EpochIn(ursus.Nano).Alias("ns"), d.EpochIn(ursus.Micro).Alias("us"),
		d.EpochIn(ursus.Milli).Alias("ms"), d.EpochIn(ursus.Second).Alias("s"), d.EpochDays().Alias("d"),
		d.Timestamp(ursus.Milli).Alias("ts"))),
		"ns,us,ms,s,d,ts\n1704067201000500000,1704067201000500,1704067201000,1704067201,19723,1704067201000\n"+
			"-1000,-1,-1,-1,-1,-1\n,,,,,\n"; got != want {
		t.Errorf("epochs:\ngot  %q\nwant %q", got, want)
	}
	dates := ymdFrame([][3]int64{{2024, 1, 2}, {1969, 12, 31}}, nil)
	if got, want := csvOf(t, dates.Select(d.EpochIn(ursus.Nano).Alias("ns"), d.EpochIn(ursus.Second).Alias("s"),
		d.EpochDays().Alias("d"))), "ns,s,d\n1704153600000000000,1704153600,19724\n-86400000000000,-86400,-1\n"; got != want {
		t.Errorf("a Date's epochs:\ngot  %q\nwant %q", got, want)
	}
	tokyo := day.Select(c("d").Dt().ReplaceTimeZone("Asia/Tokyo"))
	if got := csvOf(t, tokyo.Select(d.EpochIn(ursus.Second))); got != "d\n1704034800\n" {
		t.Errorf("a zoned epoch: %q", got)
	}
	got, err = dates.Select(d.EpochDays()).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Int32 {
		t.Errorf("days are an Int32: %v, %v", got, err)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"timestamp in seconds": {day.Select(d.Timestamp(ursus.Second)), "milliseconds, microseconds or nanoseconds"},
		"a Time's epoch":       {tm.Select(d.EpochIn(ursus.Second)), "Date or Datetime"},
		"a Date's unit":        {dates.Select(d.WithTimeUnit(ursus.Milli)), "Datetime or Duration"},
		"a Date's second":      {dates.Select(d.SecondFractional()), "Datetime or Time"},
		"a Datetime's total":   {day.Select(d.TotalMilliseconds()), "Duration"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
