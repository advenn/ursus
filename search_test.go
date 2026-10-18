package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// searchFrame is Polars' frame: g [1, 1, 1, 2, 2, 2], a [1, 3, 3, 5, None, 9] and s
// ["x", "y", "x", None, "x", "z"].
func searchFrame() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}),
		ursus.ValuesNullable("a", []int64{1, 3, 3, 5, 0, 9}, []bool{true, true, true, true, false, true}),
		ursus.ValuesNullable("s", []string{"x", "y", "x", "", "x", "z"}, []bool{true, true, true, false, true, true}))
}

// nullableInts is a column a of vals, a null where ok is false.
func nullableInts(vals []int64, ok []bool) *ursus.LazyFrame {
	return ursus.Frame(ursus.ValuesNullable("a", vals, ok))
}

// TestSearchSortedAndIndexOf is M3 of v0.6.6-scope.md (step 279). Every answer is
// Polars 1.44.1's.
func TestSearchSortedAndIndexOf(t *testing.T) {
	c := ursus.Col
	a := c("a")
	lf := searchFrame()
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"search_sorted", lf.Select(a.SearchSorted(3, ursus.SideAny, false)), "a\n1\n"},
		{"to the right", lf.Select(a.SearchSorted(3, ursus.SideRight, false)), "a\n3\n"},
		{"by group", lf.GroupBy(c("g")).Agg(a.SearchSorted(3, ursus.SideLeft, false)).Sort(ursus.Asc(c("g"))).Select(a), "a\n1\n0\n"},
		{"descending", ursus.Frame(ursus.Values("a", []int64{5, 3, 3, 1})).Select(a.SearchSorted(3, ursus.SideLeft, true).Alias("l"),
			a.SearchSorted(3, ursus.SideRight, true).Alias("r")), "l,r\n1,3\n"},
		{"descending, more above than below", ursus.Frame(ursus.Values("a", []int64{9, 8, 7, 3, 1})).
			Select(a.SearchSorted(4, ursus.SideLeft, true).Alias("l"), a.SearchSorted(7, ursus.SideRight, true).Alias("r")), "l,r\n3,3\n"},
		{"descending, nulls first", nullableInts([]int64{0, 3, 2, 1}, []bool{false, true, true, true}).
			Select(a.SearchSorted(2, ursus.SideLeft, true).Alias("l"), a.SearchSorted(2, ursus.SideRight, true).Alias("r")), "l,r\n2,3\n"},
		{"NaN last", ursus.Frame(ursus.Values("a", []float64{1, 2, math.NaN()})).
			Select(a.SearchSorted(math.NaN(), ursus.SideLeft, false).Alias("nan"), a.SearchSorted(3.0, ursus.SideLeft, false).Alias("three")),
			"nan,three\n2,2\n"},
		{"text", ursus.Frame(ursus.Values("a", []string{"a", "c", "e"})).Select(a.SearchSorted("d", ursus.SideLeft, false)), "a\n2\n"},
		{"no rows", ursus.Frame(ursus.Values("a", []int64{1})).Filter(a.Gt(5)).Select(a.SearchSorted(3, ursus.SideLeft, false)), "a\n0\n"},
		{"all null", nullableInts([]int64{0, 0}, []bool{false, false}).Select(a.SearchSorted(3, ursus.SideLeft, false)), "a\n2\n"},
		{"index_of", lf.Select(a.IndexOf(3), c("s").IndexOf("x").Alias("s"), a.IndexOf(nil).Alias("n"), a.IndexOf(100).Alias("m")),
			"a,s,n,m\n1,0,4,\n"},
		{"index_of by group", lf.GroupBy(c("g")).Agg(a.IndexOf(3)).Sort(ursus.Asc(c("g"))).Select(a), "a\n1\n\n"},
		{"index_of NaN and 0", ursus.Frame(ursus.Values("a", []float64{1, math.NaN(), math.Copysign(0, -1)})).
			Select(a.IndexOf(math.NaN()).Alias("nan"), a.IndexOf(0.0).Alias("zero")), "nan,zero\n1,2\n"},
		{"index_of a null where none is", ursus.Frame(ursus.Values("a", []int64{1, 2})).Select(a.IndexOf(nil)), "a\n\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// A null value, and 2, in Polars' columns of nulls first, last and none.
	for _, tc := range []struct {
		vals []int64
		ok   []bool
		want string // the null's any, left and right, then 2's left and right
	}{
		{[]int64{0, 1, 2, 3}, []bool{false, true, true, true}, "0,0,1,2,3"},
		{[]int64{0, 0, 1, 2}, []bool{false, false, true, true}, "0,0,2,3,4"},
		{[]int64{1, 2, 3, 0}, []bool{true, true, true, false}, "3,3,4,1,2"},
		{[]int64{1, 2, 0, 0}, []bool{true, true, false, false}, "2,2,4,1,2"},
		{[]int64{1, 2, 3}, []bool{true, true, true}, "0,0,0,1,2"},
	} {
		q := nullableInts(tc.vals, tc.ok).Select(a.SearchSorted(nil, ursus.SideAny, false).Alias("na"),
			a.SearchSorted(nil, ursus.SideLeft, false).Alias("nl"), a.SearchSorted(nil, ursus.SideRight, false).Alias("nr"),
			a.SearchSorted(2, ursus.SideLeft, false).Alias("l"), a.SearchSorted(2, ursus.SideRight, false).Alias("r"))
		if got, want := csvOf(t, q), "na,nl,nr,l,r\n"+tc.want+"\n"; got != want {
			t.Errorf("%v %v:\ngot  %q\nwant %q", tc.vals, tc.ok, got, want)
		}
	}
	got, err := lf.Select(a.SearchSorted(3, ursus.SideAny, false).Alias("x"), a.IndexOf(3).Alias("y")).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Uint32 || got.Schema().Field(1).Type != ursus.Uint32 {
		t.Errorf("UInt32s: %v, %v", got, err)
	}
	if _, err := lf.Select(a.SearchSorted(3, "middle", false)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "not a side") {
		t.Errorf("a side that is none: %v", err)
	}
}

