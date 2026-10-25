package ursus_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
)

// csvProbeFrame is the frame Polars 1.44.1 wrote each answer below from.
func csvProbeFrame() *ursus.LazyFrame {
	c := ursus.Col
	at := func(y int, mo time.Month, d, h, mi, s, ns int) time.Time {
		return time.Date(y, mo, d, h, mi, s, ns, time.UTC)
	}
	valid := []bool{true, false, true}
	return ursus.Frame(
		i64s("i", 1, nil, -3),
		ursus.Values("f", []float64{1.5, 0.1, 1e20}),
		strs("s", "a", "b,c", `q"x`),
		ursus.ValuesNullable("t", []time.Time{at(2020, 1, 2, 3, 4, 5, 600_000), {}, at(1999, 12, 31, 23, 59, 59, 0)}, valid),
		ursus.ValuesNullable("tm", []time.Time{at(1970, 1, 1, 1, 2, 3, 0), {}, at(1970, 1, 1, 23, 59, 59, 500_000_000)}, valid),
		ursus.ValuesNullable("b", []bool{true, false, false}, []bool{true, true, false}),
	).Select(
		c("i"), c("f"), c("s"),
		c("t").Cast(ursus.Date).Alias("d"),
		c("t").Cast(ursus.Datetime(ursus.Micro, "")),
		c("tm").Cast(ursus.TimeOf(ursus.Nano)),
		c("b"),
	)
}

