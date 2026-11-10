package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestCSVReadOptions is step 357: Polars' read_csv options ignore_errors, infer_schema,
// raise_if_empty, decimal_comma and encoding, and text checked as UTF-8. Each answer
// is Polars 2.1.0's since step 502, recorded in testdata/oracle/csvread.json and
// spelled in oracle_csvread_test.go. Here stay what the format does not hold or
// Polars has no answer for: a frame of no columns, a separator past ASCII, and the
// serial and the blocked reading paths, compared with each other.
func TestCSVReadOptions(t *testing.T) {
	ctx := t.Context()
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte(text), "r.csv", opts...)
	}
	types := func(m map[string]ursus.DataType) ursus.CSVOption { return ursus.WithSchemaOverrides(m) }
	i64 := types(map[string]ursus.DataType{"a": ursus.Int64})
	lossy := ursus.WithEncoding(ursus.UTF8Lossy)

	// One thread reads by the serial path, four by blocks parsed at once: the two
	// agree on what the oracle's cases read, each kind of text once.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"ignore_errors", read("a\n1\nx\n3\n", i64, ursus.WithIgnoreErrors(true))},
		{"a short record and a blank line", read("a,b\n1,2\n3\n\n4,5\n")},
		{"a blank line in CRLF", read("a,b\r\n1,2\r\n\r\n3,4\r\n")},
		{"no header", read("\n1,2\n3,4\n", ursus.WithHasHeader(false))},
		{"infer_schema=False", read("a,b\n1,2\n", ursus.WithInferSchema(false))},
		{"a decimal comma", read("a;b\n1,5;2,25\n", ursus.WithSeparator(';'), ursus.WithDecimalComma(true), types(map[string]ursus.DataType{"b": ursus.Float32}))},
		{"utf8-lossy", read("a\n\xff\xfe\n\xe2\x82A\n", lossy)},
		{"text under a limit", read("a\n東京\nZürich\n\xe2\x82\xac\n").Head(3)},
		{"text not UTF-8 under a limit", read("a\n\xff\nZürich\n", lossy).Head(2)},
	} {
		one, err := tc.lf.Collect(ctx, ursus.WithThreads(1))
		if err != nil {
			t.Fatalf("%s, 1 thread: %v", tc.name, err)
		}
		four, err := tc.lf.Collect(ctx, ursus.WithThreads(4))
		if err != nil {
			t.Fatalf("%s, 4 threads: %v", tc.name, err)
		}
		ursustest.AssertFrameEqual(t, four, one)
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

	// Refused on one thread and on four alike; the oracle records each refusal but
	// the separator past ASCII, 0xA9, which splits ©, C2 A9, into a field that is not
	// UTF-8, and which Polars refuses as an option.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		says string
	}{
		{"a value without ignore_errors", read("a\n1\nx\n", i64), "WithIgnoreErrors"},
		{"an empty file", read(""), "WithRaiseIfEmpty"},
		{"text not UTF-8", read("a,b\n1,\xff\n"), "UTF8Lossy"},
		{"a character cut across two fields", read("a\n\xe2\x82\n\xac\n"), "UTF8Lossy"},
		{"text not UTF-8 under a limit", read("a,b\n1,x\n2,\xff\n").Head(2), "UTF8Lossy"},
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
