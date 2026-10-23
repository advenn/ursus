package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestReshapeAndConcatArr is step 343: Polars' reshape and concat_arr, which make
// Arrays of a column's values and of a row's. Every answer is Polars 1.44.1's.
func TestReshapeAndConcatArr(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(i64s("x", 1, 2, 3, 4, 5, 6), i64s("n", 1, nil, 3, 4, 5, 6))
	arr := ursus.Frame(i64s("k", 1, 2, 3)).Select(ursus.When(c("k").Eq(int64(2))).Then(ursus.Null(ursus.List(ursus.Int64))).
		Otherwise(ursus.ConcatList(c("k").Mul(int64(2)).Sub(int64(1)), c("k").Mul(int64(2)))).List().ToArray(2).Alias("a"))
	lists := ursus.Frame(i64s("k", 1, 2, 3, 4)).Select(ursus.When(c("k").Eq(int64(1))).Then(ursus.ConcatList(ursus.Lit(int64(1)), ursus.Lit(int64(2)), ursus.Lit(int64(3)))).
		When(c("k").Eq(int64(2))).Then(ursus.ConcatList(ursus.Lit(int64(4)))).
		When(c("k").Eq(int64(3))).Then(ursus.Null(ursus.List(ursus.Int64))).
		Otherwise(ursus.ConcatList(ursus.Lit(int64(0))).List().Head(0)).Alias("l"))
	g := ursus.Frame(i64s("g", 1, 1, 1, 1, 2, 2), i64s("x", 1, 2, 3, 4, 5, 6))
	pairs := ursus.Frame(i64s("i", 0, 1, 2), i64s("s", 10, nil, 30), f64s("f", 0.5, 1.5, 2.5)).WithColumns(
		ursus.When(c("i").Eq(int64(2))).Then(ursus.Null(ursus.List(ursus.Int64))).
			Otherwise(ursus.ConcatList(c("i").Mul(int64(2)).Add(int64(1)), c("i").Mul(int64(2)).Add(int64(2)))).List().ToArray(2).Alias("a"),
		ursus.ConcatList(c("i").Add(int64(5))).List().ToArray(1).Alias("b"))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		col  string
		want string
	}{
		{"(-1, 3)", lf.Select(c("x").Reshape(-1, 3)), "x", "[1 2 3] [4 5 6] Array(Int64, 3)"},
		{"(2, 3)", lf.Select(c("x").Reshape(2, 3)), "x", "[1 2 3] [4 5 6] Array(Int64, 3)"},
		{"(-1,)", lf.Select(c("x").Reshape(-1)), "x", "1 2 3 4 5 6 Int64"},
		{"a null value", lf.Select(c("n").Reshape(-1, 2)), "n", "[1 <nil>] [3 4] [5 6] Array(Int64, 2)"},
		{"(-1, 1, 3)", lf.Select(c("x").Reshape(-1, 1, 3)), "x", "[[1 2 3]] [[4 5 6]] Array(Array(Int64, 3), 1)"},
		{"an Array's values, flattened", arr.Select(c("a").Reshape(-1)), "a", "1 2 <nil> <nil> 5 6 Int64"},
		{"an Array's values, by two", arr.Select(c("a").Reshape(-1, 2)), "a", "[1 2] [<nil> <nil>] [5 6] Array(Int64, 2)"},
		{"an Array's values, by three", arr.Select(c("a").Reshape(-1, 3)), "a", "[1 2 <nil>] [<nil> 5 6] Array(Int64, 3)"},
		{"a List's values, flattened", lists.Select(c("l").Reshape(-1)), "l", "1 2 3 4 Int64"},
		{"a List's values, by two", lists.Select(c("l").Reshape(-1, 2)), "l", "[1 2] [3 4] Array(Int64, 2)"},
		{"no value", lf.Filter(c("x").Gt(int64(9))).Select(c("x").Reshape(-1, 2)), "x", " Array(Int64, 2)"},
		{"in an aggregation", g.GroupBy(c("g")).Agg(c("x").Reshape(-1, 2)).Sort(ursus.Asc(c("g"))), "x",
			"[[1 2] [3 4]] [[5 6]] List(Array(Int64, 2))"},
		{"concat_arr", pairs.Select(ursus.ConcatArr(c("a"), c("b"))), "a", "[1 2 5] [3 4 6] <nil> Array(Int64, 3)"},
		{"concat_arr of a value", pairs.Select(ursus.ConcatArr(c("a"), c("s"))), "a", "[1 2 10] [3 4 <nil>] <nil> Array(Int64, 3)"},
		{"concat_arr of one value", pairs.Select(ursus.ConcatArr(c("s"))), "s", "[10] [<nil>] [30] Array(Int64, 1)"},
	} {
		if got := cells(t, tc.q, tc.col); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "cannot reshape array of size 6 into shape (inferred, 4)".
		{"six values by four", lf.Select(c("x").Reshape(-1, 4)), "cannot reshape 6 values"},
		{"six values in three rows of three", lf.Select(c("x").Reshape(3, 3)), "cannot reshape 6 values"},
		// Polars: "can only infer the first dimension".
		{"a later dimension inferred", lf.Select(c("x").Reshape(3, -1)), "can only infer the first dimension"},
		{"a zero dimension", lf.Select(c("x").Reshape(-1, 0)), "zero dimension"},
		{"a group of four by three", g.GroupBy(c("g")).Agg(c("x").Reshape(-1, 3)), "cannot reshape 4 values"},
		// Polars: "Series length 2 doesn't match the DataFrame height of 6".
		{"beside a column", lf.Select(c("x"), c("x").Reshape(-1, 3).Alias("r")), "length"},
		// Polars: "concat_arr dtype mismatch: expected i64 or array[i64]".
		{"concat_arr of a float", pairs.Select(ursus.ConcatArr(c("a"), c("f"))), "one element type"},
		{"concat_arr of a List", pairs.Select(ursus.ConcatArr(c("a").Arr().ToList())), "takes Arrays and values"},
	} {
		if _, err := tc.q.Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %q", tc.name, err, tc.want)
		}
	}
}
