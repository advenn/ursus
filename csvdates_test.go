package ursus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestCSVTryParseDates is step 355: Polars' read_csv(try_parse_dates=True), and a
// Date, a Datetime or a Time column read by Polars' patterns. Every answer is Polars
// 1.44.1's but where noted.
func TestCSVTryParseDates(t *testing.T) {
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte("x\n"+text), "x.csv", append([]ursus.CSVOption{ursus.WithTryParseDates(true)}, opts...)...)
	}
	const (
		d2  = "2020-01-02 00:00:00 +0000 UTC"
		d31 = "2021-12-31 00:00:00 +0000 UTC"
	)
	for _, tc := range []struct {
		text, want string
	}{
		{"2020-01-02\n2021-12-31\n", d2 + " " + d31 + " Date"},
		{"2020/01/02\n2021/12/31\n", d2 + " " + d31 + " Date"},
		{"2020.01.02\n", d2 + " Date"},
		{"02/01/2020\n31/12/2021\n", d2 + " " + d31 + " Date"},
		{"02-01-2020\n", d2 + " Date"},
		{"02.01.2020\n", d2 + " Date"},
		{"2020-1-2\n", d2 + " Date"},
		{"1/2/2020\n", "2020-02-01 00:00:00 +0000 UTC Date"},
		{"0999-01-02\n", "0999-01-02 00:00:00 +0000 UTC Date"},
		{"2020-01-02\n\n2021-12-31\n", d2 + " <nil> " + d31 + " Date"},
		// A month first is not a pattern: 12/31 is no day of a month 31.
		{"01/02/2020\n12/31/2021\n", "01/02/2020 12/31/2021 String"},
		{"2020-01-02\nnot a date\n", "2020-01-02 not a date String"},
		{"2020-02-30\n", "2020-02-30 String"},
		{"2020-13-01\n", "2020-13-01 String"},
		{"2020-01-02 \n", "2020-01-02  String"},
		{"Jan 2 2020\n", "Jan 2 2020 String"},
		{"20200102\n", "20200102 Int64"},
		{"2020-01-02 03:04:05\n2021-12-31 23:59:59\n", "2020-01-02 03:04:05 +0000 UTC 2021-12-31 23:59:59 +0000 UTC Datetime(us)"},
		{"2020-01-02T03:04:05\n2021-12-31T23:59:59.123\n", "2020-01-02 03:04:05 +0000 UTC 2021-12-31 23:59:59.123 +0000 UTC Datetime(us)"},
		{"2020-01-02 03:04:05\n2020-01-02T03:04:05\n", "2020-01-02 03:04:05 +0000 UTC 2020-01-02 03:04:05 +0000 UTC Datetime(us)"},
		// A fraction past microseconds is cut.
		{"2020-01-02T03:04:05.123456789\n", "2020-01-02 03:04:05.123456 +0000 UTC Datetime(us)"},
		{"2020-01-02 03:04:05.5\n", "2020-01-02 03:04:05.5 +0000 UTC Datetime(us)"},
		{"2020-01-02 03:04\n", "2020-01-02 03:04:00 +0000 UTC Datetime(us)"},
		{"2020-01-02 3:04:05\n", "2020-01-02 03:04:05 +0000 UTC Datetime(us)"},
		{"02/01/2020 03:04:05\n", "2020-01-02 03:04:05 +0000 UTC Datetime(us)"},
		{"2020/01/02 03:04:05\n", "2020-01-02 03:04:05 +0000 UTC Datetime(us)"},
		{"2020-01-02 03:04:05 PM\n", "2020-01-02 03:04:05 PM String"},
		// An offset is the instant, in UTC.
		{"2020-01-02T03:04:05Z\n", "2020-01-02 03:04:05 +0000 UTC Datetime(us, UTC)"},
		{"2020-01-02T03:04:05.123Z\n", "2020-01-02 03:04:05.123 +0000 UTC Datetime(us, UTC)"},
		{"2020-01-02T03:04:05+0900\n", "2020-01-01 18:04:05 +0000 UTC Datetime(us, UTC)"},
		{"2020-01-02T03:04:05 +0900\n", "2020-01-01 18:04:05 +0000 UTC Datetime(us, UTC)"},
		{"2020-01-02T03:04:05+09:00\n2020-01-02T03:04:05+01:00\n", "2020-01-01 18:04:05 +0000 UTC 2020-01-02 02:04:05 +0000 UTC Datetime(us, UTC)"},
		{"2020-01-02T03:04:05.123+05:30\n", "2020-01-01 21:34:05.123 +0000 UTC Datetime(us, UTC)"},
		{"2020-01-02T03:04:05.000000+0000\n", "2020-01-02 03:04:05 +0000 UTC Datetime(us, UTC)"},
		// An instant in a zone beside one in none, or a date beside an instant, is text.
		{"2020-01-02T03:04:05\n2020-01-02T03:04:05+0900\n", "2020-01-02T03:04:05 2020-01-02T03:04:05+0900 String"},
		{"2020-01-02 03:04:05.123\n2020-01-02\n", "2020-01-02 03:04:05.123 2020-01-02 String"},
		{"2020-01-02\n2020-01-02 03:04:05\n", "2020-01-02 2020-01-02 03:04:05 String"},
		{"10:30:00\n11:00:00\n", "10h30m0s 11h0m0s Time(ns)"},
		{"10:30:00.5\n", "10h30m0.5s Time(ns)"},
		{"10:30\n", "10h30m0s Time(ns)"},
		{"25:00:00\n", "25:00:00 String"},
		{"10:30 PM\n", "10:30 PM String"},
		{"10:30:00x\n", "10:30:00x String"},
		{"2020-01-02T03:04:05+0900x\n", "2020-01-02T03:04:05+0900x String"},
		{"10:30:00\n2020-01-02\n", "10:30:00 2020-01-02 String"},
		// Polars refuses these, reading a column by its first value's pattern; each
		// value is read by its own here.
		{"2020-01-02\n02/01/2020\n", d2 + " " + d2 + " Date"},
		{"10:30\n10:30:00.123\n", "10h30m0s 10h30m0.123s Time(ns)"},
	} {
		if got := cells(t, read(tc.text), "x"); got != tc.want {
			t.Errorf("%q: %s, want %s", tc.text, got, tc.want)
		}
	}

	// Polars' default: no date inferred.
	if got := cells(t, ursus.ScanCSVReader([]byte("x\n2020-01-02\n"), "x.csv"), "x"); got != "2020-01-02 String" {
		t.Errorf("without it: %s", got)
	}
	// A null value, a schema override and another column.
	if got := cells(t, read("NA\n2020-01-02\n", ursus.WithNullValues("NA")), "x"); got != "<nil> "+d2+" Date" {
		t.Errorf("a null value: %s", got)
	}
	two := ursus.ScanCSVReader([]byte("x,y\n2020-01-02,2020-01-02 03:04:05\n"), "x.csv",
		ursus.WithTryParseDates(true), ursus.WithSchemaOverrides(map[string]ursus.DataType{"x": ursus.String}))
	if got := cells(t, two, "x") + " | " + cells(t, two, "y"); got != "2020-01-02 String | 2020-01-02 03:04:05 +0000 UTC Datetime(us)" {
		t.Errorf("an override: %s", got)
	}

	// A column a schema types is read by the same patterns, as Polars reads one.
	given := func(text string, to ursus.DataType) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte("x\n"+text), "x.csv", ursus.WithSchemaOverrides(map[string]ursus.DataType{"x": to}))
	}
	for _, tc := range []struct {
		text string
		to   ursus.DataType
		want string
	}{
		{"02/01/2020\n", ursus.Date, d2 + " Date"},
		{"2020.01.02\n", ursus.Date, d2 + " Date"},
		{"2020-1-2\n", ursus.Date, d2 + " Date"},
		{"02/01/2020 03:04:05\n", ursus.Datetime(ursus.Micro, ""), "2020-01-02 03:04:05 +0000 UTC Datetime(us)"},
		{"2020-01-02T03:04:05+0900\n", ursus.Datetime(ursus.Micro, "UTC"), "2020-01-01 18:04:05 +0000 UTC Datetime(us, UTC)"},
		{"2020-01-02\n", ursus.Datetime(ursus.Milli, ""), d2 + " Datetime(ms)"},
		{"10:30\n", ursus.TimeOf(ursus.Nano), "10h30m0s Time(ns)"},
		// What ParseTemporal read before still reads.
		{"2020-01-02T03:04:05.123456789Z\n", ursus.Datetime(ursus.Nano, "UTC"), "2020-01-02 03:04:05.123456789 +0000 UTC Datetime(ns, UTC)"},
	} {
		if got := cells(t, given(tc.text, tc.to), "x"); got != tc.want {
			t.Errorf("%q as %s: %s, want %s", tc.text, tc.to, got, tc.want)
		}
	}

	// A value past the inferred rows that does not parse is refused. Polars: "could not
	// parse `abc` as dtype `date`".
	late := strings.Repeat("2020-01-03\n", 150) + "abc\n"
	if _, err := read(late).Collect(t.Context()); !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "abc") {
		t.Errorf("a late value: %v", err)
	}
	if got := cells(t, read(late, ursus.WithInferRows(0)).Tail(1), "x"); got != "abc String" {
		t.Errorf("every row inferred: %s", got)
	}
}

