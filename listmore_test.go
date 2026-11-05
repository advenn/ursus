package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
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

// TestListMethods is L of v0.6.6-scope.md (step 269): list.concat, shift, diff,
// count_matches, any, all, item and sort with nulls last. Every answer over
// listMoreFixture is Polars 2.1.0's, recorded in the listmore group of
// testdata/oracle and spelled in oracle_listmore_test.go since step 499, which took
// this file's hand table; what stays asserts the refusals.
func TestListMethods(t *testing.T) {
	c := ursus.Col
	lf := listMoreFixture(t)
	L := c("l").List()
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
// properties are held; the lengths a draw has are the oracle's
// listmore/sample_lengths. Every drawn value is from its list, the same seed draws
// alike, a whole list unshuffled is the list, a whole list shuffled sums to the
// list's sum, and without replacement a list shorter than n is refused, as Polars
// refuses it.
func TestListSample(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	lf := listMoreFixture(t)
	L := c("l").List()
	// Every drawn value is in its list: no row's draw holds a value outside it.
	outside := lf.Select(L.SampleN(4, true, true, 7).List().Eval(el().IsIn(1, 2, 3, 4, 5).Or(el().IsNull())).
		List().All(true).Alias("in")).Filter(c("in").Not())
	if df, err := outside.Collect(t.Context()); err != nil || df.Height() != 0 {
		t.Errorf("a value drawn from outside its list: %v, %v", df, err)
	}
	draw := func() *ursus.DataFrame {
		df, err := lf.Select(L.SampleN(4, true, true, 42).Alias("r")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return df
	}
	ursustest.AssertFrameEqual(t, draw(), draw())
	// Three of three, kept in order, are the list; shuffled, their sum is the list's.
	first := lf.Filter(c("id").Eq(1))
	whole, err := first.Select(L.SampleN(3, false, false, 3).Alias("l")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	list, err := first.Select(c("l")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, whole, list)
	sums, err := first.Select(L.SampleN(3, false, true, 3).List().Sum().Alias("s"), L.Sum().Alias("l")).
		Filter(c("s").Ne(c("l"))).Collect(t.Context())
	if err != nil || sums.Height() != 0 {
		t.Errorf("a shuffled whole list does not sum to the list: %v, %v", sums, err)
	}
	if _, err := lf.Select(L.SampleN(2, false, false, 1)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "takes 2 of row 2's 0 elements without replacement") {
		t.Errorf("too many: %v", err)
	}
}
