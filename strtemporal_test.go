package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestStrToTemporalByPatterns is step 356: Str().ToDate, ToDatetime and ToTime with no
// format, and Strptime with an empty one, read Polars' patterns, as Polars'
// str.to_date, str.to_datetime and str.to_time with format=None do. Every answer is
// Polars 1.44.1's but where noted.
func TestStrToTemporalByPatterns(t *testing.T) {
	c := ursus.Col
	x := func(vals ...any) *ursus.LazyFrame { return ursus.Frame(strs("x", vals...)) }
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"a date day first", x("02/01/2020", "31/12/2021").Select(c("x").Str().ToDate()),
			"2020-01-02 00:00:00 +0000 UTC 2021-12-31 00:00:00 +0000 UTC Date"},
		{"a date that does not parse", x("02/01/2020", "x").Select(c("x").Str().ToDate()),
			"2020-01-02 00:00:00 +0000 UTC <nil> Date"},
		{"a null first", x(nil, "2020.01.02").Select(c("x").Str().ToDate()),
			"<nil> 2020-01-02 00:00:00 +0000 UTC Date"},
		{"never a month first", x("01/02/2020").Select(c("x").Str().ToDate()),
			"2020-02-01 00:00:00 +0000 UTC Date"},
		{"Strptime of no format", x("02/01/2020").Select(c("x").Str().Strptime(ursus.Date, "", true)),
			"2020-01-02 00:00:00 +0000 UTC Date"},
		{"two instants", x("2020-01-02 03:04:05", "2021-01-02T03:04").Select(c("x").Str().ToDatetime(ursus.Micro, "")),
			"2020-01-02 03:04:05 +0000 UTC 2021-01-02 03:04:00 +0000 UTC Datetime(us)"},
		{"in milliseconds", x("2020-01-02 03:04:05.123456").Select(c("x").Str().ToDatetime(ursus.Milli, "")),
			"2020-01-02 03:04:05.123 +0000 UTC Datetime(ms)"},
		{"in nanoseconds", x("2020-01-02T03:04:05.123456789").Select(c("x").Str().ToDatetime(ursus.Nano, "")),
			"2020-01-02 03:04:05.123456789 +0000 UTC Datetime(ns)"},
		{"a date's midnight", x("2020-01-02").Select(c("x").Str().ToDatetime(ursus.Micro, "")),
			"2020-01-02 00:00:00 +0000 UTC Datetime(us)"},
		// The break: a text of no offset is read on the zone's wall clock, where the
		// cast read it as UTC's, 12:04:05 in Tokyo.
		{"a zone's wall clock", x("2020-01-02 03:04:05").Select(c("x").Str().ToDatetime(ursus.Micro, "Asia/Tokyo")),
			"2020-01-02 03:04:05 +0900 JST Datetime(us, Asia/Tokyo)"},
		{"an offset in a zone", x("2020-01-02T03:04:05+0900").Select(c("x").Str().ToDatetime(ursus.Micro, "Asia/Tokyo")),
			"2020-01-02 03:04:05 +0900 JST Datetime(us, Asia/Tokyo)"},
		{"an offset in UTC", x("2020-01-02T03:04:05+0900").Select(c("x").Str().ToDatetime(ursus.Micro, "UTC")),
			"2020-01-01 18:04:05 +0000 UTC Datetime(us, UTC)"},
		{"a time of one-digit hour", x("3:04:05").Select(c("x").Str().ToTime("", false)),
			"3h4m5s Time(ns)"},
		// Polars refuses these, reading a column by its first value's pattern; each value
		// is read by its own here.
		{"two times' patterns", x("10:30", "11:00:01").Select(c("x").Str().ToTime("", true)),
			"10h30m0s 11h0m1s Time(ns)"},
		{"two dates' patterns", x("2020-01-02", "02/01/2020").Select(c("x").Str().ToDate()),
			"2020-01-02 00:00:00 +0000 UTC 2020-01-02 00:00:00 +0000 UTC Date"},
		// Polars refuses a wall clock New York skips or reads twice unless told; a
		// lenient parse is null here, as Strptime's has been.
		{"a skipped wall clock", x("2024-03-10 02:30:00").Select(c("x").Str().ToDatetime(ursus.Micro, "America/New_York")),
			"<nil> Datetime(us, America/New_York)"},
		{"a wall clock read twice", x("2024-11-03 01:30:00").Select(c("x").Str().ToDatetime(ursus.Micro, "America/New_York")),
			"<nil> Datetime(us, America/New_York)"},
	} {
		if got := cells(t, tc.q, "x"); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	// Strict, as Polars' default, a value no pattern reads is refused, an instant's text
	// as a Date too: "could not find an appropriate format to parse dates".
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
	}{
		{"an instant as a Date", x("2020-01-02 03:04:05").Select(c("x").Str().Strptime(ursus.Date, "", true))},
		{"no pattern", x("abc").Select(c("x").Str().Strptime(ursus.Datetime(ursus.Micro, ""), "", true))},
		{"a date as a Time", x("2020-01-02").Select(c("x").Str().ToTime("", true))},
		{"a skipped wall clock", x("2024-03-10 02:30:00").Select(c("x").Str().Strptime(ursus.Datetime(ursus.Micro, "America/New_York"), "", true))},
	} {
		if _, err := tc.q.Collect(t.Context()); !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "Polars' patterns") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
