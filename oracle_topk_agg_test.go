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
	// top_k_ints, bottom_k_ints and top_k_decimals are asked of Polars of the values
	// without their nulls, an empty list made null (step 507): TopK skips nulls, where
	// Polars 2.1 keeps them as the least values.
	oracleGroups["topk_agg"] = map[string]oracleQuery{
		"top_k_ints":       byG(c("i").TopK(2).Alias("top")),
		"bottom_k_ints":    byG(c("i").BottomK(2).Alias("top")),
		"top_k_floats_nan": byG(c("f").TopK(2).Alias("top")),
		"top_k_strings":    byG(c("s").TopK(3).Alias("top")),
		"bottom_k_dates":   byG(c("d").BottomK(1).Alias("top")),
		"top_k_decimals":   byG(c("dec").TopK(5).Alias("top")),
		"top_k_gen": byG(c("f").TopK(3).Alias("f3"), c("f").BottomK(2).Alias("f_bottom2"), c("s").TopK(2).Alias("s2"),
			c("s").BottomK(3).Alias("s_bottom3"), c("i").TopK(4).Alias("i4"), c("i").BottomK(1).Alias("i_bottom1")),
	}
}
