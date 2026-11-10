package ursus_test

// The exprroll group (step 500, from exprroll_test.go's tables): Expr.Rolling, an
// aggregate over each row's window of a rolling group, each spelled as
// bench/scripts/oracle/exprroll.py asks Polars. A count is a Uint64 here and an
// integer Sum an Int128, so those are cast to Int64.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, t, g := c("x"), c("t"), c("g")
	i64 := ursus.Int64
	every := ursus.Every
	period := func(p string) ursus.RollingOptions { return ursus.RollingOptions{Period: every(p)} }
	offset := func(p, o string) ursus.RollingOptions {
		return ursus.RollingOptions{Period: every(p), Offset: every(o)}
	}
	noOffset := func(p string) ursus.RollingOptions {
		return ursus.RollingOptions{Period: every(p), Offset: ursus.NoOffset()}
	}
	closed := func(p string, cl ursus.Closed) ursus.RollingOptions {
		return ursus.RollingOptions{Period: every(p), Closed: cl}
	}
	sel := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(e.Alias("r")) }
	}
	plain := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["exprroll"] = map[string]oracleQuery{
		"sum_2i":            sel(x.Sum().Rolling(t, period("2i"))),
		"range_3i":          sel(x.Max().Sub(x.Min()).Rolling(t, period("3i"))),
		"count_3i":          sel(x.Count().Rolling(t, period("3i")).Cast(i64)),
		"len_3i":            sel(ursus.Len().Rolling(t, period("3i")).Cast(i64)),
		"list_2i":           sel(x.Rolling(t, period("2i"))),
		"sum_of_product_2i": sel(x.Mul(2).Sum().Rolling(t, period("2i"))),
		"no_offset_3i":      sel(x.Sum().Rolling(t, noOffset("3i"))),
		"closed_both_3i":    sel(x.Sum().Rolling(t, closed("3i", ursus.ClosedBoth))),
		"first_3i":          sel(x.First().Rolling(t, period("3i"))),
		"sort_first_3i":     sel(x.Sort().First().Rolling(t, period("3i"))),
		"mean_3i_over_g":    sel(x.Mean().Rolling(t, period("3i")).Over(g)),
		"with_columns_sum_2i": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(x.Sum().Rolling(t, period("2i")))
		},
		"sum_3i_over_g_partitions": sel(x.Sum().Rolling(t, period("3i")).Over(g)),
		"max_2i_over_g_with_columns": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(x.Max().Rolling(t, period("2i")).Over(g))
		},
		"ahead_sum_no_offset_over_g":  plain(x.Sum().Rolling(t, noOffset("3i")).Over(g)),
		"ahead_last_no_offset_over_g": plain(x.Last().Rolling(t, noOffset("3i")).Over(g)),
		"tie_last_3i_over_g":          plain(x.Last().Rolling(t, period("3i")).Over(g)),
		"first_3i_over_g_partitions":  plain(x.First().Rolling(t, period("3i")).Over(g)),
		"sum_1d":                      plain(x.Sum().Rolling(t, period("1d")).Cast(i64)),
		"sum_1d_offset_m2d":           plain(x.Sum().Rolling(t, offset("1d", "-2d")).Cast(i64)),
		"filter_sum_2d":               plain(x.Filter(x.Gt(1)).Sum().Rolling(t, period("2d")).Cast(i64)),
		"sum_3d_over_h_k":             plain(x.Sum().Rolling(t, period("3d")).Over(c("h"), c("k")).Cast(i64)),
		"mean_2i_int32":               plain(x.Mean().Rolling(t, period("2i"))),
		"in_aggregation": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(g).Agg(x.Sum().Rolling(t, period("2i"))).Sort(ursus.Asc(g))
		},
		"int_index_gen": plain(x.Sum().Rolling(t, period("5i")).Alias("sum5"), x.Mean().Rolling(t, period("5i")).Alias("mean5"),
			x.Min().Rolling(t, period("4i")).Alias("min4"), x.Max().Rolling(t, period("4i")).Alias("max4"),
			x.Sum().Rolling(t, closed("4i", ursus.ClosedBoth)).Alias("sum4_both"), x.Mean().Rolling(t, closed("4i", ursus.ClosedLeft)).Alias("mean4_left"),
			x.Max().Rolling(t, offset("4i", "-6i")).Alias("max4_m6"), x.First().Rolling(t, period("5i")).Alias("first5"),
			x.Last().Rolling(t, noOffset("3i")).Alias("last3_0"), ursus.Len().Rolling(t, period("6i")).Cast(i64).Alias("len6"),
			x.Mean().Rolling(t, period("6i")).Over(g).Alias("mean6_g"), x.Max().Sub(x.Min()).Rolling(t, period("7i")).Over(g).Alias("range7_g")),
		"var_gen": plain(x.Var(1).Rolling(t, period("6i")).Alias("var6"), x.Std(0).Rolling(t, period("5i")).Over(g).Alias("std5_g")),
	}
	// A sum of a window holding no value is null here, 0 in Polars (feature-gaps.md
	// §9): the windows ahead of b's row and a's third, the two before the first
	// instant, the filter that leaves none, and a's second window of only a null.
	empty := "a sum of a window holding no value is null here, 0 in Polars"
	oracleDifferences["exprroll/no_offset_3i"] = oracleDifference{why: empty, cells: map[string]map[int]any{"r": {5: nil}}}
	oracleDifferences["exprroll/ahead_sum_no_offset_over_g"] = oracleDifference{why: empty, cells: map[string]map[int]any{"x": {1: nil, 2: nil}}}
	oracleDifferences["exprroll/sum_1d_offset_m2d"] = oracleDifference{why: empty, cells: map[string]map[int]any{"x": {0: nil, 1: nil}}}
	oracleDifferences["exprroll/filter_sum_2d"] = oracleDifference{why: empty, cells: map[string]map[int]any{"x": {0: nil}}}
	oracleDifferences["exprroll/in_aggregation"] = oracleDifference{why: empty, cells: map[string]map[int]any{"x": {0: []any{1.0, nil, 8.0}}}}
}
