package ursus_test

// The int128exact group (step 497, from int128exact_test.go's hand answers): the
// Int128 boundary's casts and sums, each spelled as
// bench/scripts/oracle/int128exact.py asks Polars of an Int128 column.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	v, f := c("v"), c("f")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["int128exact"] = map[string]oracleQuery{
		"sum_cast_i64":           sel(v.Sum().Cast(ursus.Int64)),
		"i128_sum_cast_u64":      sel(v.Sum().Cast(ursus.Uint64)),
		"float_to_i128_lossy":    sel(f.CastLossy(ursus.Int128)),
		"float_to_i128_whole":    sel(f.Cast(ursus.Int128)),
		"i128_cum_sum_to_max":    sel(v.CumSum(false)),
		"i128_sum_max_1_minus_1": sel(v.Sum()),
		"i128_gen":               sel(v.Cast(ursus.Float64).Alias("f64"), v.Cast(ursus.String).Alias("text"), v.Abs().Alias("abs")),
		"i128_sums_gen":          sel(v.Sum().Alias("sum"), v.CumSum(false).Alias("cum_sum")),
		"float_to_i128_gen":      sel(f.Cast(ursus.Int128)),
	}
}
