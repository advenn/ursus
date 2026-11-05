package ursus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// TestCSVTryParseDates is step 355: Polars' read_csv(try_parse_dates=True), and a
// Date, a Datetime or a Time column read by Polars' patterns. Its answers are Polars
// 2.1.0's, recorded in the csvdates group of testdata/oracle and spelled in
// oracle_csvdates_test.go since step 498, which took this file's tables. What stays:
// each value read by its own pattern, where Polars reads a column by its first value's
// and refuses the other (feature-gaps.md §9), held to Go's time package; and a value
// past the inferred rows that does not parse, refused.
func TestCSVTryParseDates(t *testing.T) {
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte("x\n"+text), "x.csv", append([]ursus.CSVOption{ursus.WithTryParseDates(true)}, opts...)...)
	}
	for _, tc := range []struct {
		text    string
		dt      ursus.DataType
		layouts []string
	}{
		{"2020-01-02\n02/01/2020\n", ursus.Date, []string{time.DateOnly, "02/01/2006"}},
		{"10:30\n10:30:00.123\n", ursus.TimeOf(ursus.Nano), []string{"15:04", "15:04:05.000"}},
	} {
		schema, err := read(tc.text).CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := schema.Field(0).Type; got != tc.dt {
			t.Errorf("%q: read as %s, want %s", tc.text, got, tc.dt)
		}
		got := typedCells[int64](t, read(tc.text).Select(ursus.Col("x").Cast(ursus.Int64)))
		want := goParsedTicks(t, tc.dt, strings.Split(strings.TrimSuffix(tc.text, "\n"), "\n"), tc.layouts)
		if !sameCells(got, want) {
			t.Errorf("%q: %v, Go's time package reads %v", tc.text, got, want)
		}
	}

	// A value past the inferred rows that does not parse is refused. Polars: "could not
	// parse `abc` as dtype `date`".
	late := strings.Repeat("2020-01-03\n", 150) + "abc\n"
	if _, err := read(late).Collect(t.Context()); !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "abc") {
		t.Errorf("a late value: %v", err)
	}
}

// goParsedTicks is each text read by Go's time package with its own layout, as the
// ticks of dt: a Date's days, a Time's nanoseconds of the day.
func goParsedTicks(t *testing.T, dt ursus.DataType, texts, layouts []string) []any {
	t.Helper()
	out := make([]any, len(texts))
	for i, s := range texts {
		tm, err := time.Parse(layouts[i], s)
		if err != nil {
			t.Fatal(err)
		}
		switch dt.ID() {
		case dtype.TypeDate:
			out[i] = tm.Unix() / 86400
		case dtype.TypeTime:
			out[i] = int64(tm.Hour()*3600+tm.Minute()*60+tm.Second())*1e9 + int64(tm.Nanosecond())
		default:
			t.Fatalf("no ticks for %s", dt)
		}
	}
	return out
}

// TestFarInstantsAsGoValues is a Date's and a Datetime's value as a time.Time in a
// year a nanosecond count does not reach: before 1677 and after 2262, which ToMaps,
// Rows and At wrapped (step 355). The expected instants are Go's time package's.
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