// TestPeaks: peak_max and peak_min, an end compared with its type's zero, as Polars
// fills its shifts with 0. Every answer is Polars 1.44.1's.
func TestPeaks(t *testing.T) {
	c := ursus.Col
	x := c("x")
	both := func(lf *ursus.LazyFrame) string {
		return csvOf(t, lf.Select(x.PeakMax().Alias("max"), x.PeakMin().Alias("min")))
	}
	floats := func(v ...float64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	ints := func(v ...int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		want string
	}{
		{"Polars' column", ursus.Frame(ursus.ValuesNullable("x", []int64{1, 3, 2, 4, 4, 1, 5, 0, 2},
			[]bool{true, true, true, true, true, true, true, false, true})),
			"max,min\nfalse,false\ntrue,false\nfalse,true\nfalse,false\nfalse,false\nfalse,true\n,false\n,\n,false\n"},
		{"NaN above every number", floats(1, math.NaN(), 2, 0.5, 3),
			"max,min\nfalse,false\ntrue,false\nfalse,false\nfalse,true\ntrue,false\n"},
		{"negatives", ints(-1, -2, -3), "max,min\nfalse,false\nfalse,false\nfalse,true\n"},
		{"falling", ints(3, 2, 1), "max,min\ntrue,false\nfalse,false\nfalse,false\n"},
		{"one value", ints(5), "max,min\ntrue,false\n"},
		{"below zero", floats(-0.5, -1), "max,min\nfalse,false\nfalse,true\n"},
		{"Booleans", ursus.Frame(ursus.Values("x", []bool{true, false})), "max,min\ntrue,false\nfalse,false\n"},
		{"dates, against the epoch", ints(1, 3, 2).Select(x.Cast(ursus.Date)), "max,min\nfalse,false\ntrue,false\nfalse,false\n"},
	} {
		if got := both(tc.lf); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// A group's own peaks, over its rows: Polars' group_by gives [False, True,
	// False] and [True, False, True], a list each.
	groups := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}), ursus.Values("x", []int64{1, 3, 2, 3, 1, 2}))
	if got := csvOf(t, groups.Select(x.PeakMax().Over(c("g")))); got != "x\nfalse\ntrue\nfalse\ntrue\nfalse\ntrue\n" {
		t.Errorf("over g: %q", got)
	}
	if _, err := ursus.Frame(ursus.Values("x", []string{"a", "b"})).Select(x.PeakMax()).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "no zero") {
		t.Errorf("text: %v", err)
	}
}

// TestHash: equal values hash alike, -0 with 0 and NaN with NaN, a null too; the seed
// changes them. The numbers are ursus's, not Polars' (v0.6.6-scope.md §6.3).
func TestHash(t *testing.T) {
	c := ursus.Col
	x := c("x")
	lf := ursus.Frame(ursus.ValuesNullable("x", []float64{0, math.Copysign(0, -1), math.NaN(), math.NaN(), 0, 1.5},
		[]bool{true, true, true, true, false, true}))
	df, err := lf.Select(x.Hash(0).Alias("h"), x.Hash(9).Alias("s")).Collect(t.Context())
	if err != nil || df.Schema().Field(0).Type != ursus.Uint64 {
		t.Fatalf("%v, %v", df, err)
	}
	rows := strings.Split(strings.TrimSpace(csvOf(t, lf.Select(x.Hash(0).Alias("h"), x.Hash(9).Alias("s")))), "\n")[1:]
	h := func(i int) string { return strings.Split(rows[i], ",")[0] }
	s := func(i int) string { return strings.Split(rows[i], ",")[1] }
	if h(0) != h(1) || h(2) != h(3) {
		t.Errorf("-0 and 0, or NaN and NaN, hashed apart: %v", rows)
	}
	if h(0) == h(2) || h(0) == h(4) || h(0) == h(5) || h(2) == h(4) {
		t.Errorf("unequal values hashed alike: %v", rows)
	}
	if h(4) == "" {
		t.Errorf("a null has no hash: %v", rows)
	}
	for i := range rows {
		if h(i) == s(i) {
			t.Errorf("the seed changed nothing at row %d: %v", i, rows)
		}
	}
	if a, b := csvOf(t, lf.Select(x.Hash(0))), csvOf(t, lf.Select(x.Hash(0))); a != b {
		t.Errorf("one hash, two answers")
	}
}
