package ursus_test

// The weeks group (step 498, from weeks_test.go's table): truncate and round by weeks,
// which start on a Monday, and by "7d", which counts from the epoch, and
// GroupByDynamic's windows of each, spelled as bench/scripts/oracle/weeks.py asks
// Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	d, t := c("d").Dt(), c("t").Dt()
	every := ursus.Every
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	// An integer's Sum is an Int128 here, widened not to wrap (internal/expr.
	// ResolveAggBinding), where Polars' is an Int64: cast, as the numodds group casts
	// it, the question being the windows.
	windows := func(spelled string) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupByDynamic(c("t"), ursus.DynamicOptions{Every: every(spelled)}).Agg(c("v").Sum().Cast(ursus.Int64))
		}
	}
	oracleGroups["weeks"] = map[string]oracleQuery{
		"truncate_1w":                sel(d.Truncate(every("1w"))),
		"truncate_7d_from_the_epoch": sel(d.Truncate(every("7d"))),
		"truncate_2w":                sel(d.Truncate(every("2w"))),
		"round_1w":                   sel(d.Round(every("1w"))),
		"date_week":                  sel(d.Truncate(every("1w"))),
		"week_in_london":             sel(d.Truncate(every("1w"))),
		"group_by_dynamic_1w":        windows("1w"),
		"group_by_dynamic_7d":        windows("7d"),

		"weeks_gen": sel(t.Truncate(every("1w")).Alias("w1"), t.Truncate(every("2w")).Alias("w2"), t.Truncate(every("7d")).Alias("d7"),
			t.Round(every("1w")).Alias("r1w"), t.Round(every("7d")).Alias("r7d")),
		"date_weeks_gen":       sel(d.Truncate(every("1w")).Alias("w1"), d.Truncate(every("2w")).Alias("w2"), d.Round(every("1w")).Alias("r1w")),
		"group_by_dynamic_gen": windows("1w"),
	}
}
