package ursus_test

// The numodds group (step 496, from numodds_test.go's hand tables): round's modes,
// truncate, round_sig_figs, cot and the one-sided clips, each spelled as
// bench/scripts/oracle/numodds.py asks Polars.

import (
	"math"
	"strconv"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	x, f := c("x"), c("f")
	even, away := ursus.RoundHalfToEven, ursus.RoundHalfAwayFromZero
	nan := math.NaN()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	figures := func(in oracleInputs) *ursus.LazyFrame {
		exprs := make([]ursus.Expr, 18)
		for d := range exprs {
			exprs[d] = x.RoundSigFigs(d + 1).Alias("d" + strconv.Itoa(d+1))
		}
		return in.df().Select(exprs...)
	}
	oracleGroups["numodds"] = map[string]oracleQuery{
		"round_even_0":           sel(x.Round(0, even)),
		"round_even_2":           sel(x.Round(2, even)),
		"round_away_0":           sel(x.Round(0)),
		"round_away_2":           sel(x.Round(2, away)),
		"round_even_2_exact":     sel(x.Round(2, even)),
		"round_even_0_past_2p52": sel(x.Round(0, even)),
		"round_even_15_thirds":   sel(x.Round(15, even)),
		"round_even_23_thirds":   sel(x.Round(23, even)),
		"round_even_300_sub":     sel(x.Round(300, even)),
		"round_even_310_sub":     sel(x.Round(310, even)),
		"round_away_310_sub":     sel(x.Round(310)),
		"round_even_325_sub":     sel(x.Round(325, even)),
		"round_away_326_sub":     sel(x.Round(326)),
		"round_even_20_big":      sel(x.Round(20, even)),
		"truncate_0":             sel(x.Truncate(0)),
		"truncate_1":             sel(x.Truncate(1)),
		"truncate_2":             sel(x.Truncate(2)),
		"truncate_20":            sel(x.Truncate(20)),
		"truncate_30_thirds":     sel(x.Truncate(30)),
		"truncate_310_sub":       sel(x.Truncate(310)),
		"round_f32_even_2":       sel(x.Round(2, even)),
		"round_f32_even_0":       sel(x.Round(0, even)),
		"round_f32_even_25":      sel(x.Round(25, even)),
		"round_f32_even_46":      sel(x.Round(46, even)),
		"round_f32_even_47":      sel(x.Round(47, even)),
		"truncate_f32_2":         sel(x.Truncate(2)),
		"truncate_int_1":         sel(x.Truncate(1)),
		"round_int_even_1":       sel(x.Round(1, even)),
		"round_modes_gen": sel(x.Round(0, even).Alias("e0"), x.Round(2, even).Alias("e2"), x.Round(15, even).Alias("e15"),
			x.Round(0).Alias("a0"), x.Round(3, away).Alias("a3"),
			x.Truncate(0).Alias("t0"), x.Truncate(2).Alias("t2"), x.Truncate(12).Alias("t12")),
		"round_f32_gen": sel(x.Round(0, even).Alias("e0"), x.Round(2, away).Alias("a2"), x.Truncate(1).Alias("t1")),

		"sig_figs_1_mixed":      sel(x.RoundSigFigs(1)),
		"sig_figs_2_mixed":      sel(x.RoundSigFigs(2)),
		"sig_figs_3_mixed":      sel(x.RoundSigFigs(3)),
		"sig_figs_1_edges":      sel(x.RoundSigFigs(1)),
		"sig_figs_2_edges":      sel(x.RoundSigFigs(2)),
		"sig_figs_20_edges":     sel(x.RoundSigFigs(20)),
		"sig_figs_1_tens":       sel(x.RoundSigFigs(1)),
		"sig_figs_3_tens":       sel(x.RoundSigFigs(3)),
		"sig_figs_17_tens":      sel(x.RoundSigFigs(17)),
		"sig_figs_17_many":      sel(x.RoundSigFigs(17)),
		"sig_figs_20_many":      sel(x.RoundSigFigs(20)),
		"sig_figs_23_many":      sel(x.RoundSigFigs(23)),
		"sig_figs_25_many":      sel(x.RoundSigFigs(25)),
		"sig_figs_30_many":      sel(x.RoundSigFigs(30)),
		"sig_figs_2_halves":     sel(x.RoundSigFigs(2)),
		"sig_figs_1_halves":     sel(x.RoundSigFigs(1)),
		"sig_figs_1e23_1_to_18": figures,
		"sig_figs_1e46_1_to_18": figures,
		"sig_figs_f32_1":        sel(x.RoundSigFigs(1)),
		"sig_figs_i64_2":        sel(x.RoundSigFigs(2)),
		"sig_figs_i64_ends_1":   sel(x.RoundSigFigs(1)),
		"sig_figs_i64_ends_2":   sel(x.RoundSigFigs(2)),
		"sig_figs_i64_19":       sel(x.RoundSigFigs(19)),
		"sig_figs_u8_1":         sel(x.RoundSigFigs(1)),
		"sig_figs_u8_2":         sel(x.RoundSigFigs(2)),
		"sig_figs_i8_1":         sel(x.RoundSigFigs(1)),
		"sig_figs_i8_2":         sel(x.RoundSigFigs(2)),
		"sig_figs_i16_1":        sel(x.RoundSigFigs(1)),
		"sig_figs_i16_2":        sel(x.RoundSigFigs(2)),
		"sig_figs_u64_1":        sel(x.RoundSigFigs(1)),
		"sig_figs_u64_2":        sel(x.RoundSigFigs(2)),
		"sig_figs_sum_2":        sel(x.Sum().RoundSigFigs(2).Cast(ursus.Int64)),
		"sig_figs_gen": sel(x.RoundSigFigs(1).Alias("d1"), x.RoundSigFigs(2).Alias("d2"), x.RoundSigFigs(3).Alias("d3"),
			x.RoundSigFigs(7).Alias("d7"), x.RoundSigFigs(17).Alias("d17")),
		"sig_figs_ints_gen": sel(x.RoundSigFigs(1).Alias("x1"), x.RoundSigFigs(3).Alias("x3"), x.RoundSigFigs(19).Alias("x19"),
			c("i16").RoundSigFigs(1).Alias("i16_1"), c("i16").RoundSigFigs(2).Alias("i16_2"),
			c("u8").RoundSigFigs(1).Alias("u8_1"), c("u8").RoundSigFigs(2).Alias("u8_2")),

		"cot":      sel(x.Cot()),
		"cot_pole": sel(x.Cot()),
		"cot_ints": sel(x.Cot()),
		"cot_f32":  sel(x.Cot()),
		"cot_gen":  sel(x.Cot()),

		"clip_lower_3":      sel(x.ClipLower(3)),
		"clip_upper_6":      sel(x.ClipUpper(6)),
		"clip_lower_col":    sel(x.ClipLower(c("lo"))),
		"clip_col_8":        sel(x.Clip(c("lo"), 8)),
		"clip_null_lower_6": sel(x.Clip(ursus.Null(ursus.Int64), 6)),
		"clip_3_null_upper": sel(x.Clip(3, ursus.Null(ursus.Int64))),
		"clip_8_2":          sel(x.Clip(8, 2)),
		"clip_f_0_05":       sel(f.Clip(0.0, 0.5)),
		"clip_f_lower_0":    sel(f.ClipLower(0.0)),
		"clip_f_lower_nan":  sel(f.ClipLower(nan)),
		"clip_i32_lower_3":  sel(x.Cast(ursus.Int32).ClipLower(3)),
		"clip_gen": sel(x.ClipLower(-10).Alias("lo10"), x.ClipUpper(10).Alias("hi10"),
			x.Clip(c("lo"), c("hi")).Alias("cols"), x.ClipLower(c("lo")).Alias("lo_col"),
			f.Clip(-1.5, 1.5).Alias("f"), f.ClipLower(nan).Alias("f_nan")),
	}
}
