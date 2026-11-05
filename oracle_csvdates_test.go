package ursus_test

// The csvdates group (step 498, from csvdates_test.go's tables): a CSV's text, the
// case's param csv, scanned with WithTryParseDates or under a given schema, each
// spelled as bench/scripts/oracle/csvdates.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	read := func(opts ...ursus.CSVOption) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return ursus.ScanCSVReader([]byte(in.text("csv")), "x.csv", append([]ursus.CSVOption{ursus.WithTryParseDates(true)}, opts...)...)
		}
	}
	given := func(dt ursus.DataType) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return ursus.ScanCSVReader([]byte(in.text("csv")), "x.csv", ursus.WithSchemaOverrides(map[string]ursus.DataType{"x": dt}))
		}
	}
	reg := map[string]oracleQuery{
		"no_try_parse":                      func(in oracleInputs) *ursus.LazyFrame { return ursus.ScanCSVReader([]byte(in.text("csv")), "x.csv") },
		"null_value_na":                     read(ursus.WithNullValues("NA")),
		"override_string_beside_a_datetime": read(ursus.WithSchemaOverrides(map[string]ursus.DataType{"x": ursus.String})),
		"given_date_day_first":              given(ursus.Date),
		"given_date_dots":                   given(ursus.Date),
		"given_date_unpadded":               given(ursus.Date),
		"given_datetime_day_first":          given(ursus.Datetime(ursus.Micro, "")),
		"given_datetime_utc_offset":         given(ursus.Datetime(ursus.Micro, "UTC")),
		"given_datetime_ms_of_a_date":       given(ursus.Datetime(ursus.Milli, "")),
		"given_time_no_seconds":             given(ursus.TimeOf(ursus.Nano)),
		"given_datetime_ns_utc":             given(ursus.Datetime(ursus.Nano, "UTC")),
		"late_value_every_row_inferred": func(in oracleInputs) *ursus.LazyFrame {
			return read(ursus.WithInferRows(0))(in).Tail(1)
		},
	}
	// Every other case is the text read with WithTryParseDates alone.
	for _, name := range []string{
		"iso_dates", "slash_dates", "dot_date", "day_first_slash", "day_first_dash", "day_first_dot", "unpadded_iso",
		"one_digit_day_first", "year_0999", "blank_line_is_null", "month_first_is_text", "not_a_date_is_text",
		"february_30_is_text", "month_13_is_text", "trailing_space_is_text", "month_name_is_text", "digits_are_an_integer",
		"datetimes_with_a_space", "datetimes_with_a_t_and_a_fraction", "a_space_beside_a_t", "nanoseconds_cut", "half_a_second",
		"no_seconds", "one_digit_hour", "day_first_datetime", "slash_datetime", "pm_is_text", "z_offset", "z_offset_fraction",
		"offset_0900", "offset_after_a_space", "offsets_with_a_colon", "offset_0530_fraction", "offset_zero",
		"zoned_beside_naive_is_text", "datetime_beside_date_is_text", "date_beside_datetime_is_text", "times", "time_fraction",
		"time_no_seconds", "hour_25_is_text", "time_pm_is_text", "time_trailing_x_is_text", "offset_trailing_x_is_text",
		"time_beside_date_is_text", "dates_gen", "day_first_gen", "datetimes_gen", "zoned_gen", "times_gen",
	} {
		reg[name] = read()
	}
	oracleGroups["csvdates"] = reg
}
