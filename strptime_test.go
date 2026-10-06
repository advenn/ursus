package ursus_test

// Parsing and formatting with strftime formats (step 113).
//
// `v0.4-scope.md` item 19: Str().ToDate takes no format, so "09/02/2024" could not
// be read as a date, and nothing formatted a date as anything but ISO 8601.
// Strptime and Strftime take Polars' and chrono's strftime directives.

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

func TestStrptime(t *testing.T) {
	c := ursus.Col
	parse := func(t *testing.T, vals []string, dt dtype.DataType, format string, strict bool) (*ursus.DataFrame, error) {
		t.Helper()
		return ursus.Frame(ursus.Values("s", vals)).
			Select(c("s").Str().Strptime(dt, format, strict).Alias("v")).Collect(t.Context())
	}
	cases := []struct {
		name   string
		vals   []string
		dt     dtype.DataType
		format string
		want   []string // as ursus renders the parsed column
	}{
		{"day/month/year", []string{"09/02/2024", "29/02/2024", "1/3/2024"}, ursus.Date, "%d/%m/%Y",
			[]string{"2024-02-09", "2024-02-29", "2024-03-01"}},
		{"month names", []string{"Feb  9, 2024", "Dec 31, 1999"}, ursus.Date, "%b %e, %Y",
			[]string{"2024-02-09", "1999-12-31"}},
		{"day of year", []string{"2024-060", "2023-001"}, ursus.Date, "%Y-%j",
			[]string{"2024-02-29", "2023-01-01"}},
		{"a naive datetime with a fraction", []string{"2024-02-09 15:04:05.123456", "1969-12-31 23:59:59"},
			dtype.Datetime(dtype.Micro, ""), "%Y-%m-%d %H:%M:%S%.f",
			[]string{"2024-02-09T15:04:05.123456Z", "1969-12-31T23:59:59.000000Z"}},
		{"an offset read into UTC", []string{"2024-02-09 15:04 +0530", "2024-02-09 15:04 -0100"},
			dtype.Datetime(dtype.Second, "UTC"), "%Y-%m-%d %H:%M %z",
			[]string{"2024-02-09T09:34:00Z", "2024-02-09T16:04:00Z"}},
		{"an offset read into a naive datetime is its UTC wall clock", []string{"2024-02-09 15:04 +0530"},
			dtype.Datetime(dtype.Second, ""), "%Y-%m-%d %H:%M %z",
			[]string{"2024-02-09T09:34:00Z"}},
		{"a zone's wall clock", []string{"2024-01-15 09:00", "2024-07-15 09:00"},
			dtype.Datetime(dtype.Second, "America/New_York"), "%Y-%m-%d %H:%M",
			[]string{"2024-01-15T09:00:00-05:00", "2024-07-15T09:00:00-04:00"}},
		{"digits finer than the unit are dropped", []string{"2024-02-09 15:04:05.123999"},
			dtype.Datetime(dtype.Milli, ""), "%Y-%m-%d %H:%M:%S%.6f",
			[]string{"2024-02-09T15:04:05.123Z"}},
		{"a 12-hour clock", []string{"12:05 AM", "12:05 PM", "07:30 pm"}, dtype.Time(dtype.Micro), "%I:%M %p",
			[]string{"00:05:00.000000", "12:05:00.000000", "19:30:00.000000"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			df, err := parse(t, tc.vals, tc.dt, tc.format, true)
			if err != nil {
				t.Fatal(err)
			}
			typ, got := textOf(t, df, "v")
			if typ != tc.dt.String() {
				t.Errorf("type %s, want %s", typ, tc.dt)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("%v, want %v", got, tc.want)
			}
		})
	}

	t.Run("strict: a day that does not exist names the value", func(t *testing.T) {
		_, err := parse(t, []string{"2024-02-09", "2023-02-29"}, ursus.Date, "%Y-%m-%d", true)
		if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), `"2023-02-29"`) {
			t.Errorf("want a value error naming 2023-02-29, got %v", err)
		}
	})
	t.Run("not strict: what does not parse is null", func(t *testing.T) {
		df, err := parse(t, []string{"2024-02-09", "2023-02-29", "nope", "2024-02-09 "}, ursus.Date, "%Y-%m-%d", false)
		if err != nil {
			t.Fatal(err)
		}
		if _, got := textOf(t, df, "v"); !slices.Equal(got, []string{"2024-02-09", "∅", "∅", "∅"}) {
			t.Errorf("%v", got)
		}
	})

	refusals := []struct {
		name   string
		dt     dtype.DataType
		format string
	}{
		{"an unknown directive", ursus.Date, "%Y-%Q"},
		{"a Date format with no date", ursus.Date, "%H:%M"},
		{"a Date format with an offset", ursus.Date, "%Y-%m-%d %z"},
		{"a Time format with a date", dtype.Time(dtype.Micro), "%Y %H:%M"},
		{"an unknown zone", dtype.Datetime(dtype.Micro, "Mars/Olympus"), "%Y-%m-%d"},
	}
	for _, tc := range refusals {
		t.Run("refused while planning: "+tc.name, func(t *testing.T) {
			_, err := ursus.Frame(ursus.Values("s", []string{"x"})).
				Select(c("s").Str().Strptime(tc.dt, tc.format, true)).CollectSchema(t.Context())
			if !errors.Is(err, ursus.ErrValue) {
				t.Errorf("want a value error while planning, got %v", err)
			}
		})
	}
}

