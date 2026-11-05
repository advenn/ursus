package ursus_test

// The expressions group (step 184's R4, re-recorded under Polars 2.1.0 at step 496):
// 0.5's expressions, each over a small frame and aliased r, with Describe and
// ValueCounts as frames. bench/scripts/oracle/expressions.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c, every := ursus.Col, ursus.Every
	r := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(e.Alias("r")) }
	}
	oracleGroups["expressions"] = map[string]oracleQuery{
		"rolling_sum_3":              r(c("x").RollingSum(3)),
		"rolling_mean_3_min1":        r(c("x").RollingMean(3, ursus.MinSamples(1))),
		"rolling_min_4_min2":         r(c("x").RollingMin(4, ursus.MinSamples(2))),
		"rolling_max_2":              r(c("x").RollingMax(2)),
		"rolling_var_3_min2":         r(c("x").RollingVar(3, ursus.MinSamples(2))),
		"rolling_std_4_min1_ddof0":   r(c("x").RollingStd(4, ursus.MinSamples(1), ursus.Ddof(0))),
		"rolling_sum_3_center":       r(c("x").RollingSum(3, ursus.RollingCenter())),
		"rolling_mean_4_center_min1": r(c("x").RollingMean(4, ursus.RollingCenter(), ursus.MinSamples(1))),
		"rolling_sum_by_1d":          r(c("x").RollingSumBy(c("t"), every("1d"))),
		"rolling_max_by_40d":         r(c("x").RollingMaxBy(c("t"), every("40d"))),

		"ewm_mean_alpha05":           r(c("x").EwmMean(ursus.EwmAlpha(0.5))),
		"ewm_mean_span3_noadjust":    r(c("x").EwmMean(ursus.EwmSpan(3), ursus.EwmAdjust(false))),
		"ewm_mean_com2_ignore_nulls": r(c("x").EwmMean(ursus.EwmCom(2), ursus.IgnoreNulls())),
		"ewm_mean_alpha05_min3":      r(c("x").EwmMean(ursus.EwmAlpha(0.5), ursus.MinSamples(3))),
		"ewm_std_halflife2":          r(c("x").EwmStd(ursus.EwmHalfLife(2))),
		"ewm_var_alpha03_bias":       r(c("x").EwmVar(ursus.EwmAlpha(0.3), ursus.Biased())),
		"ewm_var_span4_noadjust":     r(c("x").EwmVar(ursus.EwmSpan(4), ursus.EwmAdjust(false))),

		"cut_0_1":         r(c("x").Cut([]float64{0, 1})),
		"cut_labels":      r(c("x").Cut([]float64{0, 1}, ursus.CutLabels("lo", "mid", "hi"))),
		"cut_left_closed": r(c("x").Cut([]float64{0, 1}, ursus.CutLeftClosed())),
		"qcut_quartiles":  r(c("x").QCut([]float64{0.25, 0.5, 0.75})),
		"qcut_3":          r(c("x").QCutN(3)),

		"concat_str_dash": r(ursus.ConcatStr("-", c("a"), c("b"))),
		"concat_str_int":  r(ursus.ConcatStr("", c("a"), c("i"))),

		"count_ones":      r(c("i").BitwiseCountOnes()),
		"count_zeros":     r(c("i").BitwiseCountZeros()),
		"leading_zeros":   r(c("i").BitwiseLeadingZeros()),
		"leading_ones":    r(c("i").BitwiseLeadingOnes()),
		"trailing_zeros":  r(c("i").BitwiseTrailingZeros()),
		"trailing_ones":   r(c("i").BitwiseTrailingOnes()),
		"count_ones_int8": r(c("i").BitwiseCountOnes()),

		"offset_by_1mo_dates":     r(c("d").Dt().OffsetBy(every("1mo"))),
		"offset_by_minus1y_dates": r(c("d").Dt().OffsetBy(every("-1y"))),
		"offset_by_1d12h":         r(c("t").Dt().OffsetBy(every("1d12h"))),
		"truncate_1w":             r(c("t").Dt().Truncate(every("1w"))),
		"truncate_1mo":            r(c("t").Dt().Truncate(every("1mo"))),
		"truncate_1q_dates":       r(c("d").Dt().Truncate(every("1q"))),
		"round_1h":                r(c("t").Dt().Round(every("1h"))),
		"round_1d":                r(c("t").Dt().Round(every("1d"))),
		"month_start":             r(c("t").Dt().MonthStart()),
		"month_end_dates":         r(c("d").Dt().MonthEnd()),
		"is_leap_year_dates":      r(c("d").Dt().IsLeapYear()),
		"convert_tokyo_hour":      r(c("t").Dt().ConvertTimeZone("Asia/Tokyo").Dt().Hour()),
		"truncate_1d_london": r(c("t").Dt().ConvertTimeZone("Europe/London").Dt().Truncate(every("1d")).
			Dt().ConvertTimeZone("UTC")),
		"replace_zone_new_york": r(c("t").Dt().ReplaceTimeZone("America/New_York", ursus.AmbiguousEarliest,
			ursus.NonExistentNull).Dt().ConvertTimeZone("UTC")),
		"strftime": r(c("t").Dt().Strftime("%Y-%m-%d %H:%M %a %j")),

		// Without the std row, which Polars' own answer holds to an ulp on one machine
		// only (expressions.py).
		"describe": func(in oracleInputs) *ursus.LazyFrame {
			return in.eager(in.df().Describe(in.t.Context())).Filter(c("statistic").Ne("std"))
		},
		"value_counts": func(in oracleInputs) *ursus.LazyFrame { return in.df().ValueCounts(c("s")) },
	}
}
