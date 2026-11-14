package ursus_test

// The dtcombine group (step 498, from dtcombine_test.go): DaysInMonth and Combine,
// spelled as bench/scripts/oracle/dtcombine.py asks Polars. A zoned Datetime's day,
// which Polars 2.1 reads in UTC, is asked of it on the wall clock (step 507).

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	d, ts, us, ms := c("d").Dt(), c("ts").Dt(), c("us").Dt(), c("ms").Dt()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	at1315 := ursus.Lit(int64((13*3600 + 15*60) * 1e9)).Cast(ursus.TimeOf(ursus.Nano))
	oracleGroups["dtcombine"] = map[string]oracleQuery{
		"days_in_month":       sel(d.DaysInMonth().Alias("dim"), ts.DaysInMonth().Alias("dimts")),
		"combine_date":        sel(d.Combine(c("t"))),
		"combine_datetime":    sel(ts.Combine(c("t"))),
		"combine_datetime_ms": sel(ms.Combine(c("t"))),
		"combine_zoned_ny":    sel(ts.Combine(at1315)),
		"days_in_month_ny":    sel(ts.DaysInMonth()),

		"combine_gen": sel(d.DaysInMonth().Alias("dim_d"), us.DaysInMonth().Alias("dim_us"), d.Combine(c("t")).Alias("c_d"),
			us.Combine(c("t")).Alias("c_us"), ms.Combine(c("t")).Alias("c_ms"), d.Combine(at1315).Alias("c_lit")),
	}
}
