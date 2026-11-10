package ursus_test

// The withfields group (step 499, from withfields_test.go's hand values):
// struct.with_fields, each spelled as bench/scripts/oracle/withfields.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	p := c("p").Struct()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["withfields"] = map[string]oracleQuery{
		"with_fields":              sel(p.WithFields(p.Field("age").Add(int64(1)).Alias("age"), c("city"), ursus.Lit("fixed").Alias("tag"))),
		"names_a_field_as_planned": sel(p.WithFields(p.Field("name")).Alias("q")),
		"with_fields_gen": sel(p.WithFields(p.Field("age").Mul(int64(2)).Alias("age"), c("city"),
			p.Field("name").Str().ToUpper().Alias("shout"))),
	}
}
