package ursus_test

// The bits group (step 497, from bits_test.go's pasted answers): M2's And, Or, Xor
// and Not of integers as their bits, of Booleans as Kleene's, each spelled as
// bench/scripts/oracle/bits.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	a, b, u, i8, p, q := c("a"), c("b"), c("u"), c("i8"), c("p"), c("q")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["bits"] = map[string]oracleQuery{
		"and":          sel(a.And(b).Alias("r")),
		"or":           sel(a.Or(b).Alias("r")),
		"xor":          sel(a.Xor(b).Alias("r")),
		"not":          sel(a.Not().Alias("r")),
		"not_u8":       sel(u.Not().Alias("r")),
		"and_const":    sel(a.And(6).Alias("r")),
		"u8_and_i8":    sel(u.And(i8).Alias("r")),
		"u8_or_i64":    sel(u.Or(a).Alias("r")),
		"not_not":      sel(a.Not().Not().Alias("r")),
		"u8_and_const": sel(u.And(6).Alias("r")),
		"i128":         sel(c("x").And(ursus.Lit(int64(3)).Cast(ursus.Int128)), c("x").Not().Alias("n")),
		"null_and":     sel(ursus.Null(ursus.NullT).And(a).Alias("r")),
		"bool_kleene":  sel(p.And(false).Alias("and"), p.Or(true).Alias("or"), p.Not().Alias("not")),
		"bits_gen": sel(a.And(b).Alias("and"), a.Or(b).Alias("or"), a.Xor(b).Alias("xor"), a.Not().Alias("not"), u.Not().Alias("not_u"),
			u.And(i8).Alias("u_and_i8"), u.Or(a).Alias("u_or_a"), a.And(6).Alias("a_and_6"), u.And(6).Alias("u_and_6"),
			c("i32").Xor(c("u16")).Alias("i32_xor_u16"), c("u16").Not().Alias("not_u16")),
		"bits_i128_gen":   sel(c("x").And(c("y")).Alias("and"), c("x").Or(c("y")).Alias("or"), c("x").Xor(c("y")).Alias("xor"), c("x").Not().Alias("not")),
		"bool_kleene_gen": sel(p.And(q).Alias("and"), p.Or(q).Alias("or"), p.Not().Alias("not"), p.And(false).Alias("and_f"), p.Or(true).Alias("or_t")),
	}
}
