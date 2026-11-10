package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestCSVSkipAndQuoting is step 358: Polars' read_csv options skip_rows_after_header,
// skip_lines and quote_char=None. Each answer is Polars 2.1.0's since step 502,
// recorded in testdata/oracle/csvskip.json and spelled in oracle_csvskip_test.go. Here
// stay the two reading paths compared with each other, a frame of no columns, and a
// value error's physical line, ursus's own text.
func TestCSVSkipAndQuoting(t *testing.T) {
	ctx := t.Context()
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte(text), "s.csv", opts...)
	}
	three := "a,b\n1,2\n3,4\n5,6\n"

	// One thread reads by the serial path, four by blocks parsed at once.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"skip_rows_after_header", read(three, ursus.WithSkipRowsAfterHeader(1))},
		{"a quoted record skipped", read("a,b\n\"x\ny\",2\n3,4\n", ursus.WithSkipRowsAfterHeader(1))},
		{"skip_lines", read("junk \"q\nmore\na,b\n1,2\n", ursus.WithSkipLines(2))},
		{"quote_char=None", read("a,b\n\"x\ny\",2\n\"say \"\"hi\"\"\",3\n", ursus.WithQuoting(false))},
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

	// Refused, as Polars refuses them (the oracle records both); skip_lines past the
	// end under raise_if_empty=False is a frame of no columns, which the oracle's
	// format does not hold.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		says string
	}{
		{"skip_rows and skip_lines", read(three, ursus.WithSkipRows(1), ursus.WithSkipLines(1)), "WithSkipLines"},
		{"skip_lines past the end", read(three, ursus.WithSkipLines(5)), "WithRaiseIfEmpty"},
	} {
		_, err := tc.lf.Collect(ctx)
		if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.says)
		}
	}
	if df, err := read(three, ursus.WithSkipLines(5), ursus.WithRaiseIfEmpty(false)).Collect(ctx); err != nil || df.Width() != 0 {
		t.Errorf("skip_lines past the end, raise_if_empty=False: %v, %v", df, err)
	}
	// A value error names its physical line, the skipped lines counted.
	_, err := read("junk\nmore\na\n1\nx\n", ursus.WithSkipLines(2),
		ursus.WithSchemaOverrides(map[string]ursus.DataType{"a": ursus.Int64})).Collect(ctx)
	if err == nil || !strings.Contains(err.Error(), "line 5") {
		t.Errorf("a bad value after skipped lines: %v", err)
	}
}
