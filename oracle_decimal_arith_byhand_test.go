package ursus_test

// The decimal_arith_byhand group (step 497, from decimal_arith_byhand_test.go's hand
// answers): Decimal arithmetic where Polars has the exact answer, its comparisons, a
// concat and a conditional, each spelled as bench/scripts/oracle/decimal_arith_byhand.py
// asks Polars. Polars' sums are cast to ursus's width there; here Add answers it.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	a, b, i := c("a"), c("b"), c("i")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["decimal_arith_byhand"] = map[string]oracleQuery{
		"add":                       sel(a.Add(b)),
		"sub":                       sel(a.Sub(b)),
		"add_same_type":             sel(a.Add(a)),
		"add_max_10_2":              sel(a.Add(a)),
		"add_rescale_past_128_bits": sel(a.Add(b)),
		"add_int_col":               sel(a.Add(i)),
		"add_int_lit":               sel(a.Add(1)),
		"add_float_lit":             sel(a.Add(0.5)),
		"add_sub_gen":               sel(a.Add(b).Alias("add"), a.Sub(b).Alias("sub"), a.Add(i).Alias("add_i"), a.Add(0.5).Alias("add_f")),
		"mul_int_col":               sel(a.Mul(i)),
		"mul_int_lit":               sel(a.Mul(2)),
		"mul_int_gen":               sel(a.Mul(i).Alias("mul_i"), b.Mul(3).Alias("mul_3")),
		"gt":                        sel(a.Gt(b)),
		"eq_1_00_1_000":             sel(a.Eq(b)),
		"eq_float_lit":              sel(a.Eq(1.25)),
		"concat_relaxed": func(in oracleInputs) *ursus.LazyFrame {
			return ursus.Concat([]*ursus.LazyFrame{in.frame("left"), in.frame("right")})
		},
		"when_two_types": sel(ursus.When(i.Eq(2)).Then(a).Otherwise(b).Alias("x")),
		"compare_gen":    sel(a.Gt(b).Alias("gt"), a.Eq(b).Alias("eq"), a.Le(b).Alias("le"), a.Eq(0).Alias("zero")),
		"when_gen":       sel(ursus.When(i.Gt(0)).Then(a).Otherwise(b).Alias("x")),
	}
}
