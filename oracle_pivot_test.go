package ursus_test

// The pivot group (step 501, from pivot_test.go's hand answers): DataFrame.Pivot and
// its options against Polars 2.1's pivot, and Transpose, each spelled as
// bench/scripts/oracle/pivot.py asks Polars. A count is a Uint64 here and a sum an
// Int128, and a sum of none is null here where Polars' is 0 (feature-gaps.md §9).

import (
	"math"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	nan := math.NaN()
	opts := func(o ursus.PivotOptions) ursus.PivotOptions { return o }
	kv := func(o ursus.PivotOptions) ursus.PivotOptions {
		o.On, o.Index, o.Values = []string{"on"}, []string{"k"}, []string{"v"}
		return o
	}
	// eager pivots the collected input, as Polars' DataFrame.pivot does; lazy is
	// LazyFrame.Pivot, whose columns OnColumns gives.
	eager := func(o ursus.PivotOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.eager(collect(in.t, in.df()).Pivot(in.t.Context(), o))
		}
	}
	lazy := func(o ursus.PivotOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Pivot(o) }
	}
	agg := func(f func(ursus.Expr) ursus.Expr) oracleQuery {
		return eager(kv(ursus.PivotOptions{Agg: f}))
	}
	transpose := func(o ursus.TransposeOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.eager(collect(in.t, in.df()).Transpose(in.t.Context(), o))
		}
	}
	oracleGroups["pivot"] = map[string]oracleQuery{
		"one_value":       eager(kv(ursus.PivotOptions{})),
		"two_values":      eager(opts(ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v", "w"}})),
		"sort_columns":    eager(kv(ursus.PivotOptions{SortColumns: true})),
		"values_left_out": eager(opts(ursus.PivotOptions{On: []string{"on"}, Index: []string{"k", "v"}})),
		"agg_sum": func(in oracleInputs) *ursus.LazyFrame {
			return agg(ursus.Expr.Sum)(in).Select(c("k"), c("x").Cast(ursus.Int64))
		},
		"null_on":               eager(kv(ursus.PivotOptions{})),
		"several_on":            eager(opts(ursus.PivotOptions{On: []string{"on", "p"}, Index: []string{"k"}, Values: []string{"v"}})),
		"several_on_two_values": eager(opts(ursus.PivotOptions{On: []string{"on", "p"}, Index: []string{"k"}, Values: []string{"v", "w"}})),
		"several_on_sorted":     eager(opts(ursus.PivotOptions{On: []string{"on", "p"}, Index: []string{"k"}, Values: []string{"v"}, SortColumns: true})),
		"several_on_given": lazy(opts(ursus.PivotOptions{On: []string{"on", "p"}, Index: []string{"k"}, Values: []string{"v"},
			OnColumns: []*ursus.Column{strs("p", "p", "q"), strs("on", "x", "y")}})),
		"given_subset":                     eager(kv(ursus.PivotOptions{OnColumns: []*ursus.Column{strs("on", "x")}})),
		"given_reordered_never_held":       eager(kv(ursus.PivotOptions{OnColumns: []*ursus.Column{strs("on", "z", "y", "x")}, SortColumns: true})),
		"given_x_y_lazy":                   lazy(kv(ursus.PivotOptions{OnColumns: []*ursus.Column{strs("on", "x", "y")}})),
		"given_index_none_of_whose_values": lazy(kv(ursus.PivotOptions{OnColumns: []*ursus.Column{strs("on", "x")}})),
		"given_null":                       lazy(kv(ursus.PivotOptions{OnColumns: []*ursus.Column{strs("on", "x", nil)}})),
		"given_none":                       lazy(kv(ursus.PivotOptions{OnColumns: []*ursus.Column{strs("on")}})),
		"floats_nan_and_zeros": lazy(opts(ursus.PivotOptions{On: []string{"f"}, Index: []string{"k"}, Values: []string{"v"}, Agg: ursus.Expr.First,
			OnColumns: []*ursus.Column{f64s("f", nan, 0.0, 1.5)}})),
		"floats_both_zeros": lazy(opts(ursus.PivotOptions{On: []string{"f"}, Index: []string{"k"}, Values: []string{"v"},
			Agg:       func(e ursus.Expr) ursus.Expr { return e.Sum().Cast(ursus.Int64) },
			OnColumns: []*ursus.Column{f64s("f", 0.0, math.Copysign(0, -1))}})),
		"int64_given_for_int32": lazy(opts(ursus.PivotOptions{On: []string{"n"}, Index: []string{"k"}, Values: []string{"w"},
			OnColumns: []*ursus.Column{i64s("n", 2, 5)}})),
		"none_len":        agg(ursus.Expr.Len),
		"none_n_unique":   agg(ursus.Expr.NUnique),
		"none_sum":        agg(ursus.Expr.Sum),
		"none_first":      agg(ursus.Expr.First),
		"none_last":       agg(ursus.Expr.Last),
		"none_min":        agg(ursus.Expr.Min),
		"none_max":        agg(ursus.Expr.Max),
		"none_mean":       agg(ursus.Expr.Mean),
		"none_median":     agg(ursus.Expr.Median),
		"none_sum_fill_0": agg(func(e ursus.Expr) ursus.Expr { return e.Sum().FillNull(0) }),
		"none_no_agg":     eager(kv(ursus.PivotOptions{})),
		"pivot_gen_max":   eager(kv(ursus.PivotOptions{Agg: ursus.Expr.Max, SortColumns: true})),
		"pivot_gen_mean":  eager(kv(ursus.PivotOptions{Agg: ursus.Expr.Mean, SortColumns: true})),
		"pivot_gen_given": lazy(kv(ursus.PivotOptions{Agg: ursus.Expr.Min, OnColumns: []*ursus.Column{strs("on", "d", "b", "z")}})),

		"transpose_header":     transpose(ursus.TransposeOptions{IncludeHeader: true}),
		"transpose_named":      transpose(ursus.TransposeOptions{ColumnNames: []string{"p", "q"}}),
		"transpose_mixed":      transpose(ursus.TransposeOptions{}),
		"transpose_bool_int":   transpose(ursus.TransposeOptions{}),
		"transpose_names_from": transpose(ursus.TransposeOptions{IncludeHeader: true, ColumnNamesFrom: "id"}),
		"transpose_gen":        transpose(ursus.TransposeOptions{IncludeHeader: true}),
	}
	// A count is a Uint64 here, Polars' a UInt32; an integer sum an Int128, Polars' an
	// Int64; a sum of none null, Polars' 0 (feature-gaps.md §9, step 296).
	counts := oracleDifference{why: "a count is a Uint64 here, Polars' a UInt32", dtypes: map[string]string{"x": "Uint64", "y": "Uint64"}}
	oracleDifferences["pivot/none_len"] = counts
	oracleDifferences["pivot/none_n_unique"] = counts
	oracleDifferences["pivot/none_sum"] = oracleDifference{
		why:    "an integer sum is an Int128 here, Polars' an Int64; a pivot's cell of none is null under Sum here, 0 in Polars",
		dtypes: map[string]string{"x": "Int128", "y": "Int128"},
		cells:  map[string]map[int]any{"x": {1: nil}, "y": {0: nil}},
	}
	oracleDifferences["pivot/none_sum_fill_0"] = oracleDifference{
		why:    "an integer sum is an Int128 here, Polars' an Int64",
		dtypes: map[string]string{"x": "Int128", "y": "Int128"},
	}
}
