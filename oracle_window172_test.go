package ursus_test

// The window172 group (step 500, from window172_test.go's tables): Interpolate's
// nearest method and rolling windows labelled at their middle row, each spelled as
// bench/scripts/oracle/window172.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	v, g := c("v"), c("g")
	nearest := v.Interpolate(ursus.InterpolateNearest())
	center, min := ursus.RollingCenter, ursus.MinSamples
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	centred := func(w int) oracleQuery {
		return sel(v.RollingSum(w, center()).Alias("full"), v.RollingSum(w, center(), min(1)).Alias("partial"))
	}
	oracleGroups["window172"] = map[string]oracleQuery{
		"nearest_ints":       sel(nearest),
		"nearest_ints_short": sel(nearest),
		"nearest_ints_ends":  sel(nearest),
		"nearest_floats":     sel(nearest),
		"nearest_dates":      sel(c("t").Interpolate(ursus.InterpolateNearest())),
		"rolling_center_2":   centred(2),
		"rolling_center_3":   centred(3),
		"rolling_center_4":   centred(4),
		"center_per_partition": sel(v.RollingMax(3, center(), min(1)).Over(g).Alias("centered"),
			v.RollingMax(3, min(1)).Over(g).Alias("trailing")),
		"linear_and_nearest": sel(v.Interpolate().Alias("linear"), nearest.Alias("nearest")),
		"nearest_gen":        sel(v.Interpolate().Alias("linear"), nearest.Alias("nearest")),
		"center_gen": sel(v.RollingSum(3, center()).Alias("s3"), v.RollingSum(4, center(), min(1)).Alias("s4p"),
			v.RollingMax(5, center(), min(2)).Over(g).Alias("m5g"), v.RollingMean(3, center(), min(1)).Over(g).Alias("mean3g")),
	}
}
