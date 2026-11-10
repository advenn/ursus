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
	// Polars 2.1's supertype rules where Promote's differ, each recorded and not
	// asserted (step 503's record, Found): the maintainer's to decide.
	textAndNumber := oracleFinding("Polars 2.1's when meets text and a number at text, a Boolean and a number at the number, " +
		"a Date and a Datetime at the Datetime and a Datetime and an integer at the integer; Promote refuses each")
	coarserUnit := oracleFinding("Polars 2.1 meets two time units at the coarser one (us over ns, ms over ns); " +
		"Promote and PromoteExact take the finer (TimeUnit.Finer)")
	enums := oracleFinding("two Enums of different categories meet at the String here (step 79); Polars 2.1 refuses them")
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
		"when_string_int64":    textAndNumber,
		"when_bool_int64":      textAndNumber,
		"when_string_string":   when,
		"when_enum_a_string":   when,
		"when_enum_a_enum_b":   enums,
		"when_enum_a_int64":    when,
		"when_date_dt_us":      textAndNumber,

		"when_dt_us_utc_dt_ns_utc":   coarserUnit,
		"when_dt_ms_dt_ns":           coarserUnit,
		"when_dur_ms_dur_ns":         coarserUnit,
		"when_dt_us_utc_dt_us":       when,
		"when_dt_us_utc_dt_us_paris": when,
		"when_dt_us_utc_dt_ns_paris": when,
		"when_dur_ns_dt_ns":          when,
		"when_time_dt_ns":            when,
		"when_dt_ns_int64":           textAndNumber,

		"unpivot_int32_float32":   melt,
		"unpivot_uint32_float64":  melt,
		"unpivot_int16_float32":   melt,
		"unpivot_int64_uint64":    melt,
		"unpivot_float32_float64": melt,
		"unpivot_dt_ms_dt_ns":     coarserUnit,
		"unpivot_int32_int64":     melt,

		"when_gen_int8_uint8":       when,
		"when_gen_float32_int16":    when,
		"when_gen_uint64_int64":     when,
		"when_gen_float32_int32":    when,
		"unpivot_gen_int32_float32": melt,
	}
}
