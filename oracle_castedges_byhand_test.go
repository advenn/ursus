package ursus_test

// The castedges_byhand group (step 497, from castedges_byhand_test.go's hand
// answers): audit.md's S18 to S20, each spelled as
// bench/scripts/oracle/castedges_byhand.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	x := ursus.Col("x")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	timeNs := ursus.TimeOf(ursus.Nano)
	oracleGroups["castedges_byhand"] = map[string]oracleQuery{
		"i64_to_time_lossy":    sel(x.CastLossy(timeNs)),
		"i64_to_time_inside":   sel(x.Cast(timeNs)),
		"datetime_to_time":     sel(x.Cast(timeNs)),
		"i64_to_time_gen":      sel(x.CastLossy(timeNs)),
		"datetime_to_time_gen": sel(x.Cast(timeNs)),

		"dur_us_to_ms_truncates": sel(x.Cast(ursus.Duration(ursus.Milli))),
		"dur_ns_to_us_truncates": sel(x.Cast(ursus.Duration(ursus.Micro))),
		"dt_us_to_ms_floors":     sel(x.Cast(ursus.Datetime(ursus.Micro, "")).Cast(ursus.Datetime(ursus.Milli, ""))),
		"dur_coarser_gen":        sel(x.Cast(ursus.Duration(ursus.Micro)).Alias("us"), x.Cast(ursus.Duration(ursus.Milli)).Alias("ms")),
		"dt_coarser_gen":         sel(x.Cast(ursus.Datetime(ursus.Milli, "")).Alias("ms"), x.Cast(ursus.Datetime(ursus.Nano, "")).Alias("ns")),

		"abs_neg_duration":     sel(x.Abs().Alias("a"), x.Neg().Alias("n")),
		"abs_min_i64_wraps":    sel(x.Abs()),
		"abs_neg_duration_gen": sel(x.Abs().Alias("a"), x.Neg().Alias("n")),
	}
}
