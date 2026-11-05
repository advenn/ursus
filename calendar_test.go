package ursus_test

// The calendar (step 145): OffsetBy, Round, MonthStart, MonthEnd, IsLeapYear and
// ConvertTimeZone, and the wall-clock reading they share with Truncate and
// GroupByDynamic. Their answers are Polars 2.1.0's, recorded in the calendar group of
// testdata/oracle and spelled in oracle_calendar_test.go since step 498, which took
// this file's by-hand tables. What stays asserts the refusals; holds Round by a
// time.Duration on a zoned column, the epoch's grid, to Go's time package, Polars'
// round having no spelling for it; and sweeps the rules across every transition of
// seven zones, against an oracle that tries each offset a zone uses near a wall clock
// and knows nothing of how the kernel reads one.

import (
	"errors"
	"fmt"
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

var (
	utcUs   = dtype.Datetime(dtype.Micro, "UTC")
	naiveUs = dtype.Datetime(dtype.Micro, "")
)

// calCase is a question of column ts refused while the query is planned, its error
// naming refuse.
type calCase struct {
	name   string
	dt     dtype.DataType
	in     []string
	f      func(ursus.DtExpr) ursus.Expr
	refuse string
}

func runCalCases(t *testing.T, cases []calCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lf := calColumn(t, c.dt, c.in...).Select(c.f(ursus.Col("ts").Dt()).Alias("ts"))
			_, err := lf.CollectSchema(t.Context())
			if err == nil || !strings.Contains(err.Error(), c.refuse) {
				t.Fatalf("want a plan-time refusal naming %q, got %v", c.refuse, err)
			}
			assertUserError(t, lf, c.refuse)
		})
	}
}

// TestCalendarRefusals: what OffsetBy, MonthStart, Round, IsLeapYear and
// ConvertTimeZone refuse while the query is planned.
func TestCalendarRefusals(t *testing.T) {
	nsTime := dtype.Time(dtype.Nano)
	one := []string{"2024-01-01T00:00:00Z"}
	runCalCases(t, []calCase{
		{name: "an hour on a Date", dt: dtype.Date, in: []string{"2024-01-01"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(time.Hour) },
			refuse: "finer than Date holds"},
		{name: "a nanosecond on microseconds", dt: utcUs, in: one,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(time.Nanosecond) },
			refuse: "finer than"},
		{name: "offset_by of a Time", dt: nsTime,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(time.Hour) },
			refuse: "requires a Date or Datetime"},
		{name: "an interval that does not parse", dt: utcUs,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.OffsetBy(ursus.Every("1mo-1d")) },
			refuse: "cannot parse interval"},
		{name: "month_start of a Time", dt: nsTime,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.MonthStart() },
			refuse: "requires a Date or Datetime"},
		{name: "round of a Time", dt: nsTime,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.Round(time.Hour) },
			refuse: "round is not defined for Time"},
		{name: "a zero interval", dt: utcUs,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.Round(time.Duration(0)) },
			refuse: "round needs a positive interval"},
		{name: "round of a Duration column", dt: dtype.Duration(dtype.Nano),
			f:      func(d ursus.DtExpr) ursus.Expr { return d.Round(time.Hour) },
			refuse: "requires a Date, Time or Datetime"},
		{name: "is_leap_year of a Duration column", dt: dtype.Duration(dtype.Nano), in: []string{"null"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.IsLeapYear() },
			refuse: "requires a Date or Datetime"},
		{name: "convert_time_zone of a naive Datetime", dt: naiveUs,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ConvertTimeZone("UTC") },
			refuse: "requires a Datetime with a time zone"},
		{name: "convert_time_zone of a Date", dt: dtype.Date,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ConvertTimeZone("UTC") },
			refuse: "requires a Datetime"},
		{name: "an unknown zone", dt: utcUs,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ConvertTimeZone("Mars/Olympus_Mons") },
			refuse: "not known"},
		{name: "no zone", dt: utcUs,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ConvertTimeZone("") },
			refuse: "needs a time zone"},
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

// TestRoundByADurationIsTheEpochsGrid: Round by a time.Duration rounds on the epoch's
// grid, where an Interval rounds on the zone's wall clock (Round's doc), so an hour at
// Kolkata's +05:30 lands on the half hour. Polars' round has no spelling for the
// epoch's grid on a zoned column; the oracle is Go's time package: the floor, on the
// epoch's grid of the step, of the instant plus half the step.
func TestRoundByADurationIsTheEpochsGrid(t *testing.T) {
	stamps := []string{"2024-06-01T05:17:00Z", "2024-03-10T06:40:00Z", "2024-11-03T05:50:00Z", "2024-04-06T15:20:00Z",
		"1969-12-31T22:50:00Z", "2024-01-01T00:45:00.5Z"}
	floorDiv := func(a, b int64) int64 {
		q := a / b
		if a%b != 0 && (a < 0) != (b < 0) {
			q--
		}
		return q
	}
	for _, zone := range []string{"Asia/Kolkata", "America/New_York", "Australia/Lord_Howe"} {
		dt := dtype.Datetime(dtype.Micro, zone)
		for _, every := range []time.Duration{time.Hour, 90 * time.Minute, 15 * time.Minute} {
			got := typedCells[int64](t, calColumn(t, dt, stamps...).Select(ursus.Col("ts").Dt().Round(every).Cast(ursus.Int64)))
			want := make([]any, len(stamps))
			for i, s := range stamps {
				tm, err := time.Parse(time.RFC3339Nano, s)
				if err != nil {
					t.Fatal(err)
				}
				step := every.Microseconds()
				want[i] = floorDiv(tm.UnixMicro()+step/2, step) * step
			}
			if !sameCells(got, want) {
				t.Errorf("%s round %s: got %v, want %v", zone, every, got, want)
			}
		}
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

// TestACalendarRefusalIsNotPushedPastAJoin: offset_by, round, month_start,
// month_end and combine refuse an answer the type cannot hold, so, like a strict cast, they must
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
		// 23:59 on 2262-04-11 is past Datetime(ns)'s last instant (step 230).
		ursus.Col("ts").Dt().Combine(ursus.Lit(int64((23*3600 + 59*60) * 1e9)).Cast(dtype.Time(dtype.Nano))),
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
