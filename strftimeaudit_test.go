package ursus_test

// What the audit of 0.4's strptime and strftime found (step 122).

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// TestStrptimeInAZoneWithAGapOrAnOverlap: time.Date answers a wall clock the clocks
// skip by moving it, and one they pass twice by picking either. A strict parse into
// a zoned Datetime returned 02:30 on the morning New York springs forward as an
// instant an hour away, with no error. Polars raises on both.
func TestStrptimeInAZoneWithAGapOrAnOverlap(t *testing.T) {
	c := ursus.Col
	ny := dtype.Datetime(dtype.Second, "America/New_York")
	parse := func(vals []string, strict bool) (*ursus.DataFrame, error) {
		return ursus.Frame(ursus.Values("s", vals)).
			Select(c("s").Str().Strptime(ny, "%Y-%m-%d %H:%M", strict).Alias("v")).Collect(t.Context())
	}
	for _, tc := range []struct{ value, says string }{
		{"2024-03-10 02:30", "does not exist in America/New_York"},
		{"2024-11-03 01:30", "ambiguous in America/New_York"},
	} {
		t.Run("strict: "+tc.value, func(t *testing.T) {
			_, err := parse([]string{"2024-03-10 01:59", tc.value}, true)
			if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("want a value error saying %q, got %v", tc.says, err)
			}
		})
	}
	t.Run("lenient: null, and the neighbours parse", func(t *testing.T) {
		df, err := parse([]string{"2024-03-10 01:59", "2024-03-10 02:30", "2024-03-10 03:00",
			"2024-11-03 00:59", "2024-11-03 01:30", "2024-11-03 02:00"}, false)
		if err != nil {
			t.Fatal(err)
		}
		_, got := textOf(t, df, "v")
		want := []string{"2024-03-10T01:59:00-05:00", "∅", "2024-03-10T03:00:00-04:00",
			"2024-11-03T00:59:00-04:00", "∅", "2024-11-03T02:00:00-05:00"}
		if !slices.Equal(got, want) {
			t.Errorf("%v\nwant %v", got, want)
		}
	})
}

// TestStrptimeFormatsThatNameNoDate: the plan-time check asked for any date
// directive. A weekday is not a date, and %d/%m is not one without a year: each
// passed, then failed on every row, or, %a alone, gave the year 0.
func TestStrptimeFormatsThatNameNoDate(t *testing.T) {
	c := ursus.Col
	for _, tc := range []struct {
		name, format string
		dt           dtype.DataType
	}{
		{"a weekday", "%a", ursus.Date},
		{"a day and a month", "%d/%m", ursus.Date},
		{"a year and a month", "%Y-%m", ursus.Date},
		{"a weekday and a time", "%A %H:%M", dtype.Datetime(dtype.Micro, "")},
		{"a zone name, which cannot be read back", "%Y-%m-%d %Z", dtype.Datetime(dtype.Micro, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ursus.Frame(ursus.Values("s", []string{"x"})).
				Select(c("s").Str().Strptime(tc.dt, tc.format, false)).CollectSchema(t.Context())
			if !errors.Is(err, ursus.ErrValue) {
				t.Errorf("want a value error while planning, got %v", err)
			}
		})
	}
}

func TestStrftimeDirectivesAsChronoReadsThem(t *testing.T) {
	c := ursus.Col
	t.Run("%y pivots at 70", func(t *testing.T) {
		df, err := ursus.Frame(ursus.Values("s", []string{"01/02/99", "01/02/70", "01/02/69", "01/02/00"})).
			Select(c("s").Str().Strptime(ursus.Date, "%d/%m/%y", true).Alias("v")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, got := textOf(t, df, "v"); !slices.Equal(got, []string{"1999-02-01", "1970-02-01", "2069-02-01", "2000-02-01"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("%z takes the colon or not", func(t *testing.T) {
		df, err := ursus.Frame(ursus.Values("s", []string{"2024-01-02 03:04 +05:30", "2024-01-02 03:04 +0530"})).
			Select(c("s").Str().Strptime(dtype.Datetime(dtype.Second, "UTC"), "%Y-%m-%d %H:%M %z", true).Alias("v")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, got := textOf(t, df, "v"); !slices.Equal(got, []string{"2024-01-01T21:34:00Z", "2024-01-01T21:34:00Z"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("a year past 9999 round-trips", func(t *testing.T) {
		df, err := ursus.Frame(ursus.Values("s", []string{"+10000-01-01", "2024-02-29", "+123456-07-08"})).
			Select(c("s").Str().Strptime(ursus.Date, "%Y-%m-%d", true).Dt().Strftime("%Y-%m-%d").Alias("v")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, got := textOf(t, df, "v"); !slices.Equal(got, []string{"+10000-01-01", "2024-02-29", "+123456-07-08"}) {
			t.Errorf("%v", got)
		}
	})
}
