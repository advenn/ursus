package ursus_test

// The dtype group (step 503, from dtype/dtype_test.go's Promote and PromoteExact
// tables): the type two types meet at, asked as the type When/Then/Otherwise gives
// its branches (Promote) and as the type Unpivot melts two columns to (PromoteExact),
// each spelled as bench/scripts/oracle/dtype.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	when := func(in oracleInputs) *ursus.LazyFrame {
		return in.df().Select(ursus.When(c("c")).Then(c("a")).Otherwise(c("b")).Alias("x"))
	}
	melt := func(in oracleInputs) *ursus.LazyFrame {
		return in.df().Unpivot(ursus.UnpivotOptions{On: []string{"a", "b"}, Index: []string{"k"}})
	}
	// Where Promote meets two types otherwise than Polars 2.1, on purpose (step 507,
	// feature-gaps §9): two time units at the finer and two Enums at the String, each
	// asked of Polars with both sides cast to ursus's type first, and asked here as
	// written. The pairs Promote refuses and Polars meets are TestCondRefusals'.
	oracleGroups["dtype"] = map[string]oracleQuery{
		"when_int64_int64":     when,
		"when_int8_int64":      when,
		"when_uint8_uint32":    when,
		"when_float32_float64": when,
		"when_null_int32":      when,
		"when_int32_null":      when,
		"when_int8_uint8":      when,
		"when_int64_uint8":     when,
		"when_int16_uint32":    when,
		"when_float32_int16":   when,
		"when_float32_int32":   when,
		"when_float64_int64":   when,
		"when_uint64_int64":    when,
		"when_int8_uint64":     when,
		"when_int128_int64":    when,
		"when_int128_uint64":   when,
		"when_int128_int8":     when,
		"when_string_string":   when,
		"when_enum_a_string":   when,
		"when_enum_a_enum_b":   when,
		"when_enum_a_int64":    when,

		"when_dt_us_utc_dt_ns_utc":   when,
		"when_dt_ms_dt_ns":           when,
		"when_dur_ms_dur_ns":         when,
		"when_dt_us_utc_dt_us":       when,
		"when_dt_us_utc_dt_us_paris": when,
		"when_dt_us_utc_dt_ns_paris": when,
		"when_dur_ns_dt_ns":          when,
		"when_time_dt_ns":            when,

		"unpivot_int32_float32":   melt,
		"unpivot_uint32_float64":  melt,
		"unpivot_int16_float32":   melt,
		"unpivot_int64_uint64":    melt,
		"unpivot_float32_float64": melt,
		"unpivot_dt_ms_dt_ns":     melt,
		"unpivot_int32_int64":     melt,

		"when_gen_int8_uint8":       when,
		"when_gen_float32_int16":    when,
		"when_gen_uint64_int64":     when,
		"when_gen_float32_int32":    when,
		"unpivot_gen_int32_float32": melt,
	}
}
