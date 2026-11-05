package ursus_test

// The aggpair group (step 500, from aggpair_test.go's tables): Corr, Cov, MinBy and
// MaxBy, each spelled as bench/scripts/oracle/aggpair.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, y, v, by, g := c("x"), c("y"), c("v"), c("by"), c("g")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	agg := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy().Agg(exprs...) }
	}
	corrCov := agg(ursus.Corr(x, y).Alias("corr"), ursus.Cov(x, y, 1).Alias("cov1"))
	minMax := agg(v.MinBy(by).Alias("lo"), v.MaxBy(by).Alias("hi"))
	byG := func(in oracleInputs) *ursus.LazyFrame {
		return in.df().GroupBy(g).Agg(ursus.Corr(x, y).Alias("corr"), ursus.Cov(x, y, 1).Alias("cov1"), ursus.Cov(x, y, 0).Alias("cov0")).
			Sort(ursus.Asc(g))
	}
	oracleGroups["aggpair"] = map[string]oracleQuery{
		"corr_cov_by_g":                  byG,
		"corr_cov_two_rows":              corrCov,
		"corr_cov_null_dropped_pairwise": corrCov,
		"corr_cov_nan":                   corrCov,
		"corr_cov_every_x_null":          corrCov,
		"corr_cov_x_flat":                corrCov,
		"corr_cov_near_equal_large":      corrCov,
		"corr_cov_ints_self":             agg(ursus.Corr(c("i"), c("i")).Alias("corr"), ursus.Cov(c("i"), c("i"), 1).Alias("cov")),
		"min_by_max_by_tie_earliest":     minMax,
		"min_by_max_by_null_by_skipped":  minMax,
		"min_by_max_by_null_by_last":     minMax,
		"min_by_max_by_every_by_null":    minMax,
		"min_by_max_by_null_value":       minMax,
		"min_by_max_by_nan":              minMax,
		"min_by_max_by_only_nans":        minMax,
		"min_by_max_by_string":           minMax,
		"min_by_max_by_integer":          minMax,
		"min_by_max_by_date":             minMax,
		"min_by_max_by_boolean":          minMax,
		"min_by_max_by_decimal":          minMax,
		"min_by_max_by_by_g": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(g).Agg(v.MinBy(by).Alias("lo"), v.MaxBy(by.Mul(ursus.Lit(2.0))).Alias("hi")).Sort(ursus.Asc(g))
		},
		"min_by_over_g":   sel(v.MinBy(by).Over(g)),
		"min_by_expanded": agg(ursus.Col("a", "b").MinBy(c("k"))),
		"corr_cov_gen":    byG,
		"min_by_max_by_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(g).Agg(v.MinBy(by).Alias("lo"), v.MaxBy(by).Alias("hi")).Sort(ursus.Asc(g))
		},
		"min_by_max_by_over_gen": sel(v.MinBy(by).Over(g).Alias("lo"), v.MaxBy(by).Over(g).Alias("hi")),
	}
	// MaxBy orders by as Max does, NaN the greatest (feature-gaps.md §9: Max propagates
	// NaN; MaxBy's doc), so it chooses the NaN's row, b, where Polars skips it, c.
	oracleDifferences["aggpair/min_by_max_by_nan"] = oracleDifference{
		why:   "MaxBy chooses a NaN's row here, NaN being the greatest as for Max; Polars' max_by skips it",
		cells: map[string]map[int]any{"hi": {0: "b"}},
	}
}
