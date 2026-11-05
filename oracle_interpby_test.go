package ursus_test

// The interpby group (step 498, from interpby_test.go's tables): InterpolateBy, alone
// and per group, spelled as bench/scripts/oracle/interpby.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, t := c("x"), c("t")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	by, over := sel(x.InterpolateBy(t)), sel(x.InterpolateBy(t).Over(c("g")))
	oracleGroups["interpby"] = map[string]oracleQuery{
		"on_the_line":            by,
		"by_unsorted":            by,
		"ends_stay_null":         by,
		"nan_is_a_value":         by,
		"zero_span_is_nan":       by,
		"duplicate_by":           by,
		"forty_rows_by_unsorted": by,
		"integers_to_float":      by,
		"float32":                by,
		"float32_fourteen":       by,
		"date_by":                by,
		"datetime_ns_big_ticks":  by,
		"nan_by_sorts_last":      by,
		"nan_by_first":           by,
		"per_group":              over,
		"per_group_by_mixed":     over,

		"interpolate_by_gen":       by,
		"interpolate_by_f32_gen":   by,
		"interpolate_by_over_gen":  over,
		"interpolate_by_dates_gen": by,
	}
}
