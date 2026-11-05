package ursus_test

// The businessdays_range group (step 498, from businessdays_range_test.go):
// AddBusinessDays on a Date and a Datetime of each unit, spelled as
// bench/scripts/oracle/businessdays_range.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	monSat := ursus.BusinessCalendar{WeekMask: [7]bool{true, true, true, true, true, true, false}}
	ms, ns := ursus.Datetime(ursus.Milli, ""), ursus.Datetime(ursus.Nano, "")
	oracleGroups["businessdays_range"] = map[string]oracleQuery{
		"add_5_date":        sel(c("t").Cast(ursus.Date).Dt().AddBusinessDays(5, ursus.RollRaise)),
		"add_5_datetime_ns": sel(c("t").Dt().AddBusinessDays(5, ursus.RollRaise)),
		"add_5_datetime_ms": sel(c("t").Cast(ms).Dt().AddBusinessDays(5, ursus.RollRaise)),
		"add_gen": sel(c("d").Dt().AddBusinessDays(c("n"), ursus.RollForward).Alias("date"),
			c("d").Cast(ms).Dt().AddBusinessDays(c("n"), ursus.RollBackward).Alias("ms"),
			c("d").Cast(ns).Dt().AddBusinessDays(c("n"), ursus.RollForward, monSat).Alias("ns")),
	}
}