// TestFarInstantsAsGoValues is a Date's and a Datetime's value as a time.Time in a
// year a nanosecond count does not reach: before 1677 and after 2262, which ToMaps,
// Rows and At wrapped (step 355).
func TestFarInstantsAsGoValues(t *testing.T) {
	text := "d,t,s\n0999-01-02,3000-01-02 03:04:05.123,1500-06-01 00:00:00\n"
	df, err := ursus.ScanCSVReader([]byte(text), "far.csv", ursus.WithSchemaOverrides(map[string]ursus.DataType{
		"d": ursus.Date, "t": ursus.Datetime(ursus.Milli, ""), "s": ursus.Datetime(ursus.Second, "Asia/Tokyo"),
	})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		D, T, S time.Time
	}
	rows, err := df.Rows[row]()
	if err != nil {
		t.Fatal(err)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	want := row{
		D: time.Date(999, 1, 2, 0, 0, 0, 0, time.UTC),
		T: time.Date(3000, 1, 2, 3, 4, 5, 123_000_000, time.UTC),
		S: time.Date(1500, 6, 1, 0, 0, 0, 0, time.UTC).In(tokyo),
	}
	if len(rows) != 1 || !rows[0].D.Equal(want.D) || !rows[0].T.Equal(want.T) || !rows[0].S.Equal(want.S) || rows[0].S.Location().String() != "Asia/Tokyo" {
		t.Errorf("rows %v, want %v", rows, want)
	}
	maps, err := df.ToMaps()
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := maps[0]["d"].(time.Time); !d.Equal(want.D) {
		t.Errorf("ToMaps' date %v", maps[0]["d"])
	}
}
