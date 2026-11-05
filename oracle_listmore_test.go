package ursus_test

// The listmore group (step 499, from listmore_test.go's hand table): list.concat,
// shift, diff, count_matches, any, all, item, a sort with nulls last and a sample's
// lengths, each spelled as bench/scripts/oracle/listmore.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	L, B := c("l").List(), c("b").List()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	one := func(e ursus.Expr) oracleQuery { return sel(e.Alias("r")) }
	oracleGroups["listmore"] = map[string]oracleQuery{
		"concat": one(L.Concat(c("o"))), "concat_value": one(L.Concat(c("v"))),
		"shift": one(L.Shift(1)), "shift_back": one(L.Shift(-1)), "shift_by_column": one(L.Shift(c("n"))),
		"diff": one(L.Diff(1, false)), "diff_by_two": one(L.Diff(2, false)), "diff_drop_nulls": one(L.Diff(1, true)),
		"sort_nulls_last": one(L.SortWith(false, true)), "desc_nulls_last": one(L.SortWith(true, true)),
		"count_matches_1": one(L.CountMatches(int64(1))), "count_matches_null": one(L.CountMatches(nil)),
		"any": one(B.Any(true)), "all": one(B.All(true)), "any_kleene": one(B.Any(false)), "all_kleene": one(B.All(false)),
		"item_empty_allowed": one(c("o").List().Item(true)),
		"sample_lengths": sel(L.SampleN(4, true, false, 1).List().Len().Alias("n"),
			L.SampleFraction(0.5, false, false, 1).List().Len().Alias("f")),

		"methods_gen": sel(L.Concat(c("o")).Alias("cat"), L.Concat(c("v")).Alias("catv"), L.Shift(1).Alias("sh"),
			L.Shift(c("n")).Alias("shn"), L.Diff(1, false).Alias("d1"), L.Diff(2, true).Alias("d2"),
			L.SortWith(false, true).Alias("snl"), L.SortWith(true, true).Alias("sdnl"),
			L.CountMatches(int64(3)).Alias("cm3"), L.CountMatches(nil).Alias("cmn"),
			L.SampleN(3, true, false, 7).List().Len().Alias("sn")),
		"bools_gen": sel(B.Any(true).Alias("any"), B.All(true).Alias("all"), B.Any(false).Alias("any_k"), B.All(false).Alias("all_k")),
		"item_gen":  sel(c("o").List().Item(true)),
	}
}
