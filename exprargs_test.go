package ursus_test

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// exprArgsFixture is eight rows of a List(Int64) beside a value, an index and a
// needle each: null lists, empty ones and null elements, and an index past either
// end.
func exprArgsFixture(t *testing.T) *ursus.LazyFrame {
	t.Helper()
	path := writeElemLists(t, []int64{1, 2, 3, 4, 5, 6, 7, 8}, []lrow{
		{vals: []elem{val(1), val(2)}, ok: true},
		{vals: []elem{val(3)}, ok: true},
		{vals: []elem{nullElem, val(1)}, ok: true},
		{ok: false},
		{ok: true},
		{vals: []elem{nullElem, val(1)}, ok: true},
		{vals: []elem{val(4), val(5)}, ok: true},
		{vals: []elem{val(3), nullElem}, ok: true},
	})
	present := []bool{true, true, false, true, true, true, true, true}
	cols := ursus.Frame(ursus.Values("id", []int64{1, 2, 3, 4, 5, 6, 7, 8}),
		ursus.Values("g", []int64{1, 1, 1, 1, 2, 2, 2, 2}),
		ursus.ValuesNullable("a", []int64{1, 2, 0, 4, 5, 2, 5, 3}, present),
		ursus.ValuesNullable("i", []int64{0, -1, 1, 0, 5, -3, -2, 2},
			[]bool{true, true, true, false, true, true, true, true}),
		ursus.ValuesNullable("v", []int64{2, 9, 1, 6, 0, 0, 4, 3},
			[]bool{true, true, true, true, false, false, true, true}))
	return ursus.ScanParquet(path).Join(cols, ursus.JoinOn(ursus.Col("id"))).
		Sort(ursus.Asc(ursus.Col("id")))
}

// TestListArgumentsAreExpressions is E5's first half (step 254): is_in against each
// row's own list, and list.get and list.contains with a per-row index and value.
// Every answer is Polars 1.44.1's on the same frame, list.get's with null_on_oob,
// ursus's Get being null out of range.
func TestListArgumentsAreExpressions(t *testing.T) {
	c := ursus.Col
	lf := exprArgsFixture(t)
	// Polars: in [T, F, None, None, F, F, T, T], get [1, 3, 1, None, None, None, 4,
	// None], has [T, F, T, None, F, T, T, T].
	want := "in,get,has\ntrue,1,true\nfalse,3,false\n,1,true\n,,\nfalse,,false\n" +
		"false,,true\ntrue,4,true\ntrue,,true\n"
	for _, size := range []int{1, 3, 8192} {
		for name, q := range map[string]*ursus.LazyFrame{
			"plain": lf.Select(c("a").IsIn(c("tags")).Alias("in"),
				c("tags").List().Get(c("i")).Alias("get"),
				c("tags").List().Contains(c("v")).Alias("has")),
			// Values meet as == compares them: a narrower value, a wider list, an index
			// of another integer type, a needle that is a float.
			"cast": lf.Select(c("a").Cast(ursus.Int8).IsIn(c("tags")).Alias("in"),
				c("tags").List().Get(c("i").Cast(ursus.Int8)).Alias("get"),
				c("tags").List().Contains(c("v").Cast(ursus.Float64)).Alias("has")),
			"strings": lf.Select(c("a").Cast(ursus.String).IsIn(c("tags").Cast(ursus.List(ursus.String))).Alias("in"),
				c("tags").Cast(ursus.List(ursus.String)).List().Get(c("i")).Alias("get"),
				c("tags").Cast(ursus.List(ursus.String)).List().Contains(c("v").Cast(ursus.String)).Alias("has")),
		} {
			var got bytes.Buffer
			if err := q.WriteCSV(t.Context(), &got, ursus.WithBatchSize(size)); err != nil {
				t.Fatalf("%s, batch %d: %v", name, size, err)
			}
			if got.String() != want {
				t.Errorf("%s, batch %d:\ngot  %q\nwant %q", name, size, got.String(), want)
			}
		}
	}

	// In an aggregation, a value a row as anywhere. Polars: n_in [1, 2], n_has [2, 3].
	var got bytes.Buffer
	if err := lf.GroupBy(c("g")).Agg(c("a").IsIn(c("tags")).Sum().Alias("n_in"),
		c("tags").List().Contains(c("v")).Sum().Alias("n_has")).Sort(ursus.Asc(c("g"))).
		WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "g,n_in,n_has\n1,1,2\n2,2,3\n"; got.String() != want {
		t.Errorf("in an aggregation:\ngot  %q\nwant %q", got.String(), want)
	}

	// A literal index is a column one row long.
	got.Reset()
	if err := lf.Select(c("tags").List().Get(ursus.Lit(-1)).Alias("last")).
		WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "last\n2\n3\n1\n\n\n1\n5\n\n"; got.String() != want {
		t.Errorf("a literal index:\ngot  %q\nwant %q", got.String(), want)
	}
}

// TestIsInAListMatchesNaNAndZeroes: NaN is in a list holding NaN and -0.0 in one
// holding 0.0, as is_in's constant form has them. Polars: [True, True, False, None].
func TestIsInAListMatchesNaNAndZeroes(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2, 3, 4}),
		ursus.Values("f", []float64{math.NaN(), 1, 0, 2, 1, 1}),
		ursus.ValuesNullable("x", []float64{math.NaN(), 9, math.Copysign(0, -1), 9, 1.5, 0},
			[]bool{true, true, true, true, true, false}))
	var got bytes.Buffer
	if err := lf.GroupBy(c("g")).Agg(c("f").Implode().Alias("l"), c("x").First()).
		Sort(ursus.Asc(c("g"))).Select(c("x").IsIn(c("l"))).WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "x\ntrue\ntrue\nfalse\n\n"; got.String() != want {
		t.Errorf("got %q\nwant %q", got.String(), want)
	}
}

// TestListArgumentsRefuse: what == cannot compare, an index that is not an integer,
// and a column of the value's own type, which Polars deprecates as ambiguous.
func TestListArgumentsRefuse(t *testing.T) {
	c := ursus.Col
	lf := exprArgsFixture(t)
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"is_in a list of another type": {c("a").IsIn(c("tags").Cast(ursus.List(ursus.String))), "cannot compare"},
		"contains another type":        {c("tags").List().Contains(c("v").Cast(ursus.String)), "cannot compare"},
		"a float index":                {c("tags").List().Get(c("i").Cast(ursus.Float64)), "integer"},
		"is_in a column of its type":   {c("a").IsIn(c("v")), "Implode"},
		"is_in two expressions":        {c("a").IsIn(c("tags"), c("tags")), "one expression"},
		"is_in an aggregate":           {c("a").IsIn(c("v").Implode()), "not there yet"},
	} {
		_, err := lf.Select(tc.e).Collect(t.Context())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
