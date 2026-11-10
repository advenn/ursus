package ursus_test

// The dynamicopts group (step 501, from dynamicopts_test.go's hand answers):
// GroupByDynamic's Label, StartBy, Offset, Period and IncludeBoundaries, each spelled
// as bench/scripts/oracle/dynamicopts.py asks Polars. ursus keeps a dynamic group's
// empty windows, Polars drops them (feature-gaps.md §9), so each case filters its
// windows on a count above zero; the sum is cast to Int64 on both sides.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	dyn := func(o ursus.DynamicOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			cols := []ursus.Expr{c("t"), c("v")}
			if o.IncludeBoundaries {
				cols = []ursus.Expr{c("_lower_boundary"), c("_upper_boundary"), c("t"), c("v")}
			}
			return in.df().GroupByDynamic(c("t"), o).Agg(c("v").Sum().Cast(ursus.Int64).Alias("v"), ursus.Len().Alias("n")).
				Filter(c("n").Gt(0)).Select(cols...)
		}
	}
	every := ursus.Every
	oracleGroups["dynamicopts"] = map[string]oracleQuery{
		"label_left":                             dyn(ursus.DynamicOptions{Every: every("1h")}),
		"label_right":                            dyn(ursus.DynamicOptions{Every: every("1h"), Label: "right"}),
		"label_datapoint":                        dyn(ursus.DynamicOptions{Every: every("1h"), Label: "datapoint"}),
		"boundaries":                             dyn(ursus.DynamicOptions{Every: every("1h"), IncludeBoundaries: true}),
		"period_2h_right_boundaries":             dyn(ursus.DynamicOptions{Every: every("1h"), Period: every("2h"), Label: "right", IncludeBoundaries: true}),
		"start_by_datapoint_hourly":              dyn(ursus.DynamicOptions{Every: every("1h"), StartBy: "datapoint"}),
		"weeks_from_monday":                      dyn(ursus.DynamicOptions{Every: every("1w")}),
		"weeks_from_wednesday":                   dyn(ursus.DynamicOptions{Every: every("1w"), StartBy: "wednesday"}),
		"weeks_from_sunday":                      dyn(ursus.DynamicOptions{Every: every("1w"), StartBy: "Sunday"}),
		"fortnights_from_friday":                 dyn(ursus.DynamicOptions{Every: every("2w"), StartBy: "friday"}),
		"weeks_from_the_first_row":               dyn(ursus.DynamicOptions{Every: every("1w"), StartBy: "datapoint"}),
		"weeks_from_wednesday_offset_3h":         dyn(ursus.DynamicOptions{Every: every("1w"), StartBy: "wednesday", Offset: every("3h")}),
		"days_from_the_first_row_offset_ignored": dyn(ursus.DynamicOptions{Every: every("1d"), StartBy: "datapoint", Offset: every("2h")}),
		"days_from_the_first_row_right":          dyn(ursus.DynamicOptions{Every: every("1d"), StartBy: "datapoint", Label: "right"}),
		"dynamic_opts_gen":                       dyn(ursus.DynamicOptions{Every: every("1d"), Period: every("3d"), Label: "right", IncludeBoundaries: true}),
		"dynamic_opts_gen_weeks":                 dyn(ursus.DynamicOptions{Every: every("1w"), StartBy: "thursday", Offset: every("-6h"), Closed: ursus.ClosedRight}),
	}
}
