package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// listMoreFixture is Polars' frame: l [[3, 1, 2], None, [], [5, None, 4]], o [[9],
// [8], None, []], b [[True, False], [None, True], [], None], v [1, 2, 3, None] and n
// [1, -1, 2, 0].
func listMoreFixture(t *testing.T) *ursus.LazyFrame {
	t.Helper()
	c, el := ursus.Col, ursus.Element
	ids := []int64{1, 2, 3, 4}
	l := ursus.ScanParquet(writeElemLists(t, ids, []lrow{
		{vals: []elem{val(3), val(1), val(2)}, ok: true}, {ok: false}, {ok: true},
		{vals: []elem{val(5), nullElem, val(4)}, ok: true},
	})).Select(c("id"), c("tags").Alias("l"))
	b := ursus.ScanParquet(writeElemLists(t, ids, []lrow{
		{vals: []elem{val(1), val(0)}, ok: true}, {vals: []elem{nullElem, val(1)}, ok: true}, {ok: true}, {ok: false},
	})).Select(c("id"), c("tags").List().Eval(el().Eq(1)).Alias("b"))
	rest := ursus.Frame(ursus.Values("id", ids),
		ursus.ValuesNullable("v", []int64{1, 2, 3, 0}, []bool{true, true, true, false}),
		ursus.Values("n", []int64{1, -1, 2, 0}),
		ursus.ValuesNullable("os", []int64{9, 8, 0, 0}, []bool{true, true, false, true}),
		ursus.Values("oe", []int64{10, 9, 0, 0}))
	return l.Join(b, ursus.JoinOn(c("id"))).Join(rest, ursus.JoinOn(c("id"))).Sort(ursus.Asc(c("id"))).
		WithColumns(ursus.IntRanges(c("os"), c("oe"), 1).Alias("o"))
}

// listText4 is listText to four elements.
func listText4(t *testing.T, lf *ursus.LazyFrame, e ursus.Expr) string {
	t.Helper()
	r := ursus.Col("r").List()
	return csvOf(t, lf.Select(e.Alias("r")).Select(r.Len().Alias("n"), r.Get(0).Alias("e0"),
		r.Get(1).Alias("e1"), r.Get(2).Alias("e2"), r.Get(3).Alias("e3")))
}

