package ursus_test

// The castbool group (step 503, from internal/kernel/castbool_test.go's values): a
// Boolean cast to each number type and each number type cast to a Boolean, each
// spelled as bench/scripts/oracle/castbool.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	b, v := c("b"), c("v")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	numbers := map[string]ursus.DataType{
		"int8": ursus.Int8, "int16": ursus.Int16, "int32": ursus.Int32, "int64": ursus.Int64, "int128": ursus.Int128,
		"uint8": ursus.Uint8, "uint16": ursus.Uint16, "uint32": ursus.Uint32, "uint64": ursus.Uint64,
		"float32": ursus.Float32, "float64": ursus.Float64,
	}
	order := []string{"int8", "int16", "int32", "int64", "int128", "uint8", "uint16", "uint32", "uint64", "float32", "float64"}
	reg := map[string]oracleQuery{
		"float_specials_to_bool": sel(c("f64").Cast(ursus.Bool), c("f32").Cast(ursus.Bool)),
		"bool_to_decimal":        sel(b.Cast(ursus.Decimal(10, 2))),
		// CanCast refuses a Decimal to a Boolean and the kernel with it
		// (castbool_test.go's TestDecimalCastDivergenceIsClosed: a truth value is not
		// an amount); Polars 2.1 casts one, true where it is not zero.
		"decimal_to_bool":     oracleFinding("a Decimal cast to a Boolean is refused here; Polars 2.1 answers true where it is not zero"),
		"gen_numbers_to_bool": sel(ursus.All().Cast(ursus.Bool)),
	}
	var toNumbers []ursus.Expr
	for _, name := range order {
		dt := numbers[name]
		reg["bool_to_"+name] = sel(b.Cast(dt))
		reg[name+"_to_bool"] = sel(v.Cast(ursus.Bool))
		toNumbers = append(toNumbers, b.Cast(dt).Alias(name))
	}
	reg["gen_bool_to_numbers"] = sel(toNumbers...)
	oracleGroups["castbool"] = reg
}
