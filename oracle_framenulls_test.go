package ursus_test

// The framenulls group (step 501, from framenulls_test.go's hand answers): a fill
// strategy's limit, drop_nans and interpolate over a frame, the temporal types
// included, each spelled as bench/scripts/oracle/framenulls.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	q := func(f func(lf *ursus.LazyFrame) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return f(in.df()) }
	}
	fwd, bwd := ursus.FillForward, ursus.FillBackward
	oracleGroups["framenulls"] = map[string]oracleQuery{
		"forward_limit_1":  q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.FillNull(fwd.Limit(1)) }),
		"backward_limit_2": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.FillNull(bwd.Limit(2)) }),
		"forward_no_limit": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.FillNull(fwd) }),
		"forward_of_one_column_limit_1": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.FillNull(fwd.Limit(1), "i").Select(c("i"))
		}),
		"an_expressions_limit_1":    q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(c("i").FillNull(fwd.Limit(1))) }),
		"a_values_strategy_limit_2": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(c("i").FillNullWith(bwd.Limit(2))) }),
		"drop_nans":                 q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.DropNans() }),
		"drop_nans_of_f":            q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.DropNans("f") }),
		"drop_nans_of_i":            q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.DropNans("i").Select(c("i")) }),
		"interpolate":               q((*ursus.LazyFrame).Interpolate),
		"interpolate_temporal":      q((*ursus.LazyFrame).Interpolate),
		"fill_limits_gen": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Select(c("f").FillNull(fwd.Limit(2)).Alias("ff2"), c("f").FillNull(bwd.Limit(1)).Alias("bf1"),
				c("i").FillNull(fwd).Alias("ff"), c("s").FillNull(bwd.Limit(3)).Alias("bf3"))
		}),
		"frame_fill_gen":  q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.FillNull(bwd.Limit(2)) }),
		"interpolate_gen": q((*ursus.LazyFrame).Interpolate),
		"drop_nans_gen":   q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.DropNans() }),
	}
}
