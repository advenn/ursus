package ursus_test

// The listset group (step 499, from listset_test.go's hand tables): the four list set
// operations and list.gather, each spelled as bench/scripts/oracle/listset.py asks
// Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	A := c("a").List()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	ops := sel(A.SetUnion(c("b")).Alias("union"), A.SetIntersection(c("b")).Alias("inter"),
		A.SetDifference(c("b")).Alias("diff"), A.SetSymmetricDifference(c("b")).Alias("sym"))
	// Polars 2.1 orders set_difference's elements by its hash table, so the generated
	// cases sort that one column on both sides.
	sortedDiff := sel(A.SetUnion(c("b")).Alias("union"), A.SetIntersection(c("b")).Alias("inter"),
		A.SetDifference(c("b")).List().Sort().Alias("diff"), A.SetSymmetricDifference(c("b")).Alias("sym"))
	gather := sel(A.Gather(c("i"), true).Alias("g"))
	oracleGroups["listset"] = map[string]oracleQuery{
		"set_operations":     ops,
		"set_floats":         sel(A.SetIntersection(c("b")).Alias("i"), A.SetUnion(c("b")).Alias("u")),
		"set_union_i32_f64":  sel(A.SetUnion(c("b")).Alias("u")),
		"gather":             gather,
		"set_operations_gen": sortedDiff,
		"set_floats_gen":     sortedDiff,
		"gather_gen":         gather,
	}
}
