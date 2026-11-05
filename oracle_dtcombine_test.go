package ursus_test

// The dtcombine group (step 498, from dtcombine_test.go): DaysInMonth and Combine,
// spelled as bench/scripts/oracle/dtcombine.py asks Polars. One case is a finding,
// recorded and skipped: Polars 2.1 combines a zoned Datetime's UTC date.

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
		"combine_zoned_ny": oracleFinding("Polars 2.1 combines a zoned Datetime's day as read in UTC, so 22:00 on the 11th in " +
			"New York, 02:00 on the 12th in UTC, with 13:15 is the 12th; ursus reads the day on New York's wall clock, the 11th " +
			"(Combine's doc)"),
		"days_in_month_ny": sel(ts.DaysInMonth()),

		"combine_gen": sel(d.DaysInMonth().Alias("dim_d"), us.DaysInMonth().Alias("dim_us"), d.Combine(c("t")).Alias("c_d"),
			us.Combine(c("t")).Alias("c_us"), ms.Combine(c("t")).Alias("c_ms"), d.Combine(at1315).Alias("c_lit")),
	}
}
