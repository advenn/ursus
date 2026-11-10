package ursus_test

// The csvskip group (step 502, from csvskip_test.go's table): read_csv's options
// skip_rows_after_header, skip_lines and quote_char=None, each ScanCSVReader of the
// param csv under the param options, the read_csv arguments bench/scripts/oracle/
// csvskip.py asks Polars with; the two-file cases write the text twice and scan the
// glob, and a refusal is a value error.

import "github.com/advenn/ursus"

func init() {
	read := func(in oracleInputs) *ursus.LazyFrame {
		return ursus.ScanCSVReader(in.bytes("csv"), "s.csv", in.csvOptions()...)
	}
	twice := func(in oracleInputs) *ursus.LazyFrame {
		dir := in.t.TempDir()
		for _, f := range []string{"p1.csv", "p2.csv"} {
			writeText(in.t, dir, f, in.text("csv"))
		}
		return ursus.ScanCSVGlob(dir+"/*.csv", in.csvOptions()...)
	}
	reg := map[string]oracleQuery{
		"two_files_rows":           twice,
		"two_files_lines":          twice,
		"skip_rows_and_skip_lines": oracleRefusedAs(ursus.ErrValue, read),
		"skip_lines_past_the_end":  oracleRefusedAs(ursus.ErrValue, read),
	}
	for _, name := range []string{
		"skip_rows_after_header", "past_the_last_row", "after_skip_rows", "blank_line_one", "blank_line_skipped",
		"blank_line_skipped_text_after", "quoted_record_one", "no_header", "no_header_one", "skip_lines", "comment_line",
		"crlf", "skip_lines_no_header", "skip_lines_and_after_header", "no_quote", "no_quote_quotes_kept",
		"no_quote_line_end_in_quotes", "no_quote_empty_pair", "no_quote_doubled_quotes", "no_quote_separator_in_quotes",
		"no_quote_number_in_quotes", "skip_gen", "no_quote_gen",
	} {
		reg[name] = read
	}
	oracleGroups["csvskip"] = reg
	for _, name := range []string{"no_header", "no_header_one", "skip_lines_no_header", "two_files_lines"} {
		oracleDifferences["csvskip/"+name] = oracleHeaderless(2)
	}
}
