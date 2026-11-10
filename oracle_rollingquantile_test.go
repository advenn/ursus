package ursus_test

// The rollingquantile group (step 500, from rollingquantile_test.go's tables): rolling
// median and quantile by rows and by time, each spelled as
// bench/scripts/oracle/rollingquantile.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	v, i, t := c("v"), c("i"), c("t")
	min, center, every := ursus.MinSamples, ursus.RollingCenter, ursus.Every
	nearest, linear, lower, higher, midpoint := ursus.InterpNearest, ursus.InterpLinear, ursus.InterpLower, ursus.InterpHigher, ursus.InterpMidpoint
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["rollingquantile"] = map[string]oracleQuery{
		"rows": sel(v.RollingMedian(3).Alias("m3"), v.RollingMedian(3, min(1)).Alias("m3_1"),
			v.RollingQuantile(0.25, nearest, 4, min(2)).Alias("q25n"), v.RollingQuantile(0.25, linear, 4, min(2)).Alias("q25l"),
			v.RollingMedian(4, min(1), center()).Alias("mc")),
		"int32_window": sel(i.RollingMedian(2, min(1)).Alias("m"), i.RollingQuantile(0.5, nearest, 2, min(1)).Alias("q")),
		"by_time_3m":   sel(v.RollingMedianBy(t, every("3m")).Alias("m"), v.RollingQuantileBy(0.75, higher, t, every("3m")).Alias("q")),
		"rows_gen": sel(v.RollingMedian(5).Alias("m5"), v.RollingMedian(5, min(1)).Alias("m5_1"),
			v.RollingQuantile(0.25, nearest, 6, min(2)).Alias("q25n"), v.RollingQuantile(0.25, linear, 6, min(2)).Alias("q25l"),
			v.RollingQuantile(0.9, lower, 4).Alias("q9lo"), v.RollingQuantile(0.9, higher, 4).Alias("q9hi"),
			v.RollingQuantile(0.3, midpoint, 7, min(3), center()).Alias("q3mc"), v.RollingMedian(6, min(1), center()).Alias("mc")),
		"by_time_gen": sel(v.RollingMedianBy(t, every("10m")).Alias("m10"), v.RollingQuantileBy(0.75, higher, t, every("25m")).Alias("q75h"),
			v.RollingQuantileBy(0.2, linear, t, every("25m"), min(2)).Alias("q2l"),
			v.RollingMedianBy(t, every("15m"), ursus.WindowClosed(ursus.ClosedBoth)).Alias("m15b")),
	}
}
