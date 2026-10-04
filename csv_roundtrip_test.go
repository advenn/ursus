package ursus_test

// Every type the CSV writer writes reads back as itself, generated over each type's
// edge values.
//
// Two routes. With the frame's own schema, every type must come back exactly: the
// writer and the reader are one format's two halves. By inference, then cast to the
// original type, the types whose text is unambiguous — the integers, Int128, the
// floats, Bool — must come back exactly too. A Decimal infers as a float, a String
// that looks like a number infers as one, and a temporal column's unit and zone are
// not in its text, so those three take the schema route only.

import (
	"math"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownCSVRoundTripDefects names each type and route that does not round-trip today.
var knownCSVRoundTripDefects = map[string]string{
	"Float32 by inference": "I21: NaN and Inf make the column String",
	"Float64 by inference": "I21: NaN and Inf make the column String",
	"String by schema":     "I27: a null is written empty and read back as \"\"",
}

// nullAtEnd is a validity of n set rows and one unset.
func nullAtEnd(n int) []bool {
	v := make([]bool, n+1)
	for i := range n {
		v[i] = true
	}
	return v
}

func withNullRow[T ursus.Literal](name string, vals ...T) *ursus.LazyFrame {
	var zero T
	return ursus.Frame(ursus.ValuesNullable(name, append(vals, zero), nullAtEnd(len(vals))))
}

// ticksFrame is one column of temporal type dt holding ticks, and a null.
func ticksFrame[T int32 | int64](dt dtype.DataType, ticks ...T) *ursus.LazyFrame {
	valid := bitmap.NewBuilder(len(ticks) + 1)
	for range ticks {
		valid.Append(true)
	}
	valid.Append(false)
	return ursus.Frame(data.NewFixed("v", dt, append(ticks, 0), valid.Finish()))
}

func TestCSVRoundTripsEveryType(t *testing.T) {
	c := ursus.Col
	instant := func(dt dtype.DataType, utc ...string) *ursus.LazyFrame {
		ticks := make([]int64, len(utc))
		for i, s := range utc {
			tm, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				t.Fatal(err)
			}
			var ok bool
			if ticks[i], ok = dt.FromTime(tm); !ok {
				t.Fatalf("cannot store %s as %s", s, dt)
			}
		}
		return ticksFrame(dt, ticks...)
	}
	for _, z := range []string{"Asia/Kolkata", "Europe/Amsterdam"} {
		if _, err := time.LoadLocation(z); err != nil {
			t.Skipf("no tzdata for %s: %v", z, err)
		}
	}
	far := []string{"1970-01-01T00:00:00Z", "1969-12-31T23:59:59.999999Z", "1677-09-22T00:00:00Z",
		"2262-04-11T00:00:00Z", "1929-12-31T23:40:28Z", "1970-01-01T00:44:30Z"}

	cases := []struct {
		name  string
		lf    *ursus.LazyFrame
		infer bool // whether inference, cast back, is exact for this type
	}{
		{"Int8", withNullRow("v", int8(math.MinInt8), -1, 0, 1, math.MaxInt8), true},
		{"Int16", withNullRow("v", int16(math.MinInt16), -1, 0, math.MaxInt16), true},
		{"Int32", withNullRow("v", int32(math.MinInt32), -1, 0, math.MaxInt32), true},
		{"Int64", withNullRow("v", int64(math.MinInt64), -1, 0, math.MaxInt64), true},
		{"Uint8", withNullRow("v", uint8(0), 1, math.MaxUint8), true},
		{"Uint16", withNullRow("v", uint16(0), 1, math.MaxUint16), true},
		{"Uint32", withNullRow("v", uint32(0), 1, math.MaxUint32), true},
		{"Uint64", withNullRow("v", uint64(0), 1, math.MaxInt64, math.MaxInt64+1, math.MaxUint64), true},
		{"Int128", ursus.Frame(i128Col(t, dtype.Int128, "v", "-170141183460469231731687303715884105728", "-1", "0",
			"18446744073709551616", "170141183460469231731687303715884105727")), true},
		{"Float32", withNullRow("v", float32(0.1), 1, -2.5, math.MaxFloat32, math.SmallestNonzeroFloat32,
			float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.NaN()), 16777216), true},
		{"Float64", withNullRow("v", 0.1, 1, -2.5, 1e300, -1e-300, math.MaxFloat64, math.SmallestNonzeroFloat64,
			math.Inf(1), math.Inf(-1), math.NaN(), 1<<53, 1e21, 123456789), true},
		{"Bool", withNullRow("v", true, false), true},
		{"Decimal(38, 10)", ursus.Frame(i128Col(t, dtype.Decimal(38, 10), "v",
			"99999999999999999999999999999999999999", "-99999999999999999999999999999999999999", "0", "1", "-1")), false},
		{"Decimal(10, 2)", ursus.Frame(i128Col(t, dtype.Decimal(10, 2), "v", "1234", "-1", "0", "9999999999")), false},
		{"String", withNullRow("v", "a,b", `say "hi"`, "line\nbreak", "  padded  ", "é", "1", "1.0", "NaN", "true"), false},
		{"Date", ticksFrame(dtype.Date, int32(0), -1, -719162, 2932896), false},
		{"Time(ns)", ticksFrame(dtype.Time(dtype.Nano), int64(0), 1, 86399_999_999_999), false},
		{"Duration(us)", ticksFrame(dtype.Duration(dtype.Micro), int64(-1), 0, 3600e6, math.MaxInt64/1000), false},
		{"Datetime(ns, UTC)", instant(dtype.Datetime(dtype.Nano, "UTC"), far[0], far[1], far[2], far[3]), false},
		{"Datetime(us)", instant(dtype.Datetime(dtype.Micro, ""), far...), false},
		{"Datetime(ms, Asia/Kolkata)", instant(dtype.Datetime(dtype.Milli, "Asia/Kolkata"), far...), false},
		{"Datetime(s, Europe/Amsterdam)", instant(dtype.Datetime(dtype.Second, "Europe/Amsterdam"),
			"1970-01-01T00:00:00Z", "1929-12-31T23:40:28Z", "1937-06-30T22:00:00Z", "2024-03-31T01:30:00Z"), false},
	}

	var routes int
	for _, tc := range cases {
		sc, err := tc.lf.CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		dt := sc.Field(0).Type
		text, err := csvText(t, tc.lf)
		check := func(route, wrong string) {
			routes++
			key := tc.name + " " + route
			why, known := knownCSVRoundTripDefects[key]
			switch {
			case known && wrong == "":
				t.Errorf("%s round-trips now; delete it from knownCSVRoundTripDefects (%s)", key, why)
			case !known && wrong != "":
				t.Errorf("%s: %s", key, wrong)
			}
		}
		if err != nil {
			check("by schema", "write: "+err.Error())
			continue
		}
		check("by schema", ioEqual(t, ursus.ScanCSVReader([]byte(text), "x.csv", ursus.WithSchema(sc)), tc.lf))
		if tc.infer {
			check("by inference", ioEqual(t,
				ursus.ScanCSVReader([]byte(text), "x.csv").Select(c("v").Cast(dt)), tc.lf))
		}
	}
	for key := range knownCSVRoundTripDefects {
		found := false
		for _, tc := range cases {
			if key == tc.name+" by schema" || (tc.infer && key == tc.name+" by inference") {
				found = true
			}
		}
		if !found {
			t.Errorf("knownCSVRoundTripDefects names %q, which is not a route", key)
		}
	}
	if routes < 30 {
		t.Fatalf("only %d routes ran — the sweep has gone vacuous", routes)
	}
}
