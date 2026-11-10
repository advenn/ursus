package ursus_test

// The intindex group (step 501, from intindex_test.go's hand answers): GroupByDynamic,
// Rolling, the rolling and ewm functions by an integer index and Upsample over one,
// each spelled as bench/scripts/oracle/intindex.py asks Polars. A count and an integer
// sum are cast to Int64 on both sides (feature-gaps.md §9).

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	every := ursus.Every("3i")
	count := ursus.Len().Cast(ursus.Int64)
	sum := c("v").Sum().Cast(ursus.Int64)
	dyn := func(o ursus.DynamicOptions, aggs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupByDynamic(c("i"), o).Agg(aggs...) }
	}
	// dynFiltered leaves out the windows of no rows, which Polars never makes (§9).
	dynFiltered := func(o ursus.DynamicOptions, aggs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupByDynamic(c("i"), o).Agg(aggs...).Filter(c("len").Gt(0))
		}
	}
	roll := func(o ursus.RollingOptions, aggs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Rolling(c("i"), o).Agg(aggs...) }
	}
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	up := func(every ursus.Interval, groups ...string) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Upsample("i", every, groups...) }
	}
	thirty := ursus.Every("30i")
	oracleGroups["intindex"] = map[string]oracleQuery{
		"every_3i":               dyn(ursus.DynamicOptions{Every: every}, count, sum),
		"every_2i_period_3i":     dyn(ursus.DynamicOptions{Every: ursus.Every("2i"), Period: every}, count, sum),
		"offset_1i":              dyn(ursus.DynamicOptions{Every: every, Offset: ursus.Every("1i")}, count),
		"boundaries":             dyn(ursus.DynamicOptions{Every: every, IncludeBoundaries: true}, count),
		"closed_both":            dyn(ursus.DynamicOptions{Every: every, Closed: ursus.ClosedBoth}, count),
		"label_right":            dyn(ursus.DynamicOptions{Every: every, Label: "right"}, count),
		"start_at_the_first_row": dyn(ursus.DynamicOptions{Every: every, StartBy: "datapoint"}, count),
		"negative":               dyn(ursus.DynamicOptions{Every: every}, count),
		"rolling_3i":             roll(ursus.RollingOptions{Period: every}, count, sum),
		"rolling_by_a_key":       roll(ursus.RollingOptions{Period: every, GroupBy: []ursus.Expr{c("g")}}, count),
		"label_datapoint":        dyn(ursus.DynamicOptions{Every: every, Label: "datapoint"}, count),
		"having": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupByDynamic(c("i"), ursus.DynamicOptions{Every: every}).Having(ursus.Len().Gt(1)).Agg(sum)
		},
		"a_shorthand": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupByDynamic(c("i"), ursus.DynamicOptions{Every: every}).First()
		},
		"rolling_over_uint32": roll(ursus.RollingOptions{Period: every}, sum),
		"rolling_over_uint64": roll(ursus.RollingOptions{Period: every}, sum),
		"int32_index_stays":   dyn(ursus.DynamicOptions{Every: every}, count),

		"rolling_sum_by":              sel(c("v").RollingSumBy(c("i"), every).Cast(ursus.Int64)),
		"rolling_mean_by_closed_both": sel(c("v").RollingMeanBy(c("i"), every, ursus.WindowClosed(ursus.ClosedBoth))),
		"ewm_mean_by":                 sel(c("v").EwmMeanBy(c("i"), ursus.Every("2i"))),
		"by_an_int32":                 sel(c("v").RollingSumBy(c("i").Cast(ursus.Int32), every).Cast(ursus.Int64)),
		"by_a_uint32":                 sel(c("v").RollingSumBy(c("i").Cast(ursus.Uint32), every).Cast(ursus.Int64)),
		"ewm_mean_by_uint64":          sel(c("v").EwmMeanBy(c("i").Cast(ursus.Uint64), ursus.Every("2i"))),

		"upsample_every_2i": up(ursus.Every("2i")),
		"upsample_int32":    up(ursus.Every("1i")),
		"upsample_uint32":   up(ursus.Every("2i")),
		"upsample_negative": up(ursus.Every("2i")),
		"upsample_groups":   up(ursus.Every("1i"), "g"),

		"dynamic_gen": dynFiltered(ursus.DynamicOptions{Every: ursus.Every("50i"), Period: ursus.Every("120i"), IncludeBoundaries: true}, count, sum),
		"dynamic_gen_closed_right_label": dynFiltered(ursus.DynamicOptions{Every: ursus.Every("70i"), Closed: ursus.ClosedRight, Label: "right"},
			count, sum),
		"rolling_gen": roll(ursus.RollingOptions{Period: thirty}, count, sum, c("f").Mean().Alias("m")),
		"by_gen": sel(c("v").RollingSumBy(c("i"), thirty).Cast(ursus.Int64).Alias("s"), c("v").RollingMaxBy(c("i"), thirty).Alias("mx"),
			c("f").RollingMeanBy(c("i"), thirty, ursus.WindowClosed(ursus.ClosedBoth)).Alias("m")),
		"upsample_int_gen": up(ursus.Every("5i")),
	}
}
