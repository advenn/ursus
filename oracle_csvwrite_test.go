package ursus_test

// The csvwrite group (step 502, from csvwrite_test.go's texts): write_csv's options,
// an instant's text by its unit and zone, and the text read back, each WriteCSV of
// the input under the param options, the write_csv arguments bench/scripts/oracle/
// csvwrite.py asks Polars with, the text compared byte for byte; an instant is the
// input's t cast to the param to, and a refusal is a value error.

import (
	"bytes"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	write := func(in oracleInputs, lf *ursus.LazyFrame) *ursus.LazyFrame {
		var b bytes.Buffer
		err := lf.WriteCSV(in.t.Context(), &b, in.csvWriteOptions()...)
		return in.written(b.String(), err)
	}
	wr := func(in oracleInputs) *ursus.LazyFrame { return write(in, in.shaped(in.df())) }
	instant := func(in oracleInputs) *ursus.LazyFrame {
		return in.df().Select(c("t").Cast(oracleDType(in.t, in.text("to"))))
	}
	// inst is the instant written.
	inst := func(in oracleInputs) *ursus.LazyFrame { return write(in, instant(in)) }
	// back is the instant written and read back by ScanCSV under the options.
	back := func(in oracleInputs) *ursus.LazyFrame {
		var b bytes.Buffer
		if err := instant(in).WriteCSV(in.t.Context(), &b); err != nil {
			in.t.Fatal(err)
		}
		return ursus.ScanCSVReader(b.Bytes(), "t.csv", in.csvOptions()...)
	}
	reg := map[string]oracleQuery{
		"instant_us_read_back":    back,
		"instant_utc_read_back":   back,
		"instant_tokyo_read_back": back,
	}
	for _, name := range []string{
		"default", "quote_necessary", "quote_always", "quote_never", "quote_non_numeric", "own_quote", "formats", "bom",
		"null_text_quoted", "precision_2", "precision_0", "scientific", "never_scientific", "scientific_precision_2",
		"decimal_comma", "decimal_comma_semicolon", "own_text", "own_text_never_scientific", "nan_precision_2",
		"nan_scientific", "float32_precision_3", "float32_own_text", "decimal_under_precision", "decimal_comma_of_a_decimal",
		"integer_beside_comma", "nulls_non_numeric", "never_empty_text", "default_gen", "quote_always_gen",
		"precision_3_gen", "scientific_gen", "decimal_comma_gen", "formats_gen",
	} {
		reg[name] = wr
	}
	for _, name := range []string{
		"instant_us", "instant_ms", "instant_ns", "instant_utc", "instant_tokyo", "instant_utc_format_offset",
		"instant_utc_format_zone_name", "instant_format_millis", "date_ignores_datetime_format", "instant_ignores_date_format",
		"time_format_millis",
	} {
		reg[name] = inst
	}
	for _, name := range []string{"naive_instant_zone_refused", "date_hour_refused", "time_year_refused", "no_such_directive_refused"} {
		reg[name] = oracleRefusedAs(ursus.ErrValue, inst)
	}
	oracleGroups["csvwrite"] = reg
}
