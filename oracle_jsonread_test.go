package ursus_test

// The jsonread group (step 502, from jsonread_test.go's cases): read_json, a document
// of an array of objects or of one object, its refusals, a key past the inferred
// objects, schema overrides, and write_json's document read back, each ScanJSONBytes
// of the param json under the param options, the read_json arguments
// bench/scripts/oracle/jsonread.py asks Polars with, shaped by the params select and
// tail; the two NDJSON overrides are ScanNDJSONBytes of the param ndjson.

import (
	"bytes"

	"github.com/advenn/ursus"
)

func init() {
	read := func(in oracleInputs) *ursus.LazyFrame {
		return in.shaped(ursus.ScanJSONBytes([]byte(in.text("json")), "t.json", in.ndjsonOptions()...))
	}
	nd := func(in oracleInputs) *ursus.LazyFrame {
		return ursus.ScanNDJSONBytes([]byte(in.text("ndjson")), "t.ndjson", in.ndjsonOptions()...)
	}
	reg := map[string]oracleQuery{
		"ndjson_override":                      nd,
		"ndjson_override_of_no_column_refused": nd,
		"write_json_read_back": func(in oracleInputs) *ursus.LazyFrame {
			var b bytes.Buffer
			if err := in.df().WriteJSON(in.t.Context(), &b); err != nil {
				in.t.Fatal(err)
			}
			return ursus.ScanJSONBytes(b.Bytes(), "t.json")
		},
	}
	for _, name := range []string{
		"an_array_of_objects", "spread_over_lines", "a_byte_order_mark", "a_key_twice", "text_beside_a_number", "an_empty_object_first",
		"one_column_alone", "one_object_one_row", "empty_document_refused", "a_number_refused", "arrays_refused",
		"a_number_among_objects_refused", "a_null_among_objects_refused", "a_trailing_comma_refused", "text_after_the_end_refused",
		"two_documents_refused", "an_unclosed_array_refused", "an_unclosed_object_refused", "a_malformed_object_refused",
		"a_key_past_the_inferred_objects_refused", "a_key_past_the_inferred_objects_in_a_struct_refused",
		"a_key_inferred_from_every_object", "a_key_a_given_schema_skips", "a_late_text_refused", "override_float32",
		"override_string", "override_date", "override_nested", "override_over_a_schema", "override_of_no_column_refused",
		"round_trip_gen", "pretty_gen",
	} {
		reg[name] = read
	}
	oracleGroups["jsonread"] = reg
}
