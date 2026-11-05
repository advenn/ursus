package ursus_test

// Parsing and formatting with strftime formats (step 113).
//
// `v0.4-scope.md` item 19: Str().ToDate takes no format, so "09/02/2024" could not
// be read as a date, and nothing formatted a date as anything but ISO 8601.
// Strptime and Strftime take Polars' and chrono's strftime directives. Every parse
// and format with a value is Polars 2.1.0's, recorded in the strptime group of
// testdata/oracle and spelled in oracle_strptime_test.go since step 499, which took
// this file's hand values. What Polars has not, a second unit and a Date formatted
// with a clock, is held to Go's time below; the refusals stay.

import (
	"errors"
	"math/rand/v2"
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
	t.Run("strict: a day that does not exist names the value", func(t *testing.T) {
		_, err := parse(t, []string{"2024-02-09", "2023-02-29"}, ursus.Date, "%Y-%m-%d", true)
		if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), `"2023-02-29"`) {
			t.Errorf("want a value error naming 2023-02-29, got %v", err)
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

// TestStrptimeSecondUnitMatchesGoTime: Polars has no Datetime of seconds, so an
// offset's text read into Datetime(s, UTC), and into a naive Datetime(s) as its UTC
// wall clock, is held to Go's time.Parse over 300 generated instants of 1900 to 2100.
func TestStrptimeSecondUnitMatchesGoTime(t *testing.T) {
	c := ursus.Col
	r := rand.New(rand.NewPCG(499, 113))
	const layout, format = "2006-01-02 15:04:05 -0700", "%Y-%m-%d %H:%M:%S %z"
	vals, want := make([]string, 300), make([]int64, 300)
	for i := range vals {
		at := time.Unix(r.Int64N(6311347200)-2208988800, 0) // 1900 to 2100
		off := time.FixedZone("", (r.IntN(57)-28)*30*60)    // -14:00 to +14:00 by the half hour
		vals[i] = at.In(off).Format(layout)
		back, err := time.Parse(layout, vals[i])
		if err != nil {
			t.Fatal(err)
		}
		want[i] = back.Unix()
	}
	for _, dt := range []dtype.DataType{dtype.Datetime(dtype.Second, "UTC"), dtype.Datetime(dtype.Second, "")} {
		df, err := ursus.Frame(ursus.Values("s", vals)).
			Select(c("s").Str().Strptime(dt, format, true).Alias("v")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := df.Schema().Field(0).Type; got != dt {
			t.Errorf("type %s, want %s", got, dt)
		}
		for i := range vals {
			got, ok, err := df.At[int64](i, "v")
			if err != nil {
				t.Fatal(err)
			}
			if !ok || got != want[i] {
				t.Errorf("%s of %q = %d (ok=%v), want %d", dt, vals[i], got, ok, want[i])
			}
		}
	}
}

func TestStrftime(t *testing.T) {
	c := ursus.Col
	at := time.Date(2024, 2, 9, 15, 4, 5, 123456000, time.UTC)
	frame := ursus.Frame(ursus.Values("ts", []time.Time{at})).WithColumns(
		c("ts").Cast(ursus.Date).Alias("day"),
		c("ts").Cast(dtype.Time(dtype.Micro)).Alias("clock"),
		// Values gives a time.Time a UTC zone; this is the naive one.
		c("ts").Cast(dtype.Datetime(dtype.Micro, "")).Alias("naive"),
	)
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

// TestStrftimeOfADateWithAClockIsMidnight: a Date formatted with a time of day is its
// midnight, which Polars 2.1 refuses ("cannot format Date with format '%F %T'"), held
// to Go's time.Format over 300 generated dates of 1800 to 2200.
func TestStrftimeOfADateWithAClockIsMidnight(t *testing.T) {
	c := ursus.Col
	r := rand.New(rand.NewPCG(499, 114))
	vals, want := make([]time.Time, 300), make([]string, 300)
	for i := range vals {
		vals[i] = time.Date(1800+r.IntN(401), time.Month(1+r.IntN(12)), 1+r.IntN(28), 0, 0, 0, 0, time.UTC)
		want[i] = vals[i].Format("2006-01-02 15:04:05 Mon 03PM")
	}
	df, err := ursus.Frame(ursus.Values("d", vals)).Select(c("d").Cast(ursus.Date).Dt().Strftime("%F %T %a %I%p").Alias("s")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range vals {
		got, _, err := df.At[string](i, "s")
		if err != nil {
			t.Fatal(err)
		}
		if got != want[i] {
			t.Errorf("%s = %q, want %q", vals[i].Format("2006-01-02"), got, want[i])
		}
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
