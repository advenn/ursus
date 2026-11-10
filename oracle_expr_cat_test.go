package ursus_test

// The expr_cat group (step 499, from expr_cat_test.go's hand table): the categorical
// namespace on an Enum, each spelled as bench/scripts/oracle/expr_cat.py asks Polars.

import (
	"strconv"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	s := c("s").Cat()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	one := func(e ursus.Expr) oracleQuery { return sel(e.Alias("r")) }
	physical := func(k int) oracleQuery {
		cats := make([]string, k)
		for i := range cats {
			cats[i] = "c" + strconv.Itoa(i)
		}
		return sel(c("s").Cast(ursus.Enum(cats...)).Cat().Physical())
	}
	oracleGroups["expr_cat"] = map[string]oracleQuery{
		"starts_with": one(s.StartsWith("ba")), "ends_with": one(s.EndsWith("ir")), "empty_prefix": one(s.StartsWith("")),
		"len_bytes": one(s.LenBytes()), "len_chars": one(s.LenChars()),
		"slice": one(s.Slice(1, 3)), "slice_from_end": one(s.Slice(-2)), "slice_to_end": one(s.Slice(2)),
		"physical":        one(s.Physical()),
		"physical_of_255": physical(255), "physical_of_256": physical(256),
		"filter_physical_1": func(in oracleInputs) *ursus.LazyFrame { return in.df().Head(4).Filter(s.Physical().Eq(1)) },
		"cat_gen": sel(s.StartsWith("a").Alias("sw"), s.EndsWith("b").Alias("ew"), s.LenBytes().Alias("lb"), s.LenChars().Alias("lc"),
			s.Slice(1, 1).Alias("sl"), s.Slice(-1).Alias("last"), s.Physical().Alias("ph")),
	}
}
