package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// D of v0.6.6-scope.md (step 272): iso_year, century and millennium, the business
// days, dt.replace, the zone offsets, and a Duration's totals, the fractional second
// and the epochs. Their answers are Polars 2.1.0's, recorded in the dtmore group of
// testdata/oracle and spelled in oracle_dtmore_test.go since step 496, which took
// this file's 90 hand-typed lines of values. What stays asserts the refusals, and
// holds to Go's own time package what Polars 2 has no answer for: with_time_unit
// (gone from Polars 2, a tick differential here), Dublin's and Casablanca's offsets
// (the zone database's form differs between Debian and Ubuntu's CI), and a gap made
// null (dt.replace lost non_existent).

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

func TestIsoYearCenturyMillennium(t *testing.T) {
	c := ursus.Col
	tm := ursus.Frame(ursus.Values("y", []int64{1})).Select(ursus.MakeTime(c("y"), 0, 0, 0).Alias("d"))
	if _, err := tm.Select(c("d").Dt().Century()).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "Date or Datetime") {
		t.Errorf("a Time's century: %v", err)
	}
}

func TestBusinessDays(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	lf := ymdFrame([][3]int64{{2024, 1, 5}, {2024, 1, 6}, {2024, 1, 7}, {2024, 12, 25}}, nil)
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

// TestReplace: dt.replace's refusals, and a wall clock that does not exist made null,
// which Polars 2 has no spelling for: Go's time package says whether 02:30 on the 10th
// of March 2024 exists in New York.
func TestReplace(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	lf := ursus.Frame(ursus.Values("y", []int64{2024, 2023}), ursus.Values("m", []int64{1, 2}), ursus.Values("dd", []int64{31, 28})).
		Select(ursus.MakeDatetime(c("y"), c("m"), c("dd"), 10, 20, 30, 0).Alias("d"))
	date := ymdFrame([][3]int64{{2024, 1, 31}}, nil)
	nyFold := ursus.Frame(ursus.Values("y", []int64{2024})).
		Select(ursus.MakeDatetime(c("y"), 11, 3, 0, 30, 0, 0).Dt().ReplaceTimeZone("America/New_York").Alias("d"))
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
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// Go normalises a wall clock in a gap to the hour after it, so the hour it reads
	// back says whether the clock exists.
	exists := time.Date(2024, 3, 10, 2, 30, 0, 0, newYork).Hour() == 2
	got, err := nyGap.Select(d.Replace(ursus.DateTimeParts{Hour: 2}, ursus.NonExistentNull)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if valid := got.ColumnAt(0).IsValid(0); valid != exists {
		t.Errorf("02:30 in the gap made null: present %v, where Go says the clock exists: %v", valid, exists)
	}
}

// TestZoneOffsets: base_utc_offset and dst_offset of Dublin and Casablanca, whose
// daylight-saving time is negative in the zone database's main form, which Polars
// bundles and Debian installs, and positive in its rearguard form, which Ubuntu
// installs: there Dublin's winter is standard time and its summer an hour of daylight
// saving. Go reads the system's, so the answer is each form's, and the oracle cannot
// record one; either way the two sum to the offset in force, and the saving is zero
// exactly where Go's IsDST is false. A naive Datetime is refused.
func TestZoneOffsets(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	for _, zone := range []string{"Europe/Dublin", "Africa/Casablanca"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		// The 15th of January, of July, and the 20th of March 2024, which is in
		// Ramadan, when Casablanca's clocks go back an hour.
		months, days := []int64{1, 7, 3}, []int64{15, 15, 20}
		at := ursus.Frame(ursus.Values("m", months), ursus.Values("dd", days)).
			Select(ursus.MakeDatetime(2024, c("m"), c("dd"), 12, 0, 0, 0).Dt().ReplaceTimeZone(zone).Alias("d"))
		got, err := at.Select(d.BaseUtcOffset().Cast(ursus.Int64).Alias("b"), d.DstOffset().Cast(ursus.Int64).Alias("o")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		b, _ := got.Column[int64]("b")
		o, _ := got.Column[int64]("o")
		for i := range months {
			wall := time.Date(2024, time.Month(months[i]), int(days[i]), 12, 0, 0, 0, loc)
			_, offset := wall.Zone()
			bv, _ := b.Get(i)
			ov, _ := o.Get(i)
			if bv+ov != int64(offset)*1000 {
				t.Errorf("%s at %v: base %d + dst %d ms, Go's offset %d s", zone, wall, bv, ov, offset)
			}
			if (ov != 0) != wall.IsDST() {
				t.Errorf("%s at %v: dst %d ms, Go's IsDST %v", zone, wall, ov, wall.IsDST())
			}
		}
	}
	naive := ursus.Frame(ursus.Values("y", []int64{2024})).Select(ursus.MakeDatetime(c("y"), 1, 1, 0, 0, 0, 0).Alias("d"))
	if _, err := naive.Select(d.BaseUtcOffset()).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "time zone") {
		t.Errorf("a naive Datetime: %v", err)
	}
}

// TestTimeUnitsTotalsAndEpochs: with_time_unit, gone from Polars 2, reads the same
// ticks in another unit, so its answer's ticks are its operand's, and a Duration's
// total in the new unit is its ticks in that unit; and the refusals.
func TestTimeUnitsTotalsAndEpochs(t *testing.T) {
	c := ursus.Col
	d := c("d").Dt()
	day := ursus.Frame(ursus.Values("y", []int64{2024})).Select(ursus.MakeDatetime(c("y"), 1, 1, 0, 0, 0, 0).Alias("d"))
	ticks := csvOf(t, day.Select(c("d").Cast(ursus.Int64)))
	for _, unit := range []ursus.TimeUnit{ursus.Milli, ursus.Nano} {
		e := d.WithTimeUnit(unit)
		if got := csvOf(t, day.Select(e.Cast(ursus.Int64))); got != ticks {
			t.Errorf("with_time_unit(%s) changed the ticks: %q, from %q", unit, got, ticks)
		}
		got, err := day.Select(e).Collect(t.Context())
		if err != nil || got.Schema().Field(0).Type != ursus.Datetime(unit, "") {
			t.Errorf("with_time_unit(%s)'s type: %v, %v", unit, got, err)
		}
	}
	got, err := day.Select(d.ReplaceTimeZone("Asia/Tokyo").Dt().WithTimeUnit(ursus.Milli)).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Datetime(ursus.Milli, "Asia/Tokyo") {
		t.Errorf("with_time_unit keeps the zone: %v, %v", got, err)
	}
	// A second of microseconds read as milliseconds is a thousand seconds: the ticks
	// over a millisecond's share of a second.
	sec := ursus.Frame(ursus.Values("s", []int64{1})).Select(ursus.MakeDuration(ursus.DurationParts{Seconds: c("s")}).Alias("d"))
	usTicks := typedCells[int64](t, sec.Select(c("d").Cast(ursus.Int64)))[0].(int64)
	if got := typedCells[int64](t, sec.Select(d.WithTimeUnit(ursus.Milli).Dt().TotalSeconds())); !sameCells(got, []any{usTicks / 1000}) {
		t.Errorf("a Duration's unit: %v, want %d", got, usTicks/1000)
	}

	tm := ursus.Frame(ursus.Values("h", []int64{1})).Select(ursus.MakeTime(c("h"), 2, 3, 250000).Alias("d"))
	dates := ymdFrame([][3]int64{{2024, 1, 2}}, nil)
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
