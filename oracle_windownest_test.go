package ursus_test

// The windownest group (step 500, from windownest_test.go's tables): a window inside
// a window, and an aggregate inside an aggregate's input, each spelled as
// bench/scripts/oracle/windownest.py asks Polars. A count is a Uint64 here and an
// integer Sum an Int128, so those are cast to Int64 where their type is not the
// question.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, t, g, h := c("x"), c("t"), c("g"), c("h")
	i64 := ursus.Int64
	byT := func(keys ...ursus.Expr) ursus.WindowSpec {
		return ursus.WindowSpec{PartitionBy: keys, OrderBy: []ursus.SortKey{ursus.Asc(t)}}
	}
	windows := map[string]ursus.Expr{
		"shifted_then_summed":               x.Shift(1).CumSum(false),
		"summed_then_shifted":               x.CumSum(false).Shift(1),
		"within_g":                          x.Shift(1).CumSum(false).Over(g),
		"diff_within_g":                     x.Diff(1).CumSum(false).Over(g),
		"agg_of_shift_within_g":             x.Shift(1).Sum().Over(g),
		"within_g_by_t":                     x.Shift(1).CumSum(false).OverWith(byT(g)),
		"over_g_within_h":                   x.Shift(1).Over(g).CumSum(false).Over(h),
		"over_g_within_h_by_t":              x.Shift(1).Over(g).CumSum(false).OverWith(byT(h)),
		"over_g_by_t_within_h":              x.Shift(1).OverWith(byT(g)).CumSum(false).Over(h),
		"shift_within_h_by_t":               x.Shift(1).CumSum(false).OverWith(byT(h)),
		"over_g_then_frame":                 x.Shift(1).Over(g).CumSum(false),
		"sum_over_g_then_frame":             x.Sum().Over(g).CumSum(false),
		"sum_over_g_within_h":               x.Sum().Over(g).CumSum(false).Over(h),
		"mean_of_ranks_within_g":            x.Rank(ursus.RankAverage, false).Mean().Over(g),
		"mean_in_running_sum_within_g":      x.Sub(x.Mean()).CumSum(false).Over(g),
		"mean_in_running_sum_within_g_by_t": x.Sub(x.Mean()).CumSum(false).OverWith(byT(g)),
	}
	aggs := map[string]ursus.Expr{
		"dist_sum":        x.Sub(x.Mean()).Abs().Sum(),
		"max_share":       x.Div(x.Sum()).Max(),
		"count_over_mean": x.Filter(x.Gt(x.Mean())).Count().Cast(i64),
		"mean_of_shift":   x.Shift(1).Mean(),
		"mean_of_sum":     x.Sum().Mean(),
		"var_by_hand":     x.Sub(x.Mean()).Pow(2).Mean(),
		"max_dist":        x.Sub(x.Mean()).Max(),
		"t_of_least_dist": t.SortBy(x.Sub(x.Mean())).First(),
	}
	sel := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(e.Alias("r")) }
	}
	byG := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(e.Alias("r")).Sort(ursus.Asc(g)) }
	}
	reg := map[string]oracleQuery{
		"sum_over_g_of_sum_over_g": sel(c("v").Sum().Over(g).Sum().Over(g).Cast(i64)),
		"dist_sum_with_columns": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(aggs["dist_sum"].Alias("r"))
		},
		"dist_sum_select_beside": func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(x, aggs["dist_sum"].Alias("r")) },
		"mix_sub_mean_by_g":      byG(x.Sub(x.Mean())),
		"mix_sum_add_by_g":       byG(x.Sum().Add(x)),
		"dist_sum_over_g": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(aggs["dist_sum"].Over(g).Alias("r"))
		},
		"int_key_null_beside_zero": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(c("v").Sum().Over(c("k")).Cast(i64).Alias("s"), c("v").CumSum(false).Over(c("k")).Cast(i64).Alias("c"))
		},
		"aggregates_over_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(aggs["dist_sum"].Over(g).Alias("dist_sum"), aggs["var_by_hand"].Over(h).Alias("var_h"),
				aggs["max_dist"].Over(g, h).Alias("max_dist_gh"))
		},
	}
	// The generated cases' columns in the Python module's order.
	windowNames := []string{"shifted_then_summed", "summed_then_shifted", "within_g", "diff_within_g", "agg_of_shift_within_g", "within_g_by_t",
		"over_g_within_h", "over_g_within_h_by_t", "over_g_by_t_within_h", "shift_within_h_by_t", "over_g_then_frame", "sum_over_g_then_frame",
		"sum_over_g_within_h", "mean_of_ranks_within_g", "mean_in_running_sum_within_g", "mean_in_running_sum_within_g_by_t"}
	aggNames := []string{"dist_sum", "max_share", "count_over_mean", "mean_of_shift", "mean_of_sum", "var_by_hand", "max_dist", "t_of_least_dist"}
	var windowsGen, aggsGen []ursus.Expr
	for _, name := range windowNames {
		reg[name] = sel(windows[name])
		windowsGen = append(windowsGen, windows[name].Alias(name))
	}
	for _, name := range aggNames {
		reg[name+"_frame"] = sel(aggs[name])
		reg[name+"_by_g"] = byG(aggs[name])
		aggsGen = append(aggsGen, aggs[name].Alias(name))
	}
	reg["windows_gen"] = func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(windowsGen...) }
	reg["aggregates_gen"] = func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(aggsGen...).Sort(ursus.Asc(g)) }
	oracleGroups["windownest"] = reg

	// A sum of only a null is null here, 0 in Polars (feature-gaps.md §9): h = 2's
	// g = a holds only the null x, and wf's c only the null v.
	oracleDifferences["windownest/sum_over_g_within_h"] = oracleDifference{
		why:   "a sum over g of only a null is null here, 0 in Polars, so its running sum within h is null at that row",
		cells: map[string]map[int]any{"r": {3: nil}},
	}
	oracleDifferences["windownest/sum_over_g_of_sum_over_g"] = oracleDifference{
		why:   "c's sum of only a null is null here, 0 in Polars",
		cells: map[string]map[int]any{"r": {5: nil}},
	}
}
