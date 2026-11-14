package ursus_test

// The fill group (step 501, from fill_test.go's hand answers): the fills' values,
// fill_nan, forward and backward fills with a limit, the strategies, a frame's
// fill_null, clip, is_close, diff and pct_change, each spelled as
// bench/scripts/oracle/fill.py asks Polars. A fill past the column's type and the mean
// strategy are asked of Polars spelled to ursus's documented rules (step 507).

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	fill := func(s ursus.FillStrategy) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().FillNull(s) }
	}
	v, f := c("v"), c("f")
	strategies := func() []ursus.Expr {
		return []ursus.Expr{v.FillNull(ursus.FillZero).Alias("zero"), v.FillNull(ursus.FillOne).Alias("one"),
			v.FillNull(ursus.FillForward).Alias("forward"), v.FillNull(ursus.FillBackward).Alias("backward"),
			v.FillNull(ursus.FillMin).Alias("min"), v.FillNull(ursus.FillMax).Alias("max")}
	}
	oracleGroups["fill"] = map[string]oracleQuery{
		"fill_null_with_minus_1_int32": sel(c("i32").FillNullWith(-1)),
		"fill_past_the_type":           sel(c("i8").FillNullWith(5000)),
		"fill_u32_with_minus_1":        sel(c("u32").FillNullWith(int64(-1))),
		"fill_nan_leaves_nulls":        sel(f.Div(f).FillNan(-1.0).Alias("v")),
		"forward_and_backward": sel(v.ForwardFill(0).Alias("ff"), v.ForwardFill(1).Alias("ff1"), v.BackwardFill(0).Alias("bf"),
			v.FillNull(ursus.FillForward).Alias("strategy")),
		"forward_fill_limits_1_and_3": sel(v.ForwardFill(1).Alias("a"), v.ForwardFill(3).Alias("b")),
		"fill_strategies":             sel(strategies()...),
		"fill_strategy_mean":          sel(v.FillNull(ursus.FillMean).Alias("mean")),
		"frame_fill_null_0":           func(in oracleInputs) *ursus.LazyFrame { return in.df().FillNull(0) },
		"clip_floats":                 sel(v.Clip(0.0, 10.0).Alias("c")),
		"clip_null":                   sel(v.Clip(0.0, 10.0).Alias("c")),
		"clip_column_bounds":          sel(v.Clip(c("lo"), c("hi")).Alias("c")),
		"is_close_infinities":         sel(c("a").IsClose(c("b"), 1e-9, 0).Alias("c")),
		"diff_and_pct_change":         sel(v.Diff(1).Alias("d"), v.PctChange(1).Alias("p")),
		"clip_int32_values":           sel(v.Clip(0, 100).Alias("c")),
		"frame_fill_forward":          fill(ursus.FillForward),
		"frame_fill_backward":         fill(ursus.FillBackward),
		"frame_fill_min":              fill(ursus.FillMin),
		"frame_fill_max":              fill(ursus.FillMax),
		"frame_fill_zero":             fill(ursus.FillZero),
		"frame_fill_one":              fill(ursus.FillOne),
		"frame_fill_mean":             fill(ursus.FillMean),
		"frame_fill_subset_and_expression": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().FillNull(ursus.FillForward, "i").WithColumns(f.FillNullWith(ursus.FillForward)).Select(c("i", "f", "s"))
		},
		"fill_gen": sel(append(strategies(), v.ForwardFill(2).Alias("ff2"), v.BackwardFill(2).Alias("bf2"))...),
		"clip_gen": sel(f.Clip(-5.0, 5.0).Alias("c"), f.Clip(c("lo"), c("hi")).Alias("cols"), f.FillNan(0.0).Alias("nn"), f.Diff(1).Alias("d"),
			f.IsClose(c("lo"), 0.1, 0.5).Alias("close")),
	}
}
