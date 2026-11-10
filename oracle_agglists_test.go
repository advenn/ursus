package ursus_test

// The agglists group (step 500, from agglists_test.go's table): a non-aggregate in an
// aggregation, each group's values as a list, spelled as
// bench/scripts/oracle/agglists.py asks Polars. An integer Sum is an Int128 here,
// cumulative or under Over (feature-gaps.md §9), so those are cast to Int64.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, y, h, g := c("x"), c("y"), c("h"), c("g")
	i64 := ursus.Int64
	byG := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(exprs...).Sort(ursus.Asc(g)) }
	}
	oracleGroups["agglists"] = map[string]oracleQuery{
		"column":                  byG(x),
		"arithmetic":              byG(x.Mul(2)),
		"two_columns":             byG(x.Add(y)),
		"cast":                    byG(x.Cast(ursus.Float64)),
		"conditional":             byG(ursus.When(y.Gt(2)).Then(x).Otherwise(0)),
		"alias":                   byG(x.Mul(2).Alias("z")),
		"name_suffix":             byG(x.Mul(2).Name().Suffix("_2")),
		"filter":                  byG(x.Filter(y.Gt(2))),
		"cum_sum":                 byG(x.CumSum(false).Cast(i64)),
		"shift":                   byG(x.Shift(1)),
		"diff":                    byG(x.Diff(1)),
		"interpolate":             byG(x.Interpolate()),
		"window_under_arithmetic": byG(x.CumSum(false).Mul(10).Cast(i64)),
		"over_y":                  byG(x.Sum().Over(y).Cast(i64)),
		"over_h":                  byG(x.Sum().Over(h).Cast(i64)),
		"aggregate_list_literal":  byG(x.Sum().Cast(i64), y.Alias("ys"), ursus.Lit(int64(1)).Alias("one")),
		"window_in_aggregate":     byG(x.CumSum(false).Max().Cast(i64)),
		"mix_mean":                byG(x.Sub(x.Mean())),
		"lists_gen": byG(x.Alias("col"), x.Mul(2).Alias("mul"), x.Add(y).Alias("add"), x.Cast(ursus.Float64).Alias("f"),
			ursus.When(y.Gt(4)).Then(x).Otherwise(0).Alias("cond"), x.Filter(y.Gt(4)).Alias("flt"), x.CumSum(false).Cast(i64).Alias("cs"),
			x.Shift(1).Alias("sh"), x.Diff(1).Alias("df"), x.Interpolate().Alias("ip"), x.Sub(x.Mean()).Alias("mix"),
			x.CumSum(false).Mul(10).Cast(i64).Alias("cs10")),
		"lists_over_gen": byG(x.Sum().Over(y).Cast(i64).Alias("over_y"), x.CumSum(false).Over(y).Cast(i64).Alias("cs_over_y"),
			x.CumSum(false).Max().Cast(i64).Alias("cs_max")),
	}
	// Within group 2 every y differs, so each row's Over is its own x; Polars sums the
	// null row to 0 where a sum of only nulls is null here (feature-gaps.md §9).
	oracleDifferences["agglists/over_y"] = oracleDifference{
		why:   "a sum of only a null is null here, 0 in Polars",
		cells: map[string]map[int]any{"x": {1: []any{3, nil, 5}}},
	}
}
