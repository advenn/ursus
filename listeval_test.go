package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// evalFixture is Polars' l: [[3, 1, 2], None, [], [5, None, 4]].
func evalFixture(t *testing.T) *ursus.LazyFrame {
	t.Helper()
	return ursus.ScanParquet(writeElemLists(t, []int64{1, 2, 3, 4}, []lrow{
		{vals: []elem{val(3), val(1), val(2)}, ok: true},
		{ok: false},
		{ok: true},
		{vals: []elem{val(5), nullElem, val(4)}, ok: true},
	})).Select(ursus.Col("tags").Alias("l"))
}

// listText is each row's list as its length and first three elements, which tells a
// null list from an empty one and from a list of a null.
func listText(t *testing.T, lf *ursus.LazyFrame, e ursus.Expr) string {
	t.Helper()
	r := e.Alias("r")
	return csvOf(t, lf.Select(r).Select(ursus.Col("r").List().Len().Alias("n"),
		ursus.Col("r").List().Get(0).Alias("e0"), ursus.Col("r").List().Get(1).Alias("e1"),
		ursus.Col("r").List().Get(2).Alias("e2")))
}

// TestListEval is §3 of v0.6.6-scope.md (step 268): List().Eval, Agg and Filter, the
// body over each list's elements. Every answer is Polars 1.44.1's, but the sum of an
// empty list, null here as every list sum is, 0 there.
func TestListEval(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	lf := evalFixture(t)
	L := c("l").List()
	const head = "n,e0,e1,e2\n"
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		// [[6, 2, 4], None, [], [10, None, 8]]
		{"doubled", L.Eval(el().Mul(2)), "3,6,2,4\n,,,\n0,,,\n3,10,,8\n"},
		// [[6], None, [0], [9]]
		{"a sum", L.Eval(el().Sum()), "1,6,,\n,,,\n1,,,\n1,9,,\n"},
		// [[2.0], None, [None], [4.5]]
		{"a mean", L.Eval(el().Mean()), "1,2.0,,\n,,,\n1,,,\n1,4.5,,\n"},
		// [[1.0, -1.0, 0.0], None, [], [0.5, None, -0.5]]
		{"centred", L.Eval(el().Sub(el().Mean())), "3,1.0,-1.0,0.0\n,,,\n0,,,\n3,0.5,,-0.5\n"},
		// [[3, 2], None, [], [5, 4]]
		{"filtered", L.Eval(el().Filter(el().Gt(1))), "2,3,2,\n,,,\n0,,,\n2,5,4,\n"},
		// [[1, 2, 3], None, [], [None, 4, 5]]
		{"sorted", L.Eval(el().Sort()), "3,1,2,3\n,,,\n0,,,\n3,,4,5\n"},
		// [[2, 4, 6], None, [], [None, 8, 10]]
		{"sorted, doubled", L.Eval(el().Sort().Mul(2)), "3,2,4,6\n,,,\n0,,,\n3,,8,10\n"},
		// [[3, 1], None, [], [5, None]]
		{"a head", L.Eval(el().Head(2)), "2,3,1,\n,,,\n0,,,\n2,5,,\n"},
		// [[5], None, [0], [9]]
		{"a filtered sum", L.Eval(el().Filter(el().Gt(1)).Sum()), "1,5,,\n,,,\n1,,,\n1,9,,\n"},
		// [[3.0, 1.0, 2.0], None, [], [2.0, 1.0]]. Without the drop, Polars leaves a
		// null's rank null, where ursus's rank, the frame's too, ranks it first:
		// feature-gaps.md §1, found here.
		{"ranked", L.Eval(el().DropNulls().Rank(ursus.RankAverage, false)), "3,3.0,1.0,2.0\n,,,\n0,,,\n2,2.0,1.0,\n"},
		// [[3, 4, 6], None, [], [5, None, 9]]
		{"a running sum", L.Eval(el().CumSum(false)), "3,3,4,6\n,,,\n0,,,\n3,5,,9\n"},
		// [[None, 3, 1], None, [], [None, 5, None]]
		{"shifted", L.Eval(el().Shift(1)), "3,,3,1\n,,,\n0,,,\n3,,5,\n"},
		// [['3', '1', '2'], None, [], ['5', None, '4']]
		{"cast", L.Eval(el().Cast(ursus.String)), "3,3,1,2\n,,,\n0,,,\n3,5,,4\n"},
		// [[False, False, False], None, [], [False, True, False]]
		{"is_null", L.Eval(el().IsNull()), "3,false,false,false\n,,,\n0,,,\n3,false,true,false\n"},
		// [[1, 1, 1], None, [], [1, 1, 1]]
		{"a constant", L.Eval(ursus.Lit(1)), "3,1,1,1\n,,,\n0,,,\n3,1,1,1\n"},
		// [[3], None, [0], [2]], [[3], None, [0], [3]], [[3], None, [None], [5]]
		{"count", L.Eval(el().Count()), "1,3,,\n,,,\n1,0,,\n1,2,,\n"},
		{"len", L.Eval(el().Len()), "1,3,,\n,,,\n1,0,,\n1,3,,\n"},
		{"first", L.Eval(el().First()), "1,3,,\n,,,\n1,,,\n1,5,,\n"},
		// list.agg: [[6, 2, 4], None, [], [10, None, 8]]; [[3, 2], None, [], [5, 4]]
		{"agg of values", L.Agg(el().Mul(2)), "3,6,2,4\n,,,\n0,,,\n3,10,,8\n"},
		{"agg of a filter", L.Agg(el().Filter(el().Gt(1))), "2,3,2,\n,,,\n0,,,\n2,5,4,\n"},
		// list.filter: [[3, 2], None, [], [5, 4]]; by is_not_null [[3, 1, 2], None, [], [5, 4]]
		{"list.filter", L.Filter(el().Gt(1)), "2,3,2,\n,,,\n0,,,\n2,5,4,\n"},
		{"list.filter, not null", L.Filter(el().IsNotNull()), "3,3,1,2\n,,,\n0,,,\n2,5,4,\n"},
	} {
		if got := listText(t, lf, tc.e); got != head+tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, head+tc.want)
		}
	}

	// list.agg of an aggregate is its value: [6, None, 0, 9] and [2.0, None, None, 4.5].
	if got, want := csvOf(t, lf.Select(L.Agg(el().Sum()).Alias("s"), L.Agg(el().Mean()).Alias("m"))),
		"s,m\n6,2.0\n,\n,\n9,4.5\n"; got != want {
		t.Errorf("agg of aggregates:\ngot  %q\nwant %q", got, want)
	}
	// Named after the list, as Polars names it. An Int64 sums to Int128 here.
	df, err := lf.Select(L.Eval(el().Sum())).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s := df.Schema().String(); !strings.Contains(s, "l: List(Int128)") {
		t.Errorf("eval's name and type: %s", s)
	}
	// Strings, by a split: [[1, 2], [3]].
	words := ursus.Frame(ursus.Values("s", []string{"a,bb", "ccc"})).Select(c("s").Str().Split(","))
	if got, want := listText(t, words, c("s").List().Eval(el().Str().LenChars())), head+"2,1,2,\n1,3,,\n"; got != want {
		t.Errorf("strings:\ngot  %q\nwant %q", got, want)
	}
}

// TestListEvalRefuses: another column in the body, as Polars refuses it; Element
// outside a list's body; a window partitioned inside one.
func TestListEvalRefuses(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	lf := evalFixture(t).WithColumns(ursus.Lit(1).Alias("x"))
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"another column":     {c("l").List().Eval(el().Add(c("x"))), "only Element() is read"},
		"element outside":    {el().Mul(2), "read only inside List().Eval"},
		"a partitioned over": {c("l").List().Eval(el().Sum().Over(c("x"))), "inside list.eval a window is the list"},
	} {
		if _, err := lf.Select(tc.e).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
