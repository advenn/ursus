package ursus_test

// The liststruct group (step 499, from liststruct_test.go's hand tables):
// list.to_struct with its fields named and struct.rename_fields, each spelled as
// bench/scripts/oracle/liststruct.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["liststruct"] = map[string]oracleQuery{
		"to_struct":         sel(c("a").List().ToStruct("x", "y")),
		"rename_fields":     sel(c("s").Struct().RenameFields("p", "q")),
		"rename_then_field": sel(c("s").Struct().RenameFields("p", "q").Struct().Field("q")),
		"to_struct_gen":     sel(c("a").List().ToStruct("x", "y", "z")),
		"to_struct_one_gen": sel(c("a").List().ToStruct("only")),
		"rename_fields_gen": sel(c("s").Struct().RenameFields("p", "q")),
	}
}
