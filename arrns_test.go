package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

type arrNsRow struct {
	A *[3]*int64
	T *[3]*string
	B *[3]*bool
	I int64
}

// arrNsFrame is step 342's frame, Polars': three Arrays of three, a null element in
// each, a null array, and a row's index.
func arrNsFrame() *ursus.LazyFrame {
	i := func(v int64) *int64 { return &v }
	s := func(v string) *string { return &v }
	b := func(v bool) *bool { return &v }
	return ursus.FromRows([]arrNsRow{
		{&[3]*int64{i(3), i(1), i(2)}, &[3]*string{s("b"), s("a"), s("b")}, &[3]*bool{b(true), b(false), b(true)}, 0},
		{&[3]*int64{i(4), nil, i(6)}, &[3]*string{s("x"), nil, s("y")}, &[3]*bool{nil, b(true), b(true)}, 1},
		{nil, nil, nil, 2},
		{&[3]*int64{i(2), i(2), i(1)}, &[3]*string{s("q"), s("q"), s("q")}, &[3]*bool{b(false), b(false), b(false)}, -1},
	})
}

// TestArrayNamespace is step 342: Polars' arr namespace. Every answer is Polars
// 1.44.1's but where noted: a sum of integers is an Int128, a count and a position
// a Uint64, as List()'s, and a null array's struct a null one.
func TestArrayNamespace(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	a, el := c("a").Arr(), ursus.Element
	lf := arrNsFrame().Select(c("A").Alias("a"), c("T").Alias("t"), c("B").Alias("b"), c("I").Alias("i"))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"max", a.Max(), "3 6 <nil> 2 Int64"},
		{"min", a.Min(), "1 4 <nil> 1 Int64"},
		{"sum", a.Sum(), "6 10 <nil> 5 Int128"},
		{"mean", a.Mean(), "2 5 <nil> 1.6666666666666667 Float64"},
		{"median", a.Median(), "2 5 <nil> 2 Float64"},
		// Polars' n_unique counts a null as a value, [4, null, 6] 3; ursus's n_unique
		// passes it over, as its aggregate does (feature-gaps §9).
		{"n_unique", a.NUnique(), "3 2 <nil> 2 Uint64"},
		{"arg_min", a.ArgMin(), "1 0 <nil> 2 Uint64"},
		{"arg_max", a.ArgMax(), "0 2 <nil> 0 Uint64"},
		{"first", a.First(), "3 4 <nil> 2 Int64"},
		{"last", a.Last(), "2 6 <nil> 1 Int64"},
		{"get(1)", a.Get(1), "1 <nil> <nil> 2 Int64"},
		{"get(i)", a.Get(c("i")), "3 <nil> <nil> 1 Int64"},
		{"contains(2)", a.Contains(int64(2)), "true false <nil> true Bool"},
		{"count_matches(2)", a.CountMatches(int64(2)), "1 0 <nil> 2 Uint32"},
		{"len", a.Len(), "3 3 <nil> 3 Uint32"},
		{"head(2)", a.Head(2), "[3 1] [4 <nil>] <nil> [2 2] List(Int64)"},
		{"tail(2)", a.Tail(2), "[1 2] [<nil> 6] <nil> [2 1] List(Int64)"},
		{"slice(1, 2)", a.Slice(1, 2), "[1 2] [<nil> 6] <nil> [2 1] List(Int64)"},
		{"reverse", a.Reverse(), "[2 1 3] [6 <nil> 4] <nil> [1 2 2] Array(Int64, 3)"},
		{"shift(1)", a.Shift(1), "[<nil> 3 1] [<nil> 4 <nil>] <nil> [<nil> 2 2] Array(Int64, 3)"},
		{"shift(-1)", a.Shift(-1), "[1 2 <nil>] [<nil> 6 <nil>] <nil> [2 1 <nil>] Array(Int64, 3)"},
		{"sort", a.Sort(), "[1 2 3] [<nil> 4 6] <nil> [1 2 2] Array(Int64, 3)"},
		{"sort descending, nulls last", a.SortWith(true, true), "[3 2 1] [6 4 <nil>] <nil> [2 2 1] Array(Int64, 3)"},
		{"unique", a.Unique(), "[3 1 2] [4 <nil> 6] <nil> [2 1] List(Int64)"},
		{"eval", a.Eval(el().Mul(int64(10)), false), "[30 10 20] [40 <nil> 60] <nil> [20 20 10] Array(Int64, 3)"},
		{"eval as a list", a.Eval(el().Mul(int64(10)), true), "[30 10 20] [40 <nil> 60] <nil> [20 20 10] List(Int64)"},
		{"agg", a.Agg(el().Max()), "3 6 <nil> 2 Int64"},
		{"dot", a.Dot(c("a")), "14 52 <nil> 9 Int128"},
		{"dot of a null row", a.Dot(ursus.When(c("i").Eq(int64(0))).Then(ursus.Null(ursus.Array(ursus.Int64, 3))).
			Otherwise(a.Reverse())), "<nil> 48 <nil> 8 Int128"},
		{"to_list", a.ToList(), "[3 1 2] [4 <nil> 6] <nil> [2 2 1] List(Int64)"},
		{"join", c("t").Arr().Join("-"), "b-a-b x-y <nil> q-q-q String"},
		{"any", c("b").Arr().Any(true), "true true <nil> false Bool"},
		{"all", c("b").Arr().All(true), "false true <nil> false Bool"},
	} {
		if got := cells(t, lf.Select(tc.e.Alias("r")), "r"); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	// Polars' std and var, to the last few bits.
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want []float64
	}{
		{"std", a.Std(1), []float64{1, 1.4142135623730951, 0.5773502691896257}},
		{"var of ddof 0", a.Var(0), []float64{0.6666666666666666, 1, 0.2222222222222222}},
	} {
		if got := floatsOf(t, lf.Select(tc.e.Alias("r")).DropNulls()); !nearFloats(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := cells(t, lf.Select(a.Explode()), "a"); got != "3 1 2 4 <nil> 6 <nil> 2 2 1 Int64" {
		t.Errorf("explode: %s", got)
	}
	// Polars names the fields field_0 on, and gives a null array a struct of nulls;
	// here it is a null struct, as List().ToStruct gives.
	if got := cells(t, lf.Select(a.ToStruct().Alias("r")), "r"); got !=
		"map[field_0:3 field_1:1 field_2:2] map[field_0:4 field_1:<nil> field_2:6] <nil> map[field_0:2 field_1:2 field_2:1] "+
			"Struct(field_0: Int64, field_1: Int64, field_2: Int64)" {
		t.Errorf("to_struct: %s", got)
	}
	if got := cells(t, lf.Select(a.ToStruct("x", "y", "z").Struct().Field("y").Alias("r")), "r"); got != "1 <nil> <nil> 2 Int64" {
		t.Errorf("to_struct named: %s", got)
	}
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		// Polars: "`array.eval` is not allowed with non-length preserving expressions".
		{"eval of another length", a.Eval(el().Unique(), false), "changes each array's length"},
		{"eval of an aggregate", a.Eval(el().Sum(), false), "changes each array's length"},
		{"to_struct of two names", a.ToStruct("x", "y"), "names 2 fields of an Array of 3"},
		{"dot of another size", a.Dot(c("a").Arr().Head(2).List().ToArray(2)), "of one size"},
		{"dot of text", c("t").Arr().Dot(c("t")), "multiplies numbers"},
		{"a List's sum", c("a").Arr().ToList().Arr().Sum(), "requires an Array"},
		{"a List reversed", c("a").Arr().ToList().Arr().Reverse(), "requires an Array"},
	} {
		if _, err := lf.Select(tc.e.Alias("r")).Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %q", tc.name, err, tc.want)
		}
	}
}
