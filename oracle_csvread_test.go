package ursus_test

// The csvread group (step 502, from csvread_test.go's table): read_csv's options
// ignore_errors, infer_schema, raise_if_empty, decimal_comma and encoding, and text
// checked as UTF-8, each ScanCSVReader of the param csv (or csv_b64, bytes not UTF-8)
// under the param options, the read_csv arguments bench/scripts/oracle/csvread.py
// asks Polars with, and the param head; a refusal is a value error.

import "github.com/advenn/ursus"

func init() {
	read := func(in oracleInputs) *ursus.LazyFrame {
		return in.shaped(ursus.ScanCSVReader(in.bytes("csv"), "r.csv", in.csvOptions()...))
	}
	reg := map[string]oracleQuery{}
	for _, name := range []string{
		"ignore_errors", "ignore_errors_of_a_date", "short_record", "blank_line", "blank_line_last", "blank_line_crlf",
		"blank_line_before_header", "blank_line_first_no_header", "blank_line_among_strings", "no_inference",
		"no_inference_an_override", "header_alone", "decimal_comma", "point_under_decimal_comma", "thousands_point",
		"exponent_decimal_comma", "quoted_decimal_comma", "float32_decimal_comma", "short_record_under_limit",
		"ignore_errors_under_limit", "decimal_comma_of_a_decimal", "utf8_lossy", "utf8_lossy_maximal_subpart",
		"utf8_lossy_sign_and_stray_byte", "name_not_utf8", "text_under_limit", "lossy_text_under_limit",
		"round_trip_gen", "decimal_comma_gen",
	} {
		reg[name] = read
	}
	for _, name := range []string{
		"value_without_ignore_errors", "long_record_under_ignore_errors", "empty_file", "empty_file_no_header", "not_utf8",
		"not_utf8_as_a_number", "not_utf8_under_ignore_errors", "character_cut_across_fields",
		"character_cut_across_quoted_fields", "not_utf8_under_limit", "cut_character_under_limit",
		"not_utf8_under_ignore_errors_and_limit",
	} {
		reg[name] = oracleRefusedAs(ursus.ErrValue, read)
	}
	oracleGroups["csvread"] = reg
	oracleDifferences["csvread/blank_line_first_no_header"] = oracleHeaderless(2)
}
