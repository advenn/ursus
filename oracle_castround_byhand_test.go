package ursus_test

// The castround_byhand group (step 497, from castround_byhand_test.go's hand
// answers): a float is not a go-between, audit.md §6's S1, S2 and S4, each spelled as
// bench/scripts/oracle/castround_byhand.py asks Polars.

import (
	"math"

	"github.com/advenn/ursus"
)

func init() {
	x := ursus.Col("x")
	even := ursus.RoundHalfToEven
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	cast := func(to ursus.DataType) oracleQuery { return sel(x.Cast(to)) }
	lossy := func(to ursus.DataType) oracleQuery { return sel(x.CastLossy(to)) }
	oracleGroups["castround_byhand"] = map[string]oracleQuery{
		"f64_to_f32":            cast(ursus.Float32),
		"f64_to_f32_lossy":      lossy(ursus.Float32),
		"f64_1e39_to_f32_lossy": lossy(ursus.Float32),
		"i64_to_f32":            cast(ursus.Float32),
		"u64_to_f32":            cast(ursus.Float32),
		"i128_to_f64":           cast(ursus.Float64),
		"i128_to_f32":           cast(ursus.Float32),
		"dec38_0_to_f32":        cast(ursus.Float32),
		"dec10_2_to_f32":        cast(ursus.Float32),
		"dt_ns_to_u64":          sel(x.Cast(ursus.Datetime(ursus.Nano, "")).Cast(ursus.Uint64)),
		"dur_ns_to_i128":        sel(x.Cast(ursus.Duration(ursus.Nano)).Cast(ursus.Int128)),
		"to_float_gen":          sel(x.Cast(ursus.Float32).Alias("f32"), x.CastLossy(ursus.Float32).Alias("lossy")),
		"ints_to_float_gen":     sel(x.Cast(ursus.Float32).Alias("f32"), x.Cast(ursus.Float64).Alias("f64")),
		"uints_to_float_gen":    sel(x.Cast(ursus.Float32).Alias("f32"), x.Cast(ursus.Float64).Alias("f64")),
		"i128_to_float_gen":     sel(x.Cast(ursus.Float32).Alias("f32"), x.Cast(ursus.Float64).Alias("f64")),
		"dec_to_float_gen":      sel(x.Cast(ursus.Float32).Alias("f32"), x.Cast(ursus.Float64).Alias("f64")),

		"str_to_i64":          sel(x.Cast(ursus.Int64).Alias("cast"), x.Str().ToInteger().Alias("to_integer")),
		"str_to_u64":          cast(ursus.Uint64),
		"str_256_to_u8_lossy": lossy(ursus.Uint8),
		"str_plus_5_to_u8":    cast(ursus.Uint8),
		"str_to_f32":          cast(ursus.Float32),
		"str_to_f32_lossy":    lossy(ursus.Float32),
		"text_ints_gen": sel(x.Cast(ursus.Int64).Alias("i64"), x.Cast(ursus.Float64).Alias("f64"), x.Cast(ursus.Float32).Alias("f32"),
			x.Str().ToInteger().Alias("to_integer")),
		"text_floats_gen": sel(x.Cast(ursus.Float64).Alias("f64"), x.Cast(ursus.Float32).Alias("f32")),

		"f32_math": sel(x.Sqrt().Alias("sqrt"), x.Round(1, even).Alias("r1"), x.Round(2, even).Alias("r2")),
		"f32_libm": sel(x.Ln().Alias("ln"), x.Exp().Alias("exp")),
		"f32_lit_sqrt": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(ursus.Lit(float32(2)).Sqrt().Alias("x"))
		},
		"f32_math_gen": sel(x.Sqrt().Alias("sqrt"), x.Round(1, even).Alias("r1"), x.Round(2, even).Alias("r2")),
		"f32_libm_gen": sel(x.Ln().Alias("ln"), x.Exp().Alias("exp")),

		"is_in_f32_0_1":   sel(x.IsIn(0.1)),
		"is_in_f32_exact": sel(x.IsIn(float32(0.1))),
		"is_in_i64_p53":   sel(x.IsIn(int64(1)<<53 + 1)),
		"is_in_u64_max":   sel(x.IsIn(uint64(math.MaxUint64))),
		"is_in_gen":       sel(x.IsIn(int64(1)<<53, int64(1)<<53+1, 0, -1, int64(math.MaxInt64), int64(math.MinInt64))),
	}
}