func csvOut(t *testing.T, lf *ursus.LazyFrame, opts ...ursus.CSVSinkOption) string {
	t.Helper()
	var b bytes.Buffer
	if err := lf.WriteCSV(t.Context(), &b, opts...); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestCSVWriteOptions is step 354: Polars' write_csv options, each answer Polars
// 1.44.1's but where noted.
func TestCSVWriteOptions(t *testing.T) {
	lf := csvProbeFrame()
	const plain = "i,f,s,d,t,tm,b\n" +
		"1,1.5,a,2020-01-02,2020-01-02T03:04:05.000600,01:02:03.000000000,true\n" +
		",0.1,\"b,c\",,,,false\n" +
		"-3,1e+20,\"q\"\"x\",1999-12-31,1999-12-31T23:59:59.000000,23:59:59.500000000,\n"
	for _, tc := range []struct {
		name string
		opts []ursus.CSVSinkOption
		want string
	}{
		{"the default", nil, plain},
		{"QuoteNecessary", []ursus.CSVSinkOption{ursus.WithQuoteStyle(ursus.QuoteNecessary)}, plain},
		{"QuoteAlways", []ursus.CSVSinkOption{ursus.WithQuoteStyle(ursus.QuoteAlways)},
			`"i","f","s","d","t","tm","b"` + "\n" +
				`"1","1.5","a","2020-01-02","2020-01-02T03:04:05.000600","01:02:03.000000000","true"` + "\n" +
				`"","0.1","b,c","","","","false"` + "\n" +
				`"-3","1e+20","q""x","1999-12-31","1999-12-31T23:59:59.000000","23:59:59.500000000",""` + "\n"},
		{"QuoteNever", []ursus.CSVSinkOption{ursus.WithQuoteStyle(ursus.QuoteNever)},
			"i,f,s,d,t,tm,b\n" +
				"1,1.5,a,2020-01-02,2020-01-02T03:04:05.000600,01:02:03.000000000,true\n" +
				",0.1,b,c,,,,false\n" +
				"-3,1e+20,q\"x,1999-12-31,1999-12-31T23:59:59.000000,23:59:59.500000000,\n"},
		{"QuoteNonNumeric", []ursus.CSVSinkOption{ursus.WithQuoteStyle(ursus.QuoteNonNumeric)},
			`"i","f","s","d","t","tm","b"` + "\n" +
				`1,1.5,"a","2020-01-02","2020-01-02T03:04:05.000600","01:02:03.000000000","true"` + "\n" +
				`,0.1,"b,c",,,,"false"` + "\n" +
				`-3,1e+20,"q""x","1999-12-31","1999-12-31T23:59:59.000000","23:59:59.500000000",` + "\n"},
		{"a quote of its own", []ursus.CSVSinkOption{ursus.WithWriteQuote('\'')},
			"i,f,s,d,t,tm,b\n" +
				"1,1.5,a,2020-01-02,2020-01-02T03:04:05.000600,01:02:03.000000000,true\n" +
				",0.1,'b,c',,,,false\n" +
				"-3,1e+20,q\"x,1999-12-31,1999-12-31T23:59:59.000000,23:59:59.500000000,\n"},
		{"formats", []ursus.CSVSinkOption{ursus.WithDateFormat("%d/%m/%Y"), ursus.WithDatetimeFormat("%Y%m%d %H:%M"), ursus.WithTimeFormat("%H.%M")},
			"i,f,s,d,t,tm,b\n" +
				"1,1.5,a,02/01/2020,20200102 03:04,01.02,true\n" +
				",0.1,\"b,c\",,,,false\n" +
				"-3,1e+20,\"q\"\"x\",31/12/1999,19991231 23:59,23.59,\n"},
		{"a BOM", []ursus.CSVSinkOption{ursus.WithIncludeBOM(true)}, "\xef\xbb\xbf" + plain},
		{"a null's text, quoted", []ursus.CSVSinkOption{ursus.WithNullValue("NA"), ursus.WithQuoteStyle(ursus.QuoteAlways)},
			`"i","f","s","d","t","tm","b"` + "\n" +
				`"1","1.5","a","2020-01-02","2020-01-02T03:04:05.000600","01:02:03.000000000","true"` + "\n" +
				`"NA","0.1","b,c","NA","NA","NA","false"` + "\n" +
				`"-3","1e+20","q""x","1999-12-31","1999-12-31T23:59:59.000000","23:59:59.500000000","NA"` + "\n"},
	} {
		if got := csvOut(t, lf, tc.opts...); got != tc.want {
			t.Errorf("%s:\n%q, want\n%q", tc.name, got, tc.want)
		}
	}

	// Floats: the f column alone.
	f := lf.Select(ursus.Col("f"))
	for _, tc := range []struct {
		name string
		opts []ursus.CSVSinkOption
		want string
	}{
		{"a precision of 2", []ursus.CSVSinkOption{ursus.WithFloatPrecision(2)}, "f\n1.50\n0.10\n100000000000000000000.00\n"},
		{"a precision of 0", []ursus.CSVSinkOption{ursus.WithFloatPrecision(0)}, "f\n2\n0\n100000000000000000000\n"},
		{"scientific", []ursus.CSVSinkOption{ursus.WithFloatScientific(true)}, "f\n1.5e0\n1e-1\n1e20\n"},
		{"never scientific", []ursus.CSVSinkOption{ursus.WithFloatScientific(false)}, "f\n1.5\n0.1\n100000000000000000000\n"},
		{"scientific of a precision", []ursus.CSVSinkOption{ursus.WithFloatScientific(true), ursus.WithFloatPrecision(2)}, "f\n1.50e0\n1.00e-1\n1.00e20\n"},
		// Polars quotes every float under a decimal comma and a comma's separator.
		{"a decimal comma", []ursus.CSVSinkOption{ursus.WithDecimalComma(true)}, "f\n\"1,5\"\n\"0,1\"\n\"1e+20\"\n"},
		{"a decimal comma, a semicolon's separator", []ursus.CSVSinkOption{ursus.WithDecimalComma(true), ursus.WithWriteSeparator(';')}, "f\n1,5\n0,1\n1e+20\n"},
	} {
		if got := csvOut(t, f, tc.opts...); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	// A float's own text is Polars' text, its cast's (step 325), where it was Go's:
	// 123456789 was 1.23456789e+08 and 1e-7 1e-07.
	odd := ursus.Frame(ursus.Values("f", []float64{2, 1e-7, 1e16, 123456789, 0.30000000000000004, math.Copysign(0, -1), math.NaN(), math.Inf(-1), 1e300, 5e-324}))
	if got := csvOut(t, odd); got != "f\n2.0\n1e-7\n1e+16\n123456789.0\n0.30000000000000004\n-0.0\nNaN\n-inf\n1e+300\n5e-324\n" {
		t.Errorf("a float's own text: %q", got)
	}
	if got := csvOut(t, odd.Head(8), ursus.WithFloatScientific(false)); got != "f\n2\n0.0000001\n10000000000000000\n123456789\n0.30000000000000004\n-0\nNaN\n-inf\n" {
		t.Errorf("never scientific: %q", got)
	}
	nan := ursus.Frame(ursus.Values("x", []float64{math.NaN(), math.Inf(1), math.Copysign(0, -1), 1234567}))
	if got := csvOut(t, nan, ursus.WithFloatPrecision(2)); got != "x\nNaN\ninf\n-0.00\n1234567.00\n" {
		t.Errorf("NaN with a precision: %q", got)
	}
	if got := csvOut(t, nan, ursus.WithFloatScientific(true)); got != "x\nNaN\ninf\n-0e0\n1.234567e6\n" {
		t.Errorf("NaN scientific: %q", got)
	}
	f32 := ursus.Frame(ursus.Values("f32", []float32{1.5, 0.1}))
	if got := csvOut(t, f32, ursus.WithFloatPrecision(3)); got != "f32\n1.500\n0.100\n" {
		t.Errorf("a Float32's precision: %q", got)
	}
	if got := csvOut(t, ursus.Frame(ursus.Values("f32", []float32{2, 1e-7, 0.1}))); got != "f32\n2.0\n1e-7\n0.1\n" {
		t.Errorf("a Float32's own text: %q", got)
	}
	// A Decimal keeps its scale under a float precision, and takes the comma.
	dec := ursus.Frame(strs("d", "1.5", "-2.25"), ursus.Values("f", []float64{1.25, 2.5})).
		Select(ursus.Col("d").Cast(ursus.Decimal(10, 2)), ursus.Col("f"))
	if got := csvOut(t, dec, ursus.WithFloatPrecision(1)); got != "d,f\n1.50,1.2\n-2.25,2.5\n" {
		t.Errorf("a Decimal under a precision: %q", got)
	}
	if got := csvOut(t, dec.Select(ursus.Col("d")), ursus.WithDecimalComma(true)); got != "d\n\"1,50\"\n\"-2,25\"\n" {
		t.Errorf("a Decimal's comma: %q", got)
	}
	// An integer is not quoted for a comma; a null is not under QuoteNonNumeric.
	if got := csvOut(t, ursus.Frame(i64s("i", 1), ursus.Values("f", []float64{2.5})), ursus.WithFloatPrecision(1), ursus.WithDecimalComma(true), ursus.WithQuoteStyle(ursus.QuoteNonNumeric)); got != "\"i\",\"f\"\n1,\"2,5\"\n" {
		t.Errorf("an integer beside a comma: %q", got)
	}
	nulls := ursus.Frame(i64s("i", nil), ursus.ValuesNullable("b", []bool{false}, []bool{false}), strs("s", nil))
	if got := csvOut(t, nulls, ursus.WithNullValue("NA"), ursus.WithQuoteStyle(ursus.QuoteNonNumeric)); got != "\"i\",\"b\",\"s\"\nNA,NA,NA\n" {
		t.Errorf("nulls under QuoteNonNumeric: %q", got)
	}
	if got := csvOut(t, ursus.Frame(strs("s", "", nil)), ursus.WithQuoteStyle(ursus.QuoteNever)); got != "s\n\n\n" {
		t.Errorf("QuoteNever's empty text: %q", got)
	}
}

// TestCSVWriteInstants is an instant's text by its unit and zone, Polars'.
func TestCSVWriteInstants(t *testing.T) {
	c := ursus.Col
	at := ursus.Frame(ursus.Values("t", []time.Time{time.Date(2020, 1, 2, 3, 4, 5, 123456000, time.UTC)}))
	for _, tc := range []struct {
		to   ursus.DataType
		opts []ursus.CSVSinkOption
		want string
	}{
		{ursus.Datetime(ursus.Micro, ""), nil, "2020-01-02T03:04:05.123456"},
		{ursus.Datetime(ursus.Milli, ""), nil, "2020-01-02T03:04:05.123"},
		{ursus.Datetime(ursus.Nano, ""), nil, "2020-01-02T03:04:05.123456000"},
		{ursus.Datetime(ursus.Micro, "UTC"), nil, "2020-01-02T03:04:05.123456+0000"},
		{ursus.Datetime(ursus.Micro, "Asia/Tokyo"), nil, "2020-01-02T12:04:05.123456+0900"},
		{ursus.Datetime(ursus.Micro, "UTC"), []ursus.CSVSinkOption{ursus.WithDatetimeFormat("%Y-%m-%d %H:%M%z")}, "2020-01-02 03:04+0000"},
		{ursus.Datetime(ursus.Micro, "UTC"), []ursus.CSVSinkOption{ursus.WithDatetimeFormat("%Y-%m-%d %H:%M:%S %Z")}, "2020-01-02 03:04:05 UTC"},
		{ursus.Datetime(ursus.Micro, ""), []ursus.CSVSinkOption{ursus.WithDatetimeFormat("%Y-%m-%dT%H:%M:%S%.3f")}, "2020-01-02T03:04:05.123"},
		// A Date takes no Datetime's format, nor a Datetime a Date's.
		{ursus.Date, []ursus.CSVSinkOption{ursus.WithDatetimeFormat("%Y")}, "2020-01-02"},
		{ursus.Datetime(ursus.Micro, ""), []ursus.CSVSinkOption{ursus.WithDateFormat("%Y")}, "2020-01-02T03:04:05.123456"},
		{ursus.TimeOf(ursus.Nano), []ursus.CSVSinkOption{ursus.WithTimeFormat("%H:%M:%S%.3f")}, "03:04:05.123"},
	} {
		got := csvOut(t, at.Select(c("t").Cast(tc.to)), tc.opts...)
		if want := "t\n" + tc.want + "\n"; got != want {
			t.Errorf("%s %v: %q, want %q", tc.to, tc.opts != nil, got, want)
		}
	}
	// Read back by ScanCSV, an offset +0900 as Polars writes it.
	for _, to := range []ursus.DataType{ursus.Datetime(ursus.Micro, ""), ursus.Datetime(ursus.Micro, "UTC"), ursus.Datetime(ursus.Micro, "Asia/Tokyo")} {
		text := csvOut(t, at.Select(c("t").Cast(to)))
		back, err := ursus.ScanCSVReader([]byte(text), "t.csv", ursus.WithSchemaOverrides(map[string]ursus.DataType{"t": to})).Collect(t.Context())
		if err != nil {
			t.Fatalf("%s read back: %v", to, err)
		}
		want, err := at.Select(c("t").Cast(to)).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if eq, err := back.Equals(t.Context(), want, true); err != nil || !eq {
			t.Errorf("%s read back: %v, want %v (%v)", to, back, want, err)
		}
	}
	// Polars: "cannot format NaiveDateTime with format '%Y %z'".
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		opt  ursus.CSVSinkOption
	}{
		{"a naive instant's zone", at.Select(c("t").Cast(ursus.Datetime(ursus.Micro, ""))), ursus.WithDatetimeFormat("%Y %z")},
		{"a Date's time of day", at.Select(c("t").Cast(ursus.Date)), ursus.WithDateFormat("%H")},
		{"a Time's date", at.Select(c("t").Cast(ursus.TimeOf(ursus.Nano))), ursus.WithTimeFormat("%Y")},
		{"no such directive", at, ursus.WithDatetimeFormat("%Q")},
		{"a negative precision", at, ursus.WithFloatPrecision(-2)},
	} {
		if err := tc.lf.WriteCSV(t.Context(), io.Discard, tc.opt); !errors.Is(err, ursus.ErrValue) {
			t.Errorf("%s: %v, want a value error", tc.name, err)
		}
	}
}

// TestCSVWriteCompressed is a CSV written by gzip or Zstandard, read back by its first
// bytes, and SinkCSV's check of its path's extension, Polars'.
func TestCSVWriteCompressed(t *testing.T) {
	ctx := t.Context()
	lf := csvProbeFrame()
	plain := csvOut(t, lf)
	for _, tc := range []struct {
		name  string
		opts  []ursus.CSVSinkOption
		magic string
		open  func(io.Reader) (io.Reader, error)
	}{
		{"gzip", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip)}, "\x1f\x8b",
			func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
		{"gzip at 9", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip), ursus.WithCSVCompressionLevel(9)}, "\x1f\x8b",
			func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
		{"Zstandard", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVZstd)}, "\x28\xb5\x2f\xfd",
			func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) }},
		{"Zstandard at 19", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVZstd), ursus.WithCSVCompressionLevel(19)}, "\x28\xb5\x2f\xfd",
			func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) }},
	} {
		raw := csvOut(t, lf, tc.opts...)
		if !strings.HasPrefix(raw, tc.magic) {
			t.Errorf("%s: begins %q", tc.name, raw[:4])
			continue
		}
		r, err := tc.open(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		text, err := io.ReadAll(r)
		if err != nil || string(text) != plain {
			t.Errorf("%s: decompressed %q, %v", tc.name, text, err)
		}
		back, err := ursus.ScanCSVReader([]byte(raw), "c.csv").Collect(ctx)
		if err != nil || back.Height() != 3 {
			t.Errorf("%s: read back %v, %v", tc.name, back, err)
		}
	}

	// A codec's level past its range is refused; Polars panics past gzip's 9.
	for _, tc := range []struct {
		c     ursus.CSVCompression
		level int
	}{{ursus.CSVGzip, 10}, {ursus.CSVGzip, -2}, {ursus.CSVZstd, 0}, {ursus.CSVZstd, 23}} {
		if err := lf.WriteCSV(ctx, io.Discard, ursus.WithCSVCompression(tc.c), ursus.WithCSVCompressionLevel(tc.level)); !errors.Is(err, ursus.ErrValue) {
			t.Errorf("codec %d at %d: %v", tc.c, tc.level, err)
		}
	}

	// SinkCSV's path ends as its codec's files do, and an uncompressed one's does not,
	// unless told otherwise: Polars' check_extension.
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		opts []ursus.CSVSinkOption
		ok   bool
	}{
		{"c.csv.gz", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip)}, true},
		{"c.gz", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip)}, true},
		{"c.csv", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip)}, false},
		{"c.csv.zst", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVZstd)}, true},
		{"c.csv.zstd", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVZstd)}, false},
		{"plain.csv.zst", nil, false},
		{"plain.txt", nil, true},
		{"unchecked.csv", []ursus.CSVSinkOption{ursus.WithCSVCompression(ursus.CSVGzip), ursus.WithCSVCheckExtension(false)}, true},
	} {
		path := filepath.Join(dir, tc.name)
		err := lf.SinkCSV(ctx, path, tc.opts...)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if _, statErr := os.Stat(path); (statErr == nil) != tc.ok {
			t.Errorf("%s: written %v", tc.name, statErr == nil)
		}
		if tc.ok {
			back, err := ursus.ScanCSV(path).Collect(ctx)
			if err != nil || back.Height() != 3 {
				t.Errorf("%s: read back %v, %v", tc.name, back, err)
			}
		}
	}
}