func TestStrftime(t *testing.T) {
	c := ursus.Col
	at := time.Date(2024, 2, 9, 15, 4, 5, 123456000, time.UTC)
	frame := ursus.Frame(ursus.Values("ts", []time.Time{at})).WithColumns(
		c("ts").Cast(ursus.Date).Alias("day"),
		c("ts").Cast(dtype.Datetime(dtype.Micro, "Asia/Tokyo")).Alias("tokyo"),
		c("ts").Cast(dtype.Time(dtype.Micro)).Alias("clock"),
		// Values gives a time.Time a UTC zone; this is the naive one.
		c("ts").Cast(dtype.Datetime(dtype.Micro, "")).Alias("naive"),
	)
	cases := []struct {
		name, col, format, want string
	}{
		{"a Date", "day", "%A, %e %B %Y", "Friday,  9 February 2024"},
		{"a naive Datetime", "naive", "%d/%m/%y %I:%M:%S%.3f %p", "09/02/24 03:04:05.123 PM"},
		{"a UTC Datetime has an offset", "ts", "%H:%M %z", "15:04 +0000"},
		{"a zoned Datetime in its own wall clock", "tokyo", "%F %T %z %Z", "2024-02-10 00:04:05 +0900 JST"},
		{"a Time", "clock", "%Hh%M", "15h04"},
		{"a Date's time is midnight", "day", "%F %T", "2024-02-09 00:00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			df, err := frame.Select(c(tc.col).Dt().Strftime(tc.format).Alias("s")).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, got := textOf(t, df, "s"); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("%q, want %q", got, tc.want)
			}
		})
	}
	refusals := []struct{ name, col, format string }{
		{"a date from a Time", "clock", "%Y-%m-%d"},
		{"an offset from a naive Datetime", "naive", "%F %z"},
		{"a zone name from a Date", "day", "%F %Z"},
	}
	for _, tc := range refusals {
		t.Run("refused while planning: "+tc.name, func(t *testing.T) {
			_, err := frame.Select(c(tc.col).Dt().Strftime(tc.format)).CollectSchema(t.Context())
			if !errors.Is(err, ursus.ErrValue) {
				t.Errorf("want a value error while planning, got %v", err)
			}
		})
	}
}

// TestStrftimeStrptimeRoundTrip: Datetime(us) values across five centuries, formatted
// and parsed back, are themselves.
func TestStrftimeStrptimeRoundTrip(t *testing.T) {
	c := ursus.Col
	r := rand.New(rand.NewPCG(113, 2))
	vals := make([]time.Time, 2000)
	for i := range vals {
		vals[i] = time.UnixMicro(r.Int64N(16e15) - 8e15).UTC()
	}
	const format = "%Y-%m-%dT%H:%M:%S%.6f"
	df, err := ursus.Frame(ursus.Values("ts", vals)).
		Select(c("ts").Cast(dtype.Datetime(dtype.Micro, "")).Alias("ts")).
		WithColumns(c("ts").Dt().Strftime(format).Str().Strptime(dtype.Datetime(dtype.Micro, ""), format, true).Alias("back")).
		Filter(c("ts").Ne(c("back"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 0 {
		_, a := textOf(t, df, "ts")
		_, b := textOf(t, df, "back")
		t.Errorf("%d values did not survive the round trip, e.g. %s came back %s", df.Height(), a[0], b[0])
	}
}
