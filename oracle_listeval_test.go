package ursus_test

// The listeval group (step 499, from listeval_test.go's hand table): List().Eval, Agg
// and Filter, the body over each list's elements, each spelled as
// bench/scripts/oracle/listeval.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c, el := ursus.Col, ursus.Element
	L := c("l").List()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["listeval"] = map[string]oracleQuery{
		"doubled":         sel(L.Eval(el().Mul(2))),
		"sum":             sel(L.Eval(el().Sum())),
		"mean":            sel(L.Eval(el().Mean())),
		"centred":         sel(L.Eval(el().Sub(el().Mean()))),
		"filtered":        sel(L.Eval(el().Filter(el().Gt(1)))),
		"sorted":          sel(L.Eval(el().Sort())),
		"sorted_doubled":  sel(L.Eval(el().Sort().Mul(2))),
		"head":            sel(L.Eval(el().Head(2))),
		"filtered_sum":    sel(L.Eval(el().Filter(el().Gt(1)).Sum())),
		"ranked":          sel(L.Eval(el().DropNulls().Rank(ursus.RankAverage, false))),
		"cum_sum":         sel(L.Eval(el().CumSum(false))),
		"shifted":         sel(L.Eval(el().Shift(1))),
		"cast":            sel(L.Eval(el().Cast(ursus.String))),
		"is_null":         sel(L.Eval(el().IsNull())),
		"constant":        sel(L.Eval(ursus.Lit(1))),
		"count":           sel(L.Eval(el().Count())),
		"len":             sel(L.Eval(el().Len())),
		"first":           sel(L.Eval(el().First())),
		"agg_values":      sel(L.Agg(el().Mul(2))),
		"agg_filter":      sel(L.Agg(el().Filter(el().Gt(1)))),
		"filter":          sel(L.Filter(el().Gt(1))),
		"filter_not_null": sel(L.Filter(el().IsNotNull())),
		"agg_aggregates":  sel(L.Agg(el().Sum()).Alias("s"), L.Agg(el().Mean()).Alias("m")),
		"strings": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(c("s").Str().Split(",")).Select(c("s").List().Eval(el().Str().LenChars()))
		},
		"eval_gen": sel(L.Eval(el().Mul(2)).Alias("x2"), L.Eval(el().Sort()).Alias("sorted"), L.Eval(el().CumSum(false)).Alias("cum"),
			L.Eval(el().Shift(1)).Alias("sh"), L.Eval(el().IsNull()).Alias("isn"), L.Eval(el().Cast(ursus.String)).Alias("txt"),
			L.Eval(el().Head(2)).Alias("h2"), L.Eval(el().Filter(el().Gt(0))).Alias("pos"),
			L.Eval(el().DropNulls().Rank(ursus.RankMin, false)).Alias("rk"), L.Filter(el().Lt(0)).Alias("neg"),
			L.Eval(el().First()).Alias("first")),
		"agg_gen": sel(L.Agg(el().Max()).Alias("max"), L.Agg(el().Min()).Alias("min"), L.Agg(el().Mean()).Alias("mean"),
			L.Agg(el().First()).Alias("first"), L.Agg(el().Last()).Alias("last"), L.Agg(el().Filter(el().Gt(0)).Max()).Alias("posmax")),
		"eval_floats_gen": sel(L.Eval(el().Sub(el().Mean())).Alias("centred"), L.Eval(el().CumSum(false)).Alias("cum"),
			L.Agg(el().Mean()).Alias("mean")),
		"eval_words_gen": sel(L.Eval(el().Str().LenChars()).Alias("n"), L.Eval(el().Str().ToUpper()).Alias("up"),
			L.Filter(el().Str().StartsWith("a")).Alias("as"), L.Eval(el().Sort()).Alias("sorted")),
	}
	// An Int64 sums to an Int128 here, and an empty list's sum is null, as a group of
	// no value's is, where Polars' is 0 (feature-gaps.md §9). The empty list is row 2.
	sum := oracleDifference{
		why:    "an empty list's sum is null here, 0 in Polars; an integer sum is an Int128 (feature-gaps.md §9)",
		cells:  map[string]map[int]any{"l": {2: []any{nil}}},
		dtypes: map[string]string{"l": "List(Int128)"},
	}
	oracleDifferences["listeval/sum"] = sum
	oracleDifferences["listeval/filtered_sum"] = sum
	oracleDifferences["listeval/agg_aggregates"] = oracleDifference{
		why:    sum.why,
		cells:  map[string]map[int]any{"s": {2: nil}},
		dtypes: map[string]string{"s": "Int128"},
	}
	// CumSum keeps Sum's accumulator width (window.go), so an Int64's running sum is
	// an Int128 too, where Polars' is an Int64.
	oracleDifferences["listeval/cum_sum"] = oracleDifference{
		why: "a running sum has Sum's width, an Int128 for an Int64 (feature-gaps.md §9, window.go's CumSum)", dtypes: map[string]string{"l": "List(Int128)"}}
	oracleDifferences["listeval/eval_gen"] = oracleDifference{
		why: "a running sum has Sum's width, an Int128 for an Int64 (feature-gaps.md §9, window.go's CumSum)", dtypes: map[string]string{"cum": "List(Int128)"}}
	// A literal has its Go value's type, Int64, where Polars' 1 is an Int32
	// (feature-gaps.md §9). A count inside a list's body is a Uint32, as Polars'.
	oracleDifferences["listeval/constant"] = oracleDifference{
		why: "a literal has its Go value's type here, Int64; Polars' is an Int32 (feature-gaps.md §9)", dtypes: map[string]string{"l": "List(Int64)"}}
}
