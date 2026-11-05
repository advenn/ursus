package ursus_test

// The ewmmore group (step 500, from ewmmore_test.go's tables and fingerprints):
// ewm_sum, ewm_mean_by and ewm_sum_by, each spelled as
// bench/scripts/oracle/ewmmore.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, i, t, g := c("x"), c("i"), c("t"), c("g")
	x32 := x.Cast(ursus.Float32)
	half := ursus.EwmAlpha(0.5)
	every := ursus.Every
	naive := func(unit ursus.TimeUnit) ursus.Expr { return t.Cast(ursus.Datetime(unit, "")) }
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["ewmmore"] = map[string]oracleQuery{
		"sum_alpha_05":              sel(x.EwmSum(half)),
		"sum_nulls_ignored":         sel(x.EwmSum(half, ursus.IgnoreNulls())),
		"sum_min_samples_2":         sel(x.EwmSum(half, ursus.MinSamples(2))),
		"sum_span_3":                sel(x.EwmSum(ursus.EwmSpan(3))),
		"sum_ints_com_1":            sel(i.EwmSum(ursus.EwmCom(1))),
		"sum_over_g":                sel(x.EwmSum(half).Over(g)),
		"sum_nan_inf":               sel(x.EwmSum(half)),
		"sum_leading_nulls":         sel(x.EwmSum(half)),
		"sum_leading_nulls_ignored": sel(x.EwmSum(half, ursus.IgnoreNulls())),
		"sum_f32_alpha_03":          sel(x32.EwmSum(ursus.EwmAlpha(0.3))),

		"mean_by_2d":          sel(x.EwmMeanBy(t, every("2d"))),
		"mean_by_ints_1d":     sel(i.EwmMeanBy(t, every("1d"))),
		"mean_by_over_g":      sel(x.EwmMeanBy(t, every("2d")).Over(g)),
		"mean_by_date_1d":     sel(x.EwmMeanBy(t.Dt().Date(), every("1d"))),
		"mean_by_date_36h":    sel(x.EwmMeanBy(t.Dt().Date(), every("36h"))),
		"mean_by_ms_1500us":   sel(x.EwmMeanBy(naive(ursus.Milli), every("1500us"))),
		"mean_by_ns_31h17m3s": sel(x.EwmMeanBy(naive(ursus.Nano), every("31h17m3s"))),
		"mean_by_zoned_24h":   sel(x.EwmMeanBy(t.Dt().ReplaceTimeZone("Europe/London"), every("24h"))),
		"mean_by_half_life_0": sel(x.EwmMeanBy(t, ursus.Interval{})),
		"mean_by_unsorted":    sel(x.EwmMeanBy(t, every("1d"))),
		"mean_by_null_time":   sel(x.EwmMeanBy(t, every("1d"))),
		"mean_by_nan":         sel(x.EwmMeanBy(t, every("2d"))),
		"mean_by_one_instant": sel(x.EwmMeanBy(t, every("1d"))),
		"mean_by_constant":    sel(x.EwmMeanBy(t, every("31h17m3s"))),
		"sum_by_2d":           sel(x.EwmSumBy(t, every("2d"))),
		"mean_by_f32_36h":     sel(x32.EwmMeanBy(t, every("36h"))),
		"sum_by_f32_36h":      sel(x32.EwmSumBy(t, every("36h"))),
		"fingerprints_300": sel(x.EwmMeanBy(t, every("5h")).Alias("mean_5h"), x.EwmSumBy(t, every("31h17m3s")).Alias("sum_31h"),
			x.EwmSum(ursus.EwmAlpha(0.1), ursus.IgnoreNulls()).Alias("sum_01_ignore")),
		"fingerprints_300_f32": sel(x32.EwmMeanBy(t, every("5h")).Alias("mean_5h"), x32.EwmSumBy(t, every("7h")).Alias("sum_7h"),
			x32.EwmSum(ursus.EwmAlpha(0.3)).Alias("sum_03")),
		"sum_gen": sel(x.EwmSum(ursus.EwmAlpha(0.2)).Alias("a02"), x.EwmSum(ursus.EwmSpan(5), ursus.IgnoreNulls(), ursus.MinSamples(3)).Alias("s5i3"),
			x.EwmSum(ursus.EwmCom(2)).Alias("c2"), x.EwmSum(ursus.EwmHalfLife(3)).Alias("h3"), x.EwmSum(ursus.EwmAlpha(0.4)).Over(g).Alias("a04g")),
		"by_gen": sel(x.EwmMeanBy(t, every("12h")).Alias("m12h"), x.EwmSumBy(t, every("3d")).Alias("s3d"),
			x.EwmMeanBy(t, every("1d")).Over(g).Alias("m1dg")),
	}
}
