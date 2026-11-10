package ursus_test

// The rollingby group (step 500, from rollingby_test.go's tables): rolling windows by
// time, each spelled as bench/scripts/oracle/rollingby.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	v, t, g := c("v"), c("t"), c("g")
	two := ursus.Every("2h")
	every, closed, min := ursus.Every, ursus.WindowClosed, ursus.MinSamples
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["rollingby"] = map[string]oracleQuery{
		"sum_2h":               sel(v.RollingSumBy(t, two)),
		"mean_2h":              sel(v.RollingMeanBy(t, two)),
		"min_2h":               sel(v.RollingMinBy(t, two)),
		"max_2h":               sel(v.RollingMaxBy(t, two)),
		"var_2h":               sel(v.RollingVarBy(t, two)),
		"std_3h":               sel(v.RollingStdBy(t, every("3h"))),
		"sum_2h_min_samples_2": sel(v.RollingSumBy(t, two, min(2))),
		"sum_2h_closed_right":  sel(v.RollingSumBy(t, two, closed(ursus.ClosedRight))),
		"sum_2h_closed_left":   sel(v.RollingSumBy(t, two, closed(ursus.ClosedLeft))),
		"sum_2h_closed_both":   sel(v.RollingSumBy(t, two, closed(ursus.ClosedBoth))),
		"sum_2h_closed_none":   sel(v.RollingSumBy(t, two, closed(ursus.ClosedNone))),
		"mean_2h_ints":         sel(v.RollingMeanBy(t, two)),
		"sum_2h_unsorted":      sel(v.RollingSumBy(t, two)),
		"sum_1h_ties":          sel(v.RollingSumBy(t, every("1h"))),
		"sum_2h_null_by":       sel(v.RollingSumBy(t, two)),
		"sum_2h_over_g":        sel(v.RollingSumBy(t, two).Over(g)),
		"sum_dates_2d":         sel(v.RollingSumBy(t, every("2d"))),
		"sum_dates_1mo":        sel(v.RollingSumBy(t, every("1mo"))),
		"sum_london_1d":        sel(v.RollingSumBy(t, every("1d"))),
		"sum_london_24h":       sel(v.RollingSumBy(t, every("24h"))),
		"names_three": sel(v.RollingSumBy(t, every("1h")).Alias("one"), v.RollingSumBy(t, two).Alias("two"),
			v.RollingSumBy(t, two, closed(ursus.ClosedBoth)).Alias("both"), v.RollingMaxBy(t, every("1h"))),
		"full_gen": sel(v.RollingSumBy(t, every("17m")).Alias("sum17"), v.RollingMeanBy(t, every("17m")).Alias("mean17"),
			v.RollingMinBy(t, every("1h")).Alias("min1h"), v.RollingMaxBy(t, every("1h")).Alias("max1h"),
			v.RollingSumBy(t, every("1h"), closed(ursus.ClosedBoth)).Alias("sum1h_both"),
			v.RollingSumBy(t, every("45m"), min(3)).Over(g).Alias("sum45g3"), v.RollingMeanBy(t, two).Over(g).Alias("mean2hg")),
		"var_gen": sel(v.RollingVarBy(t, every("1h")).Alias("var1h"), v.RollingStdBy(t, two, min(4)).Alias("std2h4"),
			v.RollingVarBy(t, every("1h")).Over(g).Alias("var1hg")),
		"nulls_gen": sel(v.RollingMeanBy(t, every("30m")).Alias("mean30"), v.RollingMinBy(t, every("30m")).Alias("min30"),
			v.RollingMaxBy(t, every("30m")).Alias("max30"), v.RollingMeanBy(t, every("1h"), closed(ursus.ClosedLeft)).Alias("mean1h_left")),
	}
	// A window by time holding no value is null here, 0.0 in Polars under its
	// min_samples of 1 (RollingSumBy's doc; feature-gaps.md §9; step 184): closed at
	// the left or at neither end, the rows at 0h, 4h and 9h hold nothing.
	empty := map[string]map[int]any{"v": {0: nil, 3: nil, 5: nil}}
	oracleDifferences["rollingby/sum_2h_closed_left"] = oracleDifference{
		why: "an empty window by time is null here, 0.0 in Polars", cells: empty}
	oracleDifferences["rollingby/sum_2h_closed_none"] = oracleDifference{
		why: "an empty window by time is null here, 0.0 in Polars", cells: empty}
}
