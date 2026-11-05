package ursus_test

// The math group (step 497, from math_test.go's hand answers): the elementwise maths
// over the adversarial corpus, abs, neg and sign of every numeric type and of a Sum,
// the float remainder and floor division, pow and round's edges, each spelled as
// bench/scripts/oracle/math.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	v := c("v")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	exact := sel(v.Sqrt().Alias("sqrt"), v.Floor().Alias("floor"), v.Ceil().Alias("ceil"), v.Abs().Alias("abs"), v.Neg().Alias("neg"), v.Sign().Alias("sign"))
	libm := sel(v.Cbrt().Alias("cbrt"), v.Exp().Alias("exp"), v.Ln().Alias("ln"), v.Log10().Alias("log10"), v.Log1p().Alias("log1p"))
	absSign := func(cols, signed []string) oracleQuery {
		var exprs []ursus.Expr
		for _, n := range cols {
			exprs = append(exprs, c(n).Abs().Alias("abs_"+n))
		}
		for _, n := range cols {
			exprs = append(exprs, c(n).Sign().Alias("sign_"+n))
		}
		for _, n := range signed {
			exprs = append(exprs, c(n).Neg().Alias("neg_"+n))
		}
		return sel(exprs...)
	}
	oracleGroups["math"] = map[string]oracleQuery{
		"corpus_exact":     exact,
		"corpus_libm":      libm,
		"corpus_f32_exact": exact,
		"corpus_f32_libm":  libm,
		"math_gen":         exact,
		"libm_gen":         libm,
		"math_f32_gen":     exact,
		"libm_f32_gen":     libm,

		"round_int_identity": sel(c("i").Round(3)),
		"round_400":          sel(c("f").Round(400)),
		"round_gen":          sel(v.Round(400).Alias("r400"), v.Round(0).Alias("r0")),

		"abs_sign_every_type": absSign([]string{"i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "f32", "f64"},
			[]string{"i8", "i16", "i32", "i64", "f32", "f64"}),
		"sum_abs_neg_sign_sqrt": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("g")).Agg(v.Sum().Abs().Alias("abs"), v.Sum().Neg().Alias("neg"), v.Sum().Sign().Alias("sign"), v.Sum().Sqrt().Alias("sqrt"))
		},
		"u32_abs_sign": sel(c("u").Abs().Alias("a"), c("u").Sign().Alias("s")),
		"abs_sign_gen": absSign([]string{"i8", "i32", "u32", "u64", "f32"}, []string{"i8", "i32", "f32"}),

		"float_mod_floordiv":    sel(c("a").Mod(c("b")).Alias("fmod"), c("a").FloorDiv(c("b")).Alias("fdiv"), c("ia").Mod(c("ib")).Alias("imod")),
		"floordiv_stays_finite": sel(c("a").FloorDiv(c("b")).Alias("fd")),
		"mod_floordiv_gen": sel(c("a").Mod(c("b")).Alias("fmod"), c("a").FloorDiv(c("b")).Alias("fdiv"),
			c("ia").Mod(c("ib")).Alias("imod"), c("ia").FloorDiv(c("ib")).Alias("idiv")),

		"pow":     sel(v.Pow(int64(-1)).Alias("inv"), v.Pow(0.5).Alias("root"), v.Pow(int64(2)).Alias("sq")),
		"pow_gen": sel(v.Pow(2.0).Alias("sq"), v.Pow(0.5).Alias("root"), c("f").Pow(c("f")).Alias("ff"), c("f").Pow(3.0).Alias("cube")),
	}
}
