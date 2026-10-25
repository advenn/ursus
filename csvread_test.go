package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestCSVReadOptions is step 357: Polars' read_csv options ignore_errors,
// infer_schema, raise_if_empty, decimal_comma and encoding, and text checked as UTF-8.
// Every answer is Polars 1.44.1's.
func TestCSVReadOptions(t *testing.T) {
	ctx := t.Context()
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte(text), "r.csv", opts...)
	}
	types := func(m map[string]ursus.DataType) ursus.CSVOption { return ursus.WithSchemaOverrides(m) }
	const fffd = "\xef\xbf\xbd" // U+FFFD, the replacement character
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		col  string
		want string
	}{
		{"ignore_errors", read("a\n1\nx\n3\n", types(map[string]ursus.DataType{"a": ursus.Int64}), ursus.WithIgnoreErrors(true)), "a", "1 <nil> 3 Int64"},
		{"ignore_errors of a date", read("a\n2020-13-45\n", types(map[string]ursus.DataType{"a": ursus.Date}), ursus.WithIgnoreErrors(true)), "a", "<nil> Date"},
		// A short record is padded and a blank line is a record of nulls, by default.
		{"a short record", read("a,b\n1,2\n3\n"), "b", "2 <nil> Int64"},
		{"a blank line", read("a,b\n1,2\n\n3,4\n"), "b", "2 <nil> 4 Int64"},
		{"a blank line last", read("a,b\n1,2\n\n\n"), "a", "1 <nil> <nil> Int64"},
		{"a blank line in CRLF", read("a,b\r\n1,2\r\n\r\n3,4\r\n"), "a", "1 <nil> 3 Int64"},
		{"a blank line before the header", read("\na,b\n1,2\n"), "a", "1 Int64"},
		{"a blank line first, no header", read("\n1,2\n3,4\n", ursus.WithHasHeader(false)), "column_2", "<nil> 2 4 Int64"},
		{"a blank line among strings", read("a,b\n1,x\n\n3,y\n"), "b", "x <nil> y String"},
		{"infer_schema=False", read("a,b\n1,2\n", ursus.WithInferSchema(false)), "a", "1 String"},
		{"infer_schema=False, an override", read("a,b\n1,2\n", ursus.WithInferSchema(false), types(map[string]ursus.DataType{"a": ursus.Int64})), "a", "1 Int64"},
		{"infer_schema=False, the other", read("a,b\n1,2\n", ursus.WithInferSchema(false), types(map[string]ursus.DataType{"a": ursus.Int64})), "b", "2 String"},
		{"a header alone", read("a,b\n", ursus.WithRaiseIfEmpty(false)), "a", " String"},
		{"a decimal comma", read("a;b\n1,5;2\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true)), "a", "1.5 Float64"},
		{"its other column", read("a;b\n1,5;2\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true)), "b", "2 Int64"},
		{"a point under a decimal comma", read("a;b\n1.5;2\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true)), "a", "1.5 String"},
		{"a thousands' point", read("a;b\n1.234,5;2\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true)), "a", "1.234,5 String"},
		{"an exponent", read("a;b\n1,5e3;2\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true)), "a", "1500 Float64"},
		{"a quoted comma", read("a,b\n\"1,5\",2\n", ursus.WithDecimalComma(true)), "a", "1.5 Float64"},
		{"a Float32's comma", read("a;b\n1,5;2\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true), types(map[string]ursus.DataType{"b": ursus.Float32})), "b", "2 Float32"},
		{"a Float32's comma read", read("a;b\n1,5;2,25\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true), types(map[string]ursus.DataType{"b": ursus.Float32})), "b", "2.25 Float32"},
		{"a short record under a limit", read("a,b\n1,2\n3\n").Head(2), "b", "2 <nil> Int64"},
		{"ignore_errors under a limit", read("a\n1\nx\n3\n", types(map[string]ursus.DataType{"a": ursus.Int64}), ursus.WithIgnoreErrors(true)).Head(3), "a", "1 <nil> 3 Int64"},
		{"a Decimal's comma", read("a;b\n1,5;x\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true), types(map[string]ursus.DataType{"a": ursus.Decimal(10, 2)})), "a", "1.50 Decimal(10, 2)"},
		{"utf8-lossy", read("a\n\xff\xfe\n", ursus.WithEncoding(ursus.UTF8Lossy)), "a", fffd + fffd + " String"},
		{"a maximal subpart", read("a\n\xe2\x82A\n", ursus.WithEncoding(ursus.UTF8Lossy)), "a", fffd + "A String"},
		{"a sign and a stray byte", read("a\n\xe2\x82\xac\xff\n", ursus.WithEncoding(ursus.UTF8Lossy)), "a", "\xe2\x82\xac" + fffd + " String"},
		// A name that is not UTF-8 is read so whatever the encoding.
		{"a name not UTF-8", read("a\xff,b\n1,1\n"), "a" + fffd, "1 Int64"},
		{"text under a limit", read("a\n東京\nZürich\n\xe2\x82\xac\n").Head(3), "a", "東京 Zürich € String"},
		{"text not UTF-8 under a limit", read("a\n\xff\nZürich\n", ursus.WithEncoding(ursus.UTF8Lossy)).Head(2), "a", fffd + " Zürich String"},
	} {
		// One thread reads by the serial path, four by blocks parsed at once.
		for _, th := range []int{1, 4} {
			if got := cellsOn(t, tc.lf, tc.col, th); got != tc.want {
				t.Errorf("%s, %d threads: %q, want %q", tc.name, th, got, tc.want)
			}
		}
	}

	// raise_if_empty=False of an empty file: a frame of no columns and no rows. Polars
	// reads a file of blank lines and no header as a column of nulls, a row a line;
	// it is no rows here.
	for _, tc := range []struct {
		text   string
		header bool
	}{{"", true}, {"\n", true}, {"\n\n", true}, {"", false}} {
		df, err := read(tc.text, ursus.WithRaiseIfEmpty(false), ursus.WithHasHeader(tc.header)).Collect(ctx)
		if err != nil || df.Width() != 0 || df.Height() != 0 {
			t.Errorf("%q, header %v, raise_if_empty=False: %v, %v", tc.text, tc.header, df, err)
		}
	}

	// Refused, as Polars refuses them.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		says string
	}{
		// "could not parse `x` as dtype `i64`"
		{"a value without ignore_errors", read("a\n1\nx\n", types(map[string]ursus.DataType{"a": ursus.Int64})), "WithIgnoreErrors"},
		// "found more fields than defined in 'Schema'"
		{"a long record under ignore_errors", read("a,b\n1,2\n3,4,5\n", ursus.WithIgnoreErrors(true)), "3 fields"},
		// "empty data"
		{"an empty file", read(""), "WithRaiseIfEmpty"},
		{"an empty file without a header", read("", ursus.WithHasHeader(false)), "WithRaiseIfEmpty"},
		// "invalid utf-8 sequence"
		{"text not UTF-8", read("a,b\n1,\xff\n"), "UTF8Lossy"},
		{"text not UTF-8 as a number", read("a,b\n\xff,1\n", types(map[string]ursus.DataType{"a": ursus.Int64})), "Int64"},
		{"text not UTF-8 under ignore_errors", read("a,b\n1,\xff\n", ursus.WithIgnoreErrors(true)), "UTF8Lossy"},
		// Two fields whose bytes together are a character, €, each not one.
		{"a character cut across two fields", read("a\n\xe2\x82\n\xac\n"), "UTF8Lossy"},
		{"a character cut across two fields, quoted", read("a,b\n\"\xe2\x82\",1\n\"\xac\",2\n"), "UTF8Lossy"},
		// A limit reads by the staged path on several threads.
		{"text not UTF-8 under a limit", read("a,b\n1,x\n2,\xff\n").Head(2), "UTF8Lossy"},
		{"a cut character under a limit", read("a\n\xe2\x82\n\xac\n").Head(2), "UTF8Lossy"},
		{"text not UTF-8 under ignore_errors and a limit", read("a,b\n1,\xff\n", ursus.WithIgnoreErrors(true)).Head(1), "UTF8Lossy"},
		// A separator past ASCII, 0xA9, splits ©, C2 A9, which is UTF-8, into a field
		// that is not.
		{"a separator within a character", read("x\xa9y\na\xc2\xa9b\n", ursus.WithSeparator(0xA9)), "UTF8Lossy"},
	} {
		for _, th := range []int{1, 4} {
			_, err := tc.lf.Collect(ctx, ursus.WithThreads(th))
			if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("%s, %d threads: %v, want an error saying %q", tc.name, th, err, tc.says)
			}
		}
	}
}

// cellsOn is cells, collected on threads threads.
func cellsOn(t *testing.T, q *ursus.LazyFrame, name string, threads int) string {
	t.Helper()
	df, err := q.Collect(t.Context(), ursus.WithThreads(threads))
	if err != nil {
		t.Fatal(err)
	}
	return cells(t, df.Lazy(), name)
}
