package ursus_test

// The arrns group (step 499, from arrns_test.go's hand table): Polars' arr namespace,
// each spelled as bench/scripts/oracle/arrns.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c, el := ursus.Col, ursus.Element
	a, t, b := c("a").Arr(), c("t").Arr(), c("b").Arr()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	one := func(e ursus.Expr) oracleQuery { return sel(e.Alias("r")) }
	oracleGroups["arrns"] = map[string]oracleQuery{
		"max": one(a.Max()), "min": one(a.Min()), "sum": one(a.Sum()), "mean": one(a.Mean()), "median": one(a.Median()),
		"n_unique": one(a.NUnique()), "arg_min": one(a.ArgMin()), "arg_max": one(a.ArgMax()),
		"first": one(a.First()), "last": one(a.Last()), "get_1": one(a.Get(1)), "get_i": one(a.Get(c("i"))),
		"contains_2": one(a.Contains(int64(2))), "count_matches_2": one(a.CountMatches(int64(2))), "len": one(a.Len()),
		"head_2": one(a.Head(2)), "tail_2": one(a.Tail(2)), "slice_1_2": one(a.Slice(1, 2)),
		"reverse": one(a.Reverse()), "shift_1": one(a.Shift(1)), "shift_m1": one(a.Shift(-1)),
		"sort": one(a.Sort()), "sort_desc_nulls_last": one(a.SortWith(true, true)), "unique": one(a.Unique()),
		"eval": one(a.Eval(el().Mul(int64(10)), false)), "eval_as_list": one(a.Eval(el().Mul(int64(10)), true)),
		"agg_max": one(a.Agg(el().Max())), "dot": one(a.Dot(c("a"))),
		"dot_null_row": one(a.Dot(ursus.When(c("i").Eq(int64(0))).Then(ursus.Null(ursus.Array(ursus.Int64, 3))).Otherwise(a.Reverse()))),
		"to_list":      one(a.ToList()), "join": one(t.Join("-")), "any": one(b.Any(true)), "all": one(b.All(true)),
		"std": one(a.Std(1)), "var_ddof0": one(a.Var(0)),
		"sum_floats_all_null": one(a.Sum()),
		"explode":             sel(a.Explode()), "to_struct": one(a.ToStruct()),
		"to_struct_named_field": one(a.ToStruct("x", "y", "z").Struct().Field("y")),

		"aggs_gen": sel(a.Max().Alias("max"), a.Min().Alias("min"), a.Mean().Alias("mean"), a.Median().Alias("median"),
			a.ArgMin().Alias("amin"), a.ArgMax().Alias("amax"), a.First().Alias("first"), a.Last().Alias("last"),
			a.Get(2).Alias("g2"), a.Get(c("i")).Alias("gi"), a.Contains(int64(3)).Alias("c3"), a.CountMatches(int64(3)).Alias("cm3"),
			a.Len().Alias("len")),
		"sum_n_unique_gen": sel(a.Sum().Cast(ursus.Int64).Alias("sum"), a.NUnique().Alias("nu"), a.Dot(c("a")).Cast(ursus.Int64).Alias("dot")),
		"reshape_gen": sel(a.Head(2).Alias("h"), a.Tail(3).Alias("t"), a.Slice(1, 2).Alias("s"), a.Reverse().Alias("r"),
			a.Shift(1).Alias("sh"), a.Shift(c("i")).Alias("shi"), a.Sort().Alias("so"), a.SortWith(true, true).Alias("sd"),
			a.Unique().Alias("u"), a.ToList().Alias("tl"), a.Eval(el().Mul(int64(10)), false).Alias("e"), a.Agg(el().Min()).Alias("ag")),
		"floats_gen": sel(a.Std(1).Alias("std"), a.Var(0).Alias("var0"), a.Mean().Alias("mean"), a.Sum().Alias("sum"), a.Max().Alias("max")),
		"text_gen":   sel(t.Join("-").Alias("j"), t.First().Alias("f"), t.Sort().Alias("s"), t.Contains("a").Alias("has_a")),
		"bools_gen":  sel(b.Any(true).Alias("any"), b.All(true).Alias("all"), b.Any(false).Alias("any_k"), b.All(false).Alias("all_k")),
	}
	// Arr()'s functions answer as List()'s (feature-gaps.md §9): an integer Sum, and
	// a Dot, which is a Mul's Sum, is an Int128 where Polars' is an Int64; NUnique
	// passes a null over where Polars counts it; a count or a position is a Uint64
	// where Polars' is a UInt32.
	oracleDifferences["arrns/sum"] = oracleDifference{
		why: "an integer Sum is an Int128 here, Polars' arr.sum an Int64 (feature-gaps.md §9)", dtypes: map[string]string{"r": "Int128"}}
	oracleDifferences["arrns/dot"] = oracleDifference{
		why: "a Dot is a Mul's Sum, an Int128 here (feature-gaps.md §9)", dtypes: map[string]string{"r": "Int128"}}
	oracleDifferences["arrns/dot_null_row"] = oracleDifference{
		why: "a Dot is a Mul's Sum, an Int128 here (feature-gaps.md §9)", dtypes: map[string]string{"r": "Int128"}}
	oracleDifferences["arrns/sum_floats_all_null"] = oracleDifference{
		why:   "the sum of nulls alone is null here, as a group of no value's is, where Polars' is 0.0 (feature-gaps.md §9)",
		cells: map[string]map[int]any{"r": {1: nil}},
	}
	oracleDifferences["arrns/n_unique"] = oracleDifference{
		why:    "n_unique passes a null over here, where Polars counts it as a value; a count is a Uint64 (feature-gaps.md §9)",
		cells:  map[string]map[int]any{"r": {1: 2}},
		dtypes: map[string]string{"r": "Uint64"},
	}
	for _, name := range []string{"arg_min", "arg_max"} {
		oracleDifferences["arrns/"+name] = oracleDifference{
			why: "a position is a Uint64 here, as a count is, where Polars' is a UInt32 (feature-gaps.md §9)", dtypes: map[string]string{"r": "Uint64"}}
	}
	oracleDifferences["arrns/aggs_gen"] = oracleDifference{
		why:    "a position is a Uint64 here, as a count is, where Polars' is a UInt32 (feature-gaps.md §9)",
		dtypes: map[string]string{"amin": "Uint64", "amax": "Uint64"}}
	oracleDifferences["arrns/sum_n_unique_gen"] = oracleDifference{
		why: "a count is a Uint64 here, Polars' a UInt32 (feature-gaps.md §9)", dtypes: map[string]string{"nu": "Uint64"}}
}
