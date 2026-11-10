package ursus_test

// The io_byhand group (step 502, from io_byhand_test.go's hand cases): CSV and I/O
// against audit.md §5's answers, each spelled as bench/scripts/oracle/io_byhand.py asks
// Polars: a read is ScanCSVReader of the param csv, a round trip WriteCSV read back,
// each under the param options, its read_csv arguments; a written text in.written; a
// refusal a value error, as audit.md's I9, I19 and S25 refuse.

import (
	"bytes"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	read := func(in oracleInputs) *ursus.LazyFrame {
		return ursus.ScanCSVReader(in.bytes("csv"), "x.csv", in.csvOptions()...)
	}
	write := func(in oracleInputs) *ursus.LazyFrame {
		var b bytes.Buffer
		err := in.df().WriteCSV(in.t.Context(), &b)
		return in.written(b.String(), err)
	}
	// roundTrip writes df and reads it back.
	roundTrip := func(in oracleInputs) *ursus.LazyFrame {
		var b bytes.Buffer
		if err := in.df().WriteCSV(in.t.Context(), &b); err != nil {
			in.t.Fatal(err)
		}
		return ursus.ScanCSVReader(b.Bytes(), "x.csv", in.csvOptions()...)
	}
	cast := func(dt ursus.DataType) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(c("s").Cast(dt)) }
	}
	refused := func(q oracleQuery) oracleQuery { return oracleRefusedAs(ursus.ErrValue, q) }
	oracleGroups["io_byhand"] = map[string]oracleQuery{
		"i8_amsterdam_1930_round_trips":        roundTrip,
		"i8_monrovia_1970_round_trips":         oracleFinding("Polars 2.1's round trip of Monrovia's -00:44:30 offset lands 30 s away; ursus writes the instant in UTC and reads it back exactly (I8)"),
		"control_amsterdam_2024_text":          write,
		"i9_inference_underscore_and_hex":      read,
		"i9_float64_schema_refuses_underscore": refused(read),
		"s25_cast_refuses_underscore":          refused(cast(ursus.Float64)),
		"s25_cast_refuses_hex":                 refused(cast(ursus.Float64)),
		"s25_cast_lossy_hex_is_null": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(c("s").CastLossy(ursus.Float64))
		},
		"control_float_grammar":                  cast(ursus.Float64),
		"i10_blank_line_int":                     read,
		"i10_blank_line_string":                  read,
		"i10_trailing_blank_line":                read,
		"i10_blank_line_no_header":               read,
		"blank_line_two_columns":                 read,
		"i19_u64_max_inferred":                   read,
		"i19_2p63_beside_1":                      read,
		"i19_int128_schema":                      read,
		"i19_decimal_schema_rounds_half_away":    read,
		"i19_decimal_past_precision_refused":     refused(read),
		"s21_cast_string_int128":                 cast(ursus.Int128),
		"s21_cast_string_decimal":                cast(ursus.Decimal(10, 2)),
		"i21_float64_round_trips":                roundTrip,
		"i21_integral_float64_round_trips":       roundTrip,
		"i21_specials_written":                   write,
		"control_float32_round_trips_as_float64": roundTrip,
		"control_only_nan_inferred":              oracleFinding("a column of only NaN infers String here, Float64 in Polars 2.1"),
		"i27_null_string_round_trips":            roundTrip,
		"i27_unquoted_empty_is_null":             read,
		"i22_no_rows_no_columns_written":         oracleFinding("a frame of no rows and no columns writes nothing here, a bare newline in Polars 2.1"),
		"round_trip_gen":                         roundTrip,
		"written_gen":                            write,
	}
	oracleDifferences["io_byhand/i10_blank_line_no_header"] = oracleHeaderless(1)
}
