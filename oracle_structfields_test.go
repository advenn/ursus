package ursus_test

// The structfields group (step 499, from structfields_test.go's hand table):
// struct.field of several names, a regex or "*", each spelled as
// bench/scripts/oracle/structfields.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	s := c("s").Struct()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	sum := func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }
	oracleGroups["structfields"] = map[string]oracleQuery{
		"two":                 sel(s.Field("b", "a")),
		"one":                 sel(s.Field("b")),
		"regex":               sel(s.Field("^a.*$")),
		"regex_inside":        sel(s.Field("^.b$")),
		"every_field":         sel(s.Field("*")),
		"name_and_regex":      sel(s.Field("c", "^a.*$")),
		"name_twice":          sel(s.Field("a", "a")),
		"regex_and_its_match": sel(s.Field("^a.*$", "a")),
		"regex_of_none":       sel(c("k"), s.Field("^z.*$")),
		"aggregate_each":      sel(s.Field("a", "ab").Sum()),
		"arithmetic_each":     sel(s.Field("a", "ab").Mul(2)),
		"suffix_each":         sel(s.Field("a", "ab").Suffix("_x")),
		"unnest_suffix":       sel(s.Unnest().Suffix("_x")),
		"one_fields_name":     sel(s.Field("c")),
		"with_columns": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(s.Field("a", "b")).Select(c("k"), c("a"), c("b"))
		},
		"groups_first": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("k")).Agg(s.Field("a", "ab").First()).Sort(ursus.Asc(c("k")))
		},
		"fold_fields": sel(ursus.CumFold(0, sum, c("a"), c("b")).Struct().Field("a", "b")),
		"fields_gen":  sel(s.Field("*")),
		"regex_gen":   sel(c("k"), s.Field("^a.*$"), s.Field("c").Mul(2).Alias("c2")),
		"groups_first_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("k")).Agg(s.Field("a", "ab").First(), s.Field("b").Last().Alias("b")).Sort(ursus.Asc(c("k")))
		},
	}
	// An integer Sum is an Int128 here, Polars' an Int64; a literal 0 has its Go
	// value's type, Int64, where Polars' is an Int32 (feature-gaps.md §9).
	oracleDifferences["structfields/aggregate_each"] = oracleDifference{
		why: "an integer Sum is an Int128 here, Polars' an Int64 (feature-gaps.md §9)", dtypes: map[string]string{"a": "Int128", "ab": "Int128"}}
	oracleDifferences["structfields/fold_fields"] = oracleDifference{
		why: "a literal 0 has its Go value's type here, Int64; Polars' is an Int32 (feature-gaps.md §9)", dtypes: map[string]string{"a": "Int64", "b": "Int64"}}
}
