package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestCSVSkipAndQuoting is step 358: Polars' read_csv options skip_rows_after_header,
// skip_lines and quote_char=None. Every answer is Polars 1.44.1's.
func TestCSVSkipAndQuoting(t *testing.T) {
	ctx := t.Context()
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte(text), "s.csv", opts...)
	}
	three := "a,b\n1,2\n3,4\n5,6\n"
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		col  string
		want string
	}{
		{"skip_rows_after_header", read(three, ursus.WithSkipRowsAfterHeader(1)), "b", "4 6 Int64"},
		{"past the last row", read(three, ursus.WithSkipRowsAfterHeader(5)), "a", " String"},
		{"after skip_rows", read(three, ursus.WithSkipRows(1), ursus.WithSkipRowsAfterHeader(1)), "2", "6 Int64"},
		{"a blank line one", read("a,b\n1,2\n\n3,4\n", ursus.WithSkipRowsAfterHeader(1)), "a", "<nil> 3 Int64"},
		{"a blank line skipped", read("a,b\n\n1,2\n3,4\n", ursus.WithSkipRowsAfterHeader(1)), "a", "1 3 Int64"},
		{"a blank line skipped, text after", read("a,b\n\nx,2\n3,4\n", ursus.WithSkipRowsAfterHeader(1)), "a", "x 3 String"},
		{"a quoted record one", read("a,b\n\"x\ny\",2\n3,4\n", ursus.WithSkipRowsAfterHeader(1)), "a", "3 Int64"},
		{"no header", read("1,2\n3,4\n5,6\n", ursus.WithHasHeader(false), ursus.WithSkipRowsAfterHeader(2)), "column_1", "5 Int64"},
		{"no header, one", read(three, ursus.WithHasHeader(false), ursus.WithSkipRowsAfterHeader(1)), "column_2", "2 4 6 Int64"},
		{"skip_lines", read("junk \"q\nmore\na,b\n1,2\n", ursus.WithSkipLines(2)), "b", "2 Int64"},
		{"a comment line", read("#c\na,b\n1,2\n", ursus.WithSkipLines(1)), "a", "1 Int64"},
		{"CRLF", read("junk\r\na,b\r\n1,2\r\n", ursus.WithSkipLines(1)), "b", "2 Int64"},
		{"skip_lines, no header", read("junk\n1,2\n3,4\n", ursus.WithSkipLines(1), ursus.WithHasHeader(false)), "column_1", "1 3 Int64"},
		{"skip_lines and after the header", read("junk\na,b\n1,2\n3,4\n", ursus.WithSkipLines(1), ursus.WithSkipRowsAfterHeader(1)), "a", "3 Int64"},
		{"quote_char=None", read("a,b\n\"x,1\n", ursus.WithQuoting(false)), "a", "\"x String"},
		{"its other column", read("a,b\n\"x,1\n", ursus.WithQuoting(false)), "b", "1 Int64"},
		{"quotes kept", read("a,b\n\"x\",\"y\"\n", ursus.WithQuoting(false)), "b", "\"y\" String"},
		{"a line end in quotes", read("a,b\n\"x\ny\",2\n", ursus.WithQuoting(false)), "a", "\"x y\" String"},
		{"its short record", read("a,b\n\"x\ny\",2\n", ursus.WithQuoting(false)), "b", "<nil> 2 Int64"},
		{"an empty pair", read("a,b\n\"\",2\n,3\n", ursus.WithQuoting(false)), "a", "\"\" <nil> String"},
		{"doubled quotes", read("a,b\n\"say \"\"hi\"\"\",2\n", ursus.WithQuoting(false)), "a", "\"say \"\"hi\"\"\" String"},
		{"a separator in quotes", read("a,b,c\n\"x,y\",2\n", ursus.WithQuoting(false)), "b", "y\" String"},
		{"a number in quotes", read("a,b\n\"1\",2\n", ursus.WithQuoting(false)), "a", "\"1\" String"},
	} {
		for _, th := range []int{1, 4} {
			if got := cellsOn(t, tc.lf, tc.col, th); got != tc.want {
				t.Errorf("%s, %d threads: %q, want %q", tc.name, th, got, tc.want)
			}
		}
	}

	// Several files: each skips its own rows and lines, as Polars does.
	dir := t.TempDir()
	for _, f := range []string{"p1.csv", "p2.csv"} {
		writeText(t, dir, f, three)
	}
	if got := cells(t, ursus.ScanCSVGlob(dir+"/*.csv", ursus.WithSkipRowsAfterHeader(2)), "a"); got != "5 5 Int64" {
		t.Errorf("two files' rows: %s", got)
	}
	if got := cells(t, ursus.ScanCSVGlob(dir+"/*.csv", ursus.WithSkipLines(1), ursus.WithHasHeader(false)), "column_1"); got != "1 3 5 1 3 5 Int64" {
		t.Errorf("two files' lines: %s", got)
	}

	// Refused, as Polars refuses them.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		says string
	}{
		// "only one of 'skip_rows'/'skip_lines' may be set"
		{"skip_rows and skip_lines", read(three, ursus.WithSkipRows(1), ursus.WithSkipLines(1)), "WithSkipLines"},
		// "specified skip_lines is larger than total number of lines"
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
