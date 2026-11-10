package ursus_test

// The topk_agg group (step 500, from topk_agg_test.go's table): TopK and BottomK
// inside a group-by, each spelled as bench/scripts/oracle/topk_agg.py asks Polars,
// which sorts each list as ursus orders it, Polars leaving a top_k's order open.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	g := c("g")
	byG := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(exprs...).Sort(ursus.Asc(g)) }
	}
	nulls := "Polars 2.1 keeps nulls in a top_k as the least values, [7, null] of [7, null] and [null, null] of three nulls, " +
		"where 1.44.1 skipped them, as ursus does (TopK's doc: a shorter list, null when none)"
	oracleGroups["topk_agg"] = map[string]oracleQuery{
		"top_k_ints":       oracleFinding(nulls),
		"bottom_k_ints":    oracleFinding(nulls),
		"top_k_floats_nan": byG(c("f").TopK(2).Alias("top")),
		"top_k_strings":    byG(c("s").TopK(3).Alias("top")),
		"bottom_k_dates":   byG(c("d").BottomK(1).Alias("top")),
		"top_k_decimals":   oracleFinding(nulls),
		"top_k_gen": byG(c("f").TopK(3).Alias("f3"), c("f").BottomK(2).Alias("f_bottom2"), c("s").TopK(2).Alias("s2"),
			c("s").BottomK(3).Alias("s_bottom3"), c("i").TopK(4).Alias("i4"), c("i").BottomK(1).Alias("i_bottom1")),
	}
}
