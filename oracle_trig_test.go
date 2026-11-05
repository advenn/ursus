package ursus_test

// The trig group (step 497, beside trig_test.go's Go oracle): the trigonometric and
// hyperbolic functions, degrees, radians and cot, each spelled as
// bench/scripts/oracle/trig.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	x := ursus.Col("x")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	libm := sel(x.Sin().Alias("sin"), x.Cos().Alias("cos"), x.Tan().Alias("tan"), x.ArcSin().Alias("arcsin"), x.ArcCos().Alias("arccos"),
		x.ArcTan().Alias("arctan"), x.Sinh().Alias("sinh"), x.Cosh().Alias("cosh"), x.Tanh().Alias("tanh"), x.ArcSinh().Alias("arcsinh"),
		x.ArcCosh().Alias("arccosh"), x.ArcTanh().Alias("arctanh"), x.Cot().Alias("cot"))
	angles := sel(x.Degrees().Alias("degrees"), x.Radians().Alias("radians"))
	oracleGroups["trig"] = map[string]oracleQuery{
		"trig":                 libm,
		"angles":               angles,
		"int_widens_f32_stays": sel(ursus.Col("i").Sin().Alias("i"), ursus.Col("f").Cos().Alias("f")),
		"trig_gen":             libm,
		"angles_gen":           angles,
		"trig_f32_gen":         libm,
		"angles_f32_gen":       angles,
	}
}
