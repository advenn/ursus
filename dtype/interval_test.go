package dtype_test

import (
	"errors"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

func TestEveryParses(t *testing.T) {
	for _, c := range []struct {
		in           string
		months, days int32
		nanos        time.Duration
		calendar     bool
	}{
		{"1ns", 0, 0, time.Nanosecond, false},
		{"5us", 0, 0, 5 * time.Microsecond, false},
		{"250ms", 0, 0, 250 * time.Millisecond, false},
		{"30s", 0, 0, 30 * time.Second, false},
		{"15m", 0, 0, 15 * time.Minute, false},
		{"6h", 0, 0, 6 * time.Hour, false},
		{"2d", 0, 2, 0, true},
		{"1w", 0, 7, 0, true},
		{"3mo", 3, 0, 0, true},
		{"1q", 3, 0, 0, true},
		{"1y", 12, 0, 0, true},
		// Compound, and the ordering of components does not matter.
		{"1h30m", 0, 0, 90 * time.Minute, false},
		{"1y6mo", 18, 0, 0, true},
		{"1w1d12h", 0, 8, 12 * time.Hour, true},
		// A leading minus negates the WHOLE interval, not one component.
		{"-1d", 0, -1, 0, true},
		{"-1h30m", 0, 0, -90 * time.Minute, false},
	} {
		t.Run(c.in, func(t *testing.T) {
			got := dtype.Every(c.in)
			if err := got.Err(); err != nil {
				t.Fatalf("Every(%q): %v", c.in, err)
			}
			if got.Months() != c.months || got.Days() != c.days ||
				got.Nanos() != int64(c.nanos) {
				t.Errorf("Every(%q) = {%d mo, %d d, %d ns}, want {%d, %d, %d}",
					c.in, got.Months(), got.Days(), got.Nanos(),
					c.months, c.days, int64(c.nanos))
			}
			if got.IsCalendar() != c.calendar {
				t.Errorf("IsCalendar = %v, want %v", got.IsCalendar(), c.calendar)
			}
		})
	}
}

// TestEveryDistinguishesMonthsFromMinutes is the one ambiguity in the notation, and
// getting it wrong aggregates over the wrong window with no error anywhere: "3m" is
// three minutes and "3mo" is three months, a factor of about 43,000.
func TestEveryDistinguishesMonthsFromMinutes(t *testing.T) {
	min3, mo3 := dtype.Every("3m"), dtype.Every("3mo")
	if min3.Nanos() != int64(3*time.Minute) || min3.Months() != 0 {
		t.Errorf(`Every("3m") = %v, want three minutes`, min3)
	}
	if mo3.Months() != 3 || mo3.Nanos() != 0 {
		t.Errorf(`Every("3mo") = %v, want three months`, mo3)
	}
}

func TestEveryRefuses(t *testing.T) {
	for _, in := range []string{
		"", "-", "banana", "1", "h", "1x", "1h30", "1hm", " 1h", "1.5h", "--1h", "0s",
	} {
		t.Run(in, func(t *testing.T) {
			got := dtype.Every(in)
			if got.Err() == nil {
				t.Fatalf("Every(%q) was accepted as %v", in, got)
			}
			if !errors.Is(got.Err(), uerr.ErrValue) {
				t.Errorf("want a value error, got %v", got.Err())
			}
		})
	}
}

func TestIntervalStringRoundTrips(t *testing.T) {
	for _, in := range []string{
		"1ns", "5us", "250ms", "30s", "15m", "6h", "2d", "1w", "3mo", "1y",
		"1h30m", "1y6mo", "-1d", "-1h30m",
	} {
		iv := dtype.Every(in)
		if iv.Err() != nil {
			t.Fatalf("Every(%q): %v", in, iv.Err())
		}
		back := dtype.Every(iv.String())
		if back.Err() != nil {
			t.Fatalf("Every(%q).String() = %q, which does not parse: %v",
				in, iv.String(), back.Err())
		}
		if back != iv {
			t.Errorf("Every(%q) -> %q -> {%d,%d,%d}, want {%d,%d,%d}",
				in, iv.String(), back.Months(), back.Days(), back.Nanos(),
				iv.Months(), iv.Days(), iv.Nanos())
		}
	}
}

// TestIntervalClampsMonthEnds is the bug the standard library will write for you if
// you let it.
//
// time.Date NORMALISES an out-of-range day: time.Date(2024, 2, 31, …) is March 2nd.
// So AddDate(0, 1, 0) on January 31st answers March 2nd and skips February entirely.
// Every calendar system in use clamps to the last day of the target month instead.
//
// The expectations are written out rather than derived, because deriving them from
// AddDate is deriving them from the thing being corrected.
func TestIntervalClampsMonthEnds(t *testing.T) {
	for _, c := range []struct {
		start  string
		every  string
		want   string
		stdlib string // what AddDate answers, when it differs
	}{
		{"2024-01-31", "1mo", "2024-02-29", "2024-03-02"}, // leap year
		{"2023-01-31", "1mo", "2023-02-28", "2023-03-03"}, // not a leap year
		{"2024-01-30", "1mo", "2024-02-29", "2024-03-01"},
		{"2024-03-31", "1mo", "2024-04-30", "2024-05-01"},
		{"2024-05-31", "1mo", "2024-06-30", "2024-07-01"},
		{"2024-01-31", "1y", "2025-01-31", ""},           // no clamp needed
		{"2024-02-29", "1y", "2025-02-28", "2025-03-01"}, // leap day, one year on
		{"2024-01-15", "1mo", "2024-02-15", ""},          // ordinary
		{"2024-03-31", "-1mo", "2024-02-29", "2024-03-02"},
	} {
		t.Run(c.start+"+"+c.every, func(t *testing.T) {
			start := mustDay(t, c.start)
			got := dtype.Every(c.every).AddTo(start)
			if got.Format("2006-01-02") != c.want {
				t.Errorf("%s + %s = %s, want %s",
					c.start, c.every, got.Format("2006-01-02"), c.want)
			}
			// And where the two differ, confirm the standard library really does
			// answer what this test claims it does — so the reason for the clamp is
			// verified rather than asserted.
			if c.stdlib != "" {
				iv := dtype.Every(c.every)
				std := start.AddDate(0, int(iv.Months()), 0)
				if std.Format("2006-01-02") != c.stdlib {
					t.Errorf("AddDate answers %s; this test claims %s",
						std.Format("2006-01-02"), c.stdlib)
				}
			}
		})
	}
}

// TestIntervalMatchesTimePackage: where the interval is NOT correcting the standard
// library, it must agree with it exactly.
//
// Nanos delegate to Add and days to AddDate; months agree with AddDate on every start
// day that needs no clamp, which is every day of the month up to 28. The corpus is
// the one step 6 used for the .dt namespace: leap days, 1900 (not a leap year), 2000
// (which is one), year boundaries, and instants before the epoch.
func TestIntervalMatchesTimePackage(t *testing.T) {
	starts := []time.Time{
		mustStamp(t, "2024-02-29T13:45:06.123456789Z"),
		mustStamp(t, "1900-02-28T23:59:59Z"),
		mustStamp(t, "2000-02-28T00:00:00Z"),
		mustStamp(t, "1969-12-31T23:59:59Z"),
		mustStamp(t, "1900-01-01T00:00:00Z"),
		mustStamp(t, "2024-12-31T00:00:00Z"),
	}
	for _, start := range starts {
		for _, c := range []struct {
			every   string
			d       time.Duration
			days    int
			mos     int
			dayLE28 bool
		}{
			{every: "1ns", d: time.Nanosecond},
			{every: "90m", d: 90 * time.Minute},
			{every: "6h", d: 6 * time.Hour},
			{every: "-6h", d: -6 * time.Hour},
			{every: "1d", days: 1},
			{every: "3w", days: 21},
			{every: "-2d", days: -2},
			// Months only where the start day is 28 or less, because that is exactly
			// the range in which no clamp is needed and the two must agree. The
			// clamping cases are the point of TestIntervalClampsMonthEnds and are
			// deliberately NOT compared against AddDate, which is the thing being
			// corrected.
			{every: "1y", mos: 12, dayLE28: true},
			{every: "-6mo", mos: -6, dayLE28: true},
		} {
			if c.dayLE28 && start.Day() > 28 {
				continue
			}
			t.Run(start.Format("2006-01-02")+"+"+c.every, func(t *testing.T) {
				got := dtype.Every(c.every).AddTo(start)
				var want time.Time
				switch {
				case c.d != 0:
					want = start.Add(c.d)
				case c.days != 0:
					want = start.AddDate(0, 0, c.days)
				default:
					want = start.AddDate(0, c.mos, 0)
				}
				if !got.Equal(want) {
					t.Errorf("%s + %s = %s, want %s (stdlib)",
						start.Format(time.RFC3339Nano), c.every,
						got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
				}
			})
		}
	}
}

// TestIntervalDayIsNotTwentyFourHours is the whole reason days are a separate field.
//
// On a spring-forward day a calendar day is 23 hours; on a fall-back day it is 25. An
// Interval that stored one day as 86,400,000,000,000 nanoseconds would answer 23:00
// and 01:00 on those two days, and would be right the other 363 times — which is
// exactly the kind of bug that ships.
func TestIntervalDayIsNotTwentyFourHours(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	for _, c := range []struct{ start, want string }{
		// 2024-03-10 is spring forward: this calendar day is 23 hours long.
		{"2024-03-09T12:00:00", "2024-03-10T12:00:00"},
		// 2024-11-03 is fall back: 25 hours.
		{"2024-11-02T12:00:00", "2024-11-03T12:00:00"},
	} {
		start, err := time.ParseInLocation("2006-01-02T15:04:05", c.start, ny)
		if err != nil {
			t.Fatal(err)
		}
		got := dtype.Every("1d").AddTo(start)
		if got.Format("2006-01-02T15:04:05") != c.want {
			t.Errorf("%s + 1d = %s, want %s — the wall clock must not move",
				c.start, got.Format("2006-01-02T15:04:05"), c.want)
		}
		// And the absolute elapsed time really is not 24 hours, which is what makes
		// the distinction observable rather than notional.
		if d := got.Sub(start); d == 24*time.Hour {
			t.Errorf("%s + 1d elapsed exactly 24h; this date should not", c.start)
		}
		// The duration form is the other answer, and it is also correct — for a
		// different question.
		if d := dtype.FromDuration(24 * time.Hour).AddTo(start).Sub(start); d != 24*time.Hour {
			t.Errorf("FromDuration(24h) moved %v, want exactly 24h", d)
		}
	}
}

// TestTruncateToIsLocal: a window grid is built from wall-clock components, so
// truncating by a day lands on LOCAL midnight.
func TestTruncateToIsLocal(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	// 2024-06-15 09:30 in New York is 13:30 UTC. Truncating by a day must give
	// 2024-06-15 00:00 New York — which is 04:00 UTC, NOT 00:00 UTC.
	ts, err := time.ParseInLocation("2006-01-02T15:04:05", "2024-06-15T09:30:00", ny)
	if err != nil {
		t.Fatal(err)
	}
	got := dtype.Every("1d").TruncateTo(ts)
	if w := got.Format("2006-01-02T15:04:05"); w != "2024-06-15T00:00:00" {
		t.Errorf("truncate 1d = %s local, want 2024-06-15T00:00:00", w)
	}
	if u := got.UTC().Format("15:04"); u != "04:00" {
		t.Errorf("truncate 1d = %s UTC; 00:00 UTC would mean the zone was ignored", u)
	}

	// Months land on the first of the month, quarters on the quarter start.
	for _, c := range []struct{ every, want string }{
		{"1mo", "2024-06-01T00:00:00"},
		{"1q", "2024-04-01T00:00:00"},
		{"1y", "2024-01-01T00:00:00"},
		{"1h", "2024-06-15T09:00:00"},
		{"15m", "2024-06-15T09:30:00"},
	} {
		got := dtype.Every(c.every).TruncateTo(ts)
		if w := got.Format("2006-01-02T15:04:05"); w != c.want {
			t.Errorf("truncate %s = %s, want %s", c.every, w, c.want)
		}
	}
}

// TestTruncateToFloorsBeforeTheEpoch: the grid must stay monotone on both sides of
// 1970, which is the same rule FromTime and truncateTemporal already follow.
func TestTruncateToFloorsBeforeTheEpoch(t *testing.T) {
	for _, c := range []struct{ in, every, want string }{
		{"1969-12-31T13:45:00Z", "1d", "1969-12-31T00:00:00Z"},
		{"1969-12-31T13:45:00Z", "1mo", "1969-12-01T00:00:00Z"},
		{"1969-12-31T13:45:00Z", "1y", "1969-01-01T00:00:00Z"},
		{"1969-12-31T13:45:00Z", "1h", "1969-12-31T13:00:00Z"},
		{"1900-05-17T04:05:06Z", "1q", "1900-04-01T00:00:00Z"},
	} {
		got := dtype.Every(c.every).TruncateTo(mustStamp(t, c.in))
		w := got.Format(time.RFC3339)
		if w != c.want {
			t.Errorf("truncate %s by %s = %s, want %s", c.in, c.every, w, c.want)
		}
		// Monotone: the floor never moves an instant forwards.
		if got.After(mustStamp(t, c.in)) {
			t.Errorf("truncate %s by %s moved FORWARD to %s", c.in, c.every, w)
		}
	}
}

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustStamp(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
