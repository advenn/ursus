package ursus_test

// The temporalagg group (step 498, from temporalagg_test.go's tables): the mean,
// median and quantiles of Dates, Datetimes, Times and Durations, each spelled as
// bench/scripts/oracle/temporalagg.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	quantiles := map[string]func() ursus.Expr{
		"0.5_linear":   func() ursus.Expr { return ursus.All().Quantile(0.5, ursus.InterpLinear) },
		"0.25_linear":  func() ursus.Expr { return ursus.All().Quantile(0.25, ursus.InterpLinear) },
		"0.5_nearest":  func() ursus.Expr { return ursus.All().Quantile(0.5, ursus.InterpNearest) },
		"0.9_lower":    func() ursus.Expr { return ursus.All().Quantile(0.9, ursus.InterpLower) },
		"0.9_higher":   func() ursus.Expr { return ursus.All().Quantile(0.9, ursus.InterpHigher) },
		"0.3_midpoint": func() ursus.Expr { return ursus.All().Quantile(0.3, ursus.InterpMidpoint) },
	}
	reg := map[string]oracleQuery{
		"date_mean":                      sel(c("d").Mean()),
		"date_median_even":               sel(c("d").Median()),
		"date_mean_before_1970":          sel(c("d").Mean()),
		"datetime_mean":                  sel(c("t").Mean()),
		"datetime_median":                sel(c("t").Median()),
		"datetime_mean_truncated":        sel(c("t").Mean()),
		"datetime_median_even_truncated": sel(c("t").Median()),
		"datetime_mean_before_1970":      sel(c("t").Mean()),
		"datetime_median_before_1970":    sel(c("t").Median()),
		"time_mean_median":               sel(c("u").Mean().Alias("mean"), c("u").Median().Alias("median")),
		"duration_median":                sel(c("du").Median()),
		"grouped_mean": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("g")).Agg(c("t").Mean().Alias("m"))
		},
		"mean_median_gen": sel(c("d").Mean().Alias("d_mean"), c("d").Median().Alias("d_median"), c("t").Mean().Alias("t_mean"),
			c("t").Median().Alias("t_median"), c("tz").Mean().Alias("tz_mean"), c("tz").Median().Alias("tz_median"),
			c("u").Mean().Alias("u_mean"), c("u").Median().Alias("u_median"), c("tm").Mean().Alias("tm_mean"), c("tm").Median().Alias("tm_median")),
	}
	for name, q := range quantiles {
		reg["quantile_"+name] = sel(q())
		reg["quantile_gen_"+name] = sel(q())
	}
	oracleGroups["temporalagg"] = reg
}