// TestListMethods is L of v0.6.6-scope.md (step 269): list.concat, shift, diff,
// count_matches, any, all, item and sort with nulls last. Every answer is Polars
// 1.44.1's on the same frame.
func TestListMethods(t *testing.T) {
	c := ursus.Col
	lf := listMoreFixture(t)
	L := c("l").List()
	const head = "n,e0,e1,e2,e3\n"
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		// [[3, 1, 2, 9], None, None, [5, None, 4]]
		{"concat", L.Concat(c("o")), "4,3,1,2,9\n,,,,\n,,,,\n3,5,,4,\n"},
		// [[3, 1, 2, 1], None, [3], [5, None, 4, None]]
		{"concat a value", L.Concat(c("v")), "4,3,1,2,1\n,,,,\n1,3,,,\n4,5,,4,\n"},
		// [[None, 3, 1], None, [], [None, 5, None]]
		{"shift", L.Shift(1), "3,,3,1,\n,,,,\n0,,,,\n3,,5,,\n"},
		// [[1, 2, None], None, [], [None, 4, None]]
		{"shift back", L.Shift(-1), "3,1,2,,\n,,,,\n0,,,,\n3,,4,,\n"},
		// [[None, 3, 1], None, [], [5, None, 4]]
		{"shift by a column", L.Shift(c("n")), "3,,3,1,\n,,,,\n0,,,,\n3,5,,4,\n"},
		// [[None, -2, 1], None, [], [None, None, None]]
		{"diff", L.Diff(1, false), "3,,-2,1,\n,,,,\n0,,,,\n3,,,,\n"},
		// [[None, None, -1], None, [], [None, None, -1]]
		{"diff by two", L.Diff(2, false), "3,,,-1,\n,,,,\n0,,,,\n3,,,-1,\n"},
		// [[-2, 1], None, [], [None, None]]
		{"diff, nulls dropped", L.Diff(1, true), "2,-2,1,,\n,,,,\n0,,,,\n2,,,,\n"},
		// [[1, 2, 3], None, [], [4, 5, None]]
		{"sorted, nulls last", L.SortWith(false, true), "3,1,2,3,\n,,,,\n0,,,,\n3,4,5,,\n"},
		// [[3, 2, 1], None, [], [5, 4, None]]
		{"descending, nulls last", L.SortWith(true, true), "3,3,2,1,\n,,,,\n0,,,,\n3,5,4,,\n"},
	} {
		if got := listText4(t, lf, tc.e); got != head+tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, head+tc.want)
		}
	}

	B := c("b").List()
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		// [1, None, 0, 0] and, of nulls, [0, None, 0, 1]
		{"count_matches", L.CountMatches(int64(1)), "r\n1\n\n0\n0\n"},
		{"count_matches of null", L.CountMatches(nil), "r\n0\n\n0\n1\n"},
		// [True, True, False, None], [False, True, True, None]
		{"any", B.Any(true), "r\ntrue\ntrue\nfalse\n\n"},
		{"all", B.All(true), "r\nfalse\ntrue\ntrue\n\n"},
		// Kleene: [True, True, False, None], [False, None, True, None]
		{"any, Kleene", B.Any(false), "r\ntrue\ntrue\nfalse\n\n"},
		{"all, Kleene", B.All(false), "r\nfalse\n\ntrue\n\n"},
		// [9, 8, None, None]
		{"item, empty allowed", c("o").List().Item(true), "r\n9\n8\n\n\n"},
	} {
		if got := csvOf(t, lf.Select(tc.e.Alias("r"))); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}

	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		// Polars: "aggregation 'item' expected a single value, got none" and "got 3".
		"item of an empty list": {c("o").List().Item(false), "row 3 has 0"},
		"item of three":         {L.Item(true), "row 0 has 3"},
		"count of a string":     {L.CountMatches("x"), "cannot compare"},
		"any of integers":       {L.Any(true), "List of Booleans"},
	} {
		if _, err := lf.Select(tc.e).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestListSample: a seeded draw cannot be Polars', whose generator is its own, so its
// properties are held. Lengths are Polars': n with replacement [4, None, 0, 4], a half
// [1, None, 0, 1]; without replacement a list shorter than n is refused, as Polars
// refuses it. The same seed draws alike, every drawn value is from its list, a whole
// list unshuffled is the list, and shuffled one of its orders.
func TestListSample(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	lf := listMoreFixture(t)
	L := c("l").List()
	if got, want := csvOf(t, lf.Select(L.SampleN(4, true, false, 1).List().Len().Alias("n"),
		L.SampleFraction(0.5, false, false, 1).List().Len().Alias("f"))), "n,f\n4,1\n,\n0,0\n4,1\n"; got != want {
		t.Errorf("lengths:\ngot  %q\nwant %q", got, want)
	}
	// Every drawn value is in its list: none is outside {1, 2, 3} or {4, 5, null}.
	in := L.SampleN(4, true, true, 7).List().Eval(el().IsIn(1, 2, 3, 4, 5).Or(el().IsNull())).List().All(true)
	if got := csvOf(t, lf.Select(in.Alias("in"))); got != "in\ntrue\n\ntrue\ntrue\n" {
		t.Errorf("drawn from the list: %q", got)
	}
	a := csvOf(t, lf.Select(L.SampleN(4, true, true, 42).Alias("r")).Select(c("r").List().Eval(el().Cast(ursus.String)).List().Join("|")))
	b := csvOf(t, lf.Select(L.SampleN(4, true, true, 42).Alias("r")).Select(c("r").List().Eval(el().Cast(ursus.String)).List().Join("|")))
	if a != b {
		t.Errorf("the same seed drew %q and %q", a, b)
	}
	// Three of three, kept in order, are the list; shuffled, its sum is the same.
	first := lf.Filter(c("id").Eq(1))
	if got := listText4(t, first, L.SampleN(3, false, false, 3)); got != "n,e0,e1,e2,e3\n3,3,1,2,\n" {
		t.Errorf("the whole list in order: %q", got)
	}
	if got := csvOf(t, first.Select(L.SampleN(3, false, true, 3).List().Sum().Alias("s"))); got != "s\n6\n" {
		t.Errorf("shuffled: %q", got)
	}
	if _, err := lf.Select(L.SampleN(2, false, false, 1)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "takes 2 of row 2's 0 elements without replacement") {
		t.Errorf("too many: %v", err)
	}
}
