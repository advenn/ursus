package ursus_test

// The rollmore group (step 496, from rollmore_test.go's hand tables): rolling_skew,
// rolling_kurtosis, rolling_rank, rolling_cov and rolling_corr, each spelled as
// bench/scripts/oracle/rollmore.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, i, y, g := c("x"), c("i"), c("y"), c("g")
	min, center := ursus.MinSamples, ursus.RollingCenter
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["rollmore"] = map[string]oracleQuery{
		"skew_4":                 sel(x.RollingSkew(4, true)),
		"skew_4_unbiased":        sel(x.RollingSkew(4, false)),
		"skew_4_min2":            sel(x.RollingSkew(4, true, min(2))),
		"kurt_4":                 sel(x.RollingKurtosis(4, true, true)),
		"kurt_4_unbiased":        sel(x.RollingKurtosis(4, true, false)),
		"kurt_4_pearson":         sel(x.RollingKurtosis(4, false, true)),
		"kurt_4_min1":            sel(x.RollingKurtosis(4, true, true, min(1))),
		"skew_3_ints":            sel(i.RollingSkew(3, true)),
		"skew_3_center":          sel(x.RollingSkew(3, true, center())),
		"skew_3_over_g":          sel(x.RollingSkew(3, true).Over(g)),
		"skew_3_flat_nan":        sel(x.RollingSkew(3, true)),
		"kurt_3_flat_nan":        sel(x.RollingKurtosis(3, true, true)),
		"skew_3_nan_in_window":   sel(x.RollingSkew(3, true)),
		"kurt_3_nan_in_window":   sel(x.RollingKurtosis(3, true, true)),
		"skew_4_near_1e8":        sel(x.RollingSkew(4, true)),
		"kurt_4_near_1e8":        sel(x.RollingKurtosis(4, true, true)),
		"skew_4_near_1e8_center": sel(x.RollingSkew(4, true, center())),
		"skew_1_one_row":         sel(x.RollingSkew(1, true)),
		"skew_3_alike_after":     sel(x.RollingSkew(3, true)),
		"kurt_3_alike_after":     sel(x.RollingKurtosis(3, true, true)),
		"skew_2_ulp_apart":       sel(x.RollingSkew(2, true)),
		"skew_3_ulp_apart":       sel(x.RollingSkew(3, true)),
		"kurt_3_ulp_apart":       sel(x.RollingKurtosis(3, true, true)),
		"skew_4_alike_after":     sel(x.RollingSkew(4, true)),
		"moments_300": sel(x.RollingSkew(5, true).Alias("sk5"), x.RollingKurtosis(7, true, true).Alias("ku7"),
			x.RollingKurtosis(7, true, false, min(4)).Alias("ku7u4"),
			x.RollingSkew(9, true, center(), min(3)).Alias("sk9c3")),
		"skew_3_f32":          sel(i.Cast(ursus.Float32).RollingSkew(3, true)),
		"skew_3_bool_refused": sel(x.RollingSkew(3, true)),
		"moments_gen": sel(x.RollingSkew(5, true).Alias("sk5"), x.RollingSkew(6, false, min(3)).Alias("sk6u3"),
			x.RollingKurtosis(6, true, true).Alias("ku6"),
			x.RollingKurtosis(7, false, false, center(), min(4)).Alias("ku7pc4")),

		"rank_min_4":                   sel(x.RollingRank(4, ursus.RankMin)),
		"rank_max_4":                   sel(x.RollingRank(4, ursus.RankMax)),
		"rank_dense_4":                 sel(x.RollingRank(4, ursus.RankDense)),
		"rank_min_3_ints":              sel(i.RollingRank(3, ursus.RankMin)),
		"rank_min_3_ties":              sel(x.RollingRank(3, ursus.RankMin)),
		"rank_max_3_ties":              sel(x.RollingRank(3, ursus.RankMax)),
		"rank_dense_3_ties":            sel(x.RollingRank(3, ursus.RankDense)),
		"rank_dense_4_dup":             sel(x.RollingRank(4, ursus.RankDense)),
		"rank_min_4_dup":               sel(x.RollingRank(4, ursus.RankMin)),
		"rank_min_4_min1":              sel(x.RollingRank(4, ursus.RankMin, min(1))),
		"rank_min_3_nan":               sel(x.RollingRank(3, ursus.RankMin)),
		"rank_max_3_nan":               sel(x.RollingRank(3, ursus.RankMax)),
		"rank_min_2_zeros":             sel(x.RollingRank(2, ursus.RankMin)),
		"rank_max_2_zeros":             sel(x.RollingRank(2, ursus.RankMax)),
		"rank_min_2_f32":               sel(x.RollingRank(2, ursus.RankMin)),
		"rank_min_3_center":            sel(x.RollingRank(3, ursus.RankMin, center())),
		"rank_min_4_center_min1":       sel(x.RollingRank(4, ursus.RankMin, center(), min(1))),
		"rank_min_3_center_null_after": sel(x.RollingRank(3, ursus.RankMin, center(), min(1))),
		"rank_avg_4":                   sel(x.RollingRank(4, ursus.RankAverage)),
		"rank_avg_3_ints":              sel(i.RollingRank(3, ursus.RankAverage)),
		"rank_avg_3_ties":              sel(x.RollingRank(3, ursus.RankAverage)),
		"rank_avg_2_dates":             sel(x.RollingRank(2, ursus.RankAverage)),
		"rank_avg_2_bools":             sel(x.RollingRank(2, ursus.RankAverage)),
		"rank_avg_2_u64":               sel(x.RollingRank(2, ursus.RankAverage)),
		"rank_avg_4_center":            sel(x.RollingRank(4, ursus.RankAverage, center())),
		"rank_min_3_min1_over_g":       sel(x.RollingRank(3, ursus.RankMin, min(1)).Over(g)),
		"rank_gen": sel(x.RollingRank(5, ursus.RankMin).Alias("min5"), x.RollingRank(5, ursus.RankMax).Alias("max5"),
			x.RollingRank(5, ursus.RankDense).Alias("dense5"), x.RollingRank(5, ursus.RankAverage).Alias("avg5"),
			x.RollingRank(6, ursus.RankMin, center(), min(2)).Alias("min6c2"),
			c("f").RollingRank(4, ursus.RankAverage, min(1)).Alias("favg4")),

		"cov_4":                sel(ursus.RollingCov(x, y, 4)),
		"corr_4":               sel(ursus.RollingCorr(x, y, 4)),
		"cov_4_min2_ddof0":     sel(ursus.RollingCov(x, y, 4, min(2), ursus.Ddof(0))),
		"cov_3_ints":           sel(ursus.RollingCov(i, i, 3)),
		"corr_3_int_y":         sel(ursus.RollingCorr(i, y, 3)),
		"cov_3_f32":            sel(ursus.RollingCov(x.Cast(ursus.Float32), y.Cast(ursus.Float32), 3)),
		"cov_2_f32_f64":        sel(ursus.RollingCov(x.Cast(ursus.Float32), y, 2)),
		"corr_3_f32":           sel(ursus.RollingCorr(x.Cast(ursus.Float32), y.Cast(ursus.Float32), 3)),
		"corr_2_one_side_flat": sel(ursus.RollingCorr(x, y, 2)),
		"cov_3_min2_over_g":    sel(ursus.RollingCov(x, y, 3, min(2)).Over(g)),
		"pairs_gen": sel(ursus.RollingCov(x, y, 6).Alias("cov6"), ursus.RollingCov(x, y, 5, min(3), ursus.Ddof(0)).Alias("cov5d0"),
			ursus.RollingCorr(x, y, 6).Alias("corr6"), ursus.RollingCorr(x, y, 8, min(4)).Over(g).Alias("corr8g")),
	}
}
