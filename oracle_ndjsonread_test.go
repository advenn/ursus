package ursus_test

// The ndjsonread group (step 502, from ndjsonread_test.go's tables): read_ndjson, the
// types its lines meet, a schema given, errors ignored and several files, each
// ScanNDJSONBytes of the param ndjson under the param options, the read_ndjson
// arguments bench/scripts/oracle/ndjsonread.py asks Polars with; two_files writes the
// layout and scans the files.

import (
	"path/filepath"

	"github.com/advenn/ursus"
)

func init() {
	read := func(in oracleInputs) *ursus.LazyFrame {
		return ursus.ScanNDJSONBytes([]byte(in.text("ndjson")), "t.ndjson", in.ndjsonOptions()...)
	}
	reg := map[string]oracleQuery{
		"ignored_in_a_struct": oracleFinding("a value that is not an object, in a Struct column under ignore_errors, is a struct of nulls here and null in Polars 2.1"),
		"two_files": func(in oracleInputs) *ursus.LazyFrame {
			root := in.textFiles()
			return ursus.ScanNDJSONFiles([]string{filepath.Join(root, "a.ndjson"), filepath.Join(root, "b.ndjson")}, in.ndjsonOptions()...)
		},
	}
	for _, name := range []string{
		"a_column_a_key", "keys_in_the_order_first_seen", "integer_then_float", "integer_and_text", "boolean_and_text", "only_nulls",
		"null_then_integer", "past_int64", "exponent", "list_of_integers_and_a_float", "list_of_integers_and_text", "list_of_objects",
		"two_objects", "list_and_text", "text_and_object", "elements_float_and_object", "elements_float_boolean_integer",
		"elements_object_and_number", "elements_objects", "elements_past_int64_and_integer", "elements_past_int64_alone",
		"elements_empty_list_and_list", "text_of_numbers", "text_as_rust_prints", "elements_as_text", "list_rows", "struct_rows",
		"text_escapes", "boolean_and_number_refused", "object_and_number_refused", "later_lines_type_refused",
		"line_not_an_object_refused", "malformed_line_refused", "leading_zero_refused", "leading_zero_in_floats_refused",
		"keys_types_by_line_refused", "lines_place_past_a_blank_refused", "boolean_among_numbers_refused", "schema_given",
		"schema_of_each_type", "schema_refuses_text_as_int32", "schema_refuses_past_int32", "schema_refuses_negative_uint64",
		"ignored_in_integers", "ignored_in_booleans", "ignored_in_a_list", "ignored_under_a_schema", "malformed_refused_still",
		"round_trip_gen",
	} {
		reg[name] = read
	}
	oracleGroups["ndjsonread"] = reg
}
