package ursus_test

// The listns group (step 499, from listns_test.go's hand tables): the `.list`
// namespace's reducing and reshaping halves over a null list, an empty list and a
// list of a null, each spelled as bench/scripts/oracle/listns.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	L := c("tags").List()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	reduce := []ursus.Expr{L.Len().Alias("n"), L.Min().Alias("min"), L.Max().Alias("max"),
		L.Get(0).Alias("first"), L.Get(-1).Alias("last")}
	reshapes := []ursus.Expr{L.Reverse().Alias("rev"), L.Head(2).Alias("head"), L.Tail(2).Alias("tail"),
		L.Slice(1, 2).Alias("slice"), L.Sort().Alias("sort"), L.SortDesc().Alias("desc"), L.Unique().Alias("uniq"),
		L.DropNulls().Alias("drop")}
	oracleGroups["listns"] = map[string]oracleQuery{
		"semantics": sel(L.Len().Alias("n"), L.Sum().Alias("sum"), L.Min().Alias("min"), L.Max().Alias("max"),
			L.Get(0).Alias("first"), L.Contains(int64(20)).Alias("has20")),
		"get_indexing": sel(L.Get(1).Alias("at1"), L.Get(-1).Alias("last"), L.Get(-3).Alias("backthree"), L.Get(9).Alias("past")),
		"strings":      sel(L.Len().Alias("n"), L.Min().Alias("min"), L.Contains("rust").Alias("hasrust"), L.Get(0).Alias("first")),
		"filter_len_gt_1": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Filter(L.Len().Gt(uint32(1)))
		},
		"semantics_gen":    sel(append(reduce, L.Contains(int64(7)).Alias("has7"), L.Contains(ursus.Null(ursus.Int64)).Alias("hasnull"))...),
		"max_nan":          sel(L.Min().Alias("min"), L.Max().Alias("max")),
		"sum_gen":          sel(L.Sum().Cast(ursus.Int64).Alias("sum")),
		"float_reduce_gen": sel(L.Min().Alias("min"), L.Max().Alias("max"), L.Get(0).Alias("first"), L.Contains(0.5).Alias("has_half")),
		"strings_gen": sel(L.Len().Alias("n"), L.Min().Alias("min"), L.Max().Alias("max"), L.Contains("a").Alias("has_a"),
			L.Get(0).Alias("first")),

		"reshape": sel(reshapes...),
		"reshape_edges": sel(L.Head(0).Alias("h0"), L.Head(99).Alias("h99"), L.Tail(0).Alias("t0"), L.Tail(99).Alias("t99"),
			L.Slice(-2, 1).Alias("back"), L.Slice(1, 99).Alias("rest"), L.Slice(9, 2).Alias("past"), L.Slice(-99, 2).Alias("before"),
			L.Slice(-4, 2).Alias("cut")),
		"sort_strings": sel(L.Sort().Alias("s"), L.SortDesc().Alias("d"), L.Reverse().Alias("r")),
		"chains": sel(L.Sort().List().Head(2).List().Len().Alias("n"), L.Sort().List().Head(2).List().Get(0).Alias("smallest"),
			L.DropNulls().List().Len().Alias("present")),
		"reshape_gen":         sel(append(reshapes, L.Slice(-2, 1).Alias("back"), L.Slice(-9, 3).Alias("before"))...),
		"reshape_strings_gen": sel(reshapes...),
		"reshape_floats_gen":  sel(reshapes...),

		"mean_duration":     sel(L.Mean()),
		"mean_f32":          sel(L.Mean()),
		"mean_gen":          sel(L.Mean()),
		"mean_f32_gen":      sel(L.Mean()),
		"mean_duration_gen": sel(L.Mean()),
	}
	// The sum of an empty list is null here, as the sum of a group of no value is,
	// where Polars' is 0; and an integer Sum is an Int128 (feature-gaps.md §9).
	// Max propagates NaN here, where Polars' max passes it over (feature-gaps.md §9).
	oracleDifferences["listns/max_nan"] = oracleDifference{
		why:   "Max propagates NaN here, where Polars' max passes it over (feature-gaps.md §9)",
		cells: map[string]map[int]any{"max": {0: "NaN"}},
	}
	oracleDifferences["listns/semantics"] = oracleDifference{
		why:    "an empty list's sum is null here, 0 in Polars; an integer sum is an Int128 (feature-gaps.md §9)",
		cells:  map[string]map[int]any{"sum": {1: nil}},
		dtypes: map[string]string{"sum": "Int128"},
	}
}
