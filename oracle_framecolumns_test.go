package ursus_test

// The framecolumns group (step 501, from framecolumns_test.go's hand answers):
// insert_column of values or an expression, each spelled as
// bench/scripts/oracle/framecolumns.py asks Polars.

import "github.com/advenn/ursus"

// oracleColumnOf is the one column of the case's input named name, for a question
// that takes a column and not a frame (InsertColumn).
func oracleColumnOf(in oracleInputs, name string) *ursus.Column {
	return collect(in.t, in.frame(name)).ColumnAt(0)
}

func init() {
	c := ursus.Col
	ins := func(at int, e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().InsertColumn(at, e) }
	}
	oracleGroups["framecolumns"] = map[string]oracleQuery{
		"values_at_1":        func(in oracleInputs) *ursus.LazyFrame { return in.df().InsertColumn(1, oracleColumnOf(in, "n")) },
		"an_expression_at_0": ins(0, c("a").Mul(10).Alias("a10")),
		"at_the_width":       ins(3, c("a").Alias("z")),
		"at_minus_1":         ins(-1, c("a").Alias("m1")),
		"at_minus_3":         ins(-3, c("a").Alias("m3")),
		"an_aggregate":       ins(1, c("a").Sum().Cast(ursus.Int64).Alias("s")),
		"a_literal":          ins(1, ursus.Lit(int64(5)).Alias("five")),
		"insert_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().InsertColumn(1, oracleColumnOf(in, "n")).InsertColumn(-1, c("a").Mul(2).Alias("a2"))
		},
	}
}
