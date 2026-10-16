package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

func argFixture() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2, 1}),
		ursus.ValuesNullable("a", []int64{3, 1, 0, 1, 2}, []bool{true, true, false, true, true}),
		ursus.Values("b", []int64{5, 6, 7, 8, 9}),
		ursus.ValuesNullable("p", []bool{true, false, true, false, true}, []bool{true, true, true, false, true}))
}

// TestSelectionByPosition is F2 of v0.6.6-scope.md (step 267): Nth, and First and
// Last as selectors. Polars: nth(1) a; nth(-1) p; nth(0, 2) g and b; nth(1) * 2 [6,
// 2, None, 2, 4]; first() g; last() p; first("a") [3].
func TestSelectionByPosition(t *testing.T) {
	lf := argFixture()
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"nth", lf.Select(ursus.Nth(1)), "a\n3\n1\n\n1\n2\n"},
		{"from the end", lf.Select(ursus.Nth(-1)), "p\ntrue\nfalse\ntrue\n\ntrue\n"},
		{"two", lf.Select(ursus.Nth(0, 2)), "g,b\n1,5\n1,6\n2,7\n2,8\n1,9\n"},
		{"in an expression", lf.Select(ursus.Nth(1).Mul(2)), "a\n6\n2\n\n2\n4\n"},
		{"first", lf.Select(ursus.First()), "g\n1\n1\n2\n2\n1\n"},
		{"last", lf.Select(ursus.Last()), "p\ntrue\nfalse\ntrue\n\ntrue\n"},
		{"a column's first", lf.Select(ursus.First("a")), "a\n3\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	if _, err := lf.Select(ursus.Nth(9)).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "nth(9) of a frame of 4 columns") {
		t.Errorf("past the end: %v", err)
	}
}

// TestIndexFunctions: arg_where, arg_true, arg_sort, arg_sort_by and arg_unique, a
// UInt32 column of their own length, and in an aggregation a list a group of
// positions within it. Every answer is Polars 1.44.1's.
func TestIndexFunctions(t *testing.T) {
	c := ursus.Col
	lf := argFixture()
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"arg_where", lf.Select(ursus.ArgWhere(c("p"))), "p\n0\n2\n4\n"},
		{"arg_where of a comparison", lf.Select(ursus.ArgWhere(c("b").Gt(6))), "b\n2\n3\n4\n"},
		{"arg_sort", lf.Select(c("a").ArgSort(false, false)), "a\n2\n1\n3\n4\n0\n"},
		{"descending, ties in order", lf.Select(c("a").ArgSort(true, false)), "a\n2\n0\n4\n1\n3\n"},
		{"nulls last", lf.Select(c("a").ArgSort(false, true)), "a\n1\n3\n4\n0\n2\n"},
		{"descending, nulls last", lf.Select(c("a").ArgSort(true, true)), "a\n0\n4\n1\n3\n2\n"},
		{"arg_sort_by", lf.Select(ursus.ArgSortBy(ursus.Asc(c("a")), ursus.Asc(c("b")))), "a\n2\n1\n3\n4\n0\n"},
		{"by a descending second key", lf.Select(ursus.ArgSortBy(ursus.Asc(c("a")), ursus.Desc(c("b")))), "a\n2\n3\n1\n4\n0\n"},
		{"nulls last by two keys", lf.Select(ursus.ArgSortBy(ursus.Desc(c("g")).NullsLast(), ursus.Asc(c("a")).NullsLast())),
			"g\n3\n2\n1\n4\n0\n"},
		{"arg_unique", lf.Select(c("a").ArgUnique()), "a\n0\n1\n2\n4\n"},
		{"arg_unique of a key", lf.Select(c("g").ArgUnique()), "g\n0\n2\n"},
		{"beside the frame", lf.WithColumns(c("a").ArgSort(false, false).Alias("o")).Select(c("o")), "o\n2\n1\n3\n4\n0\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}

	// In an aggregation. Polars: arg_sort [[1, 2, 0], [0, 1]]; arg_true [[0, 2], [0]];
	// arg_where [[2], [0, 1]]; arg_unique [[0, 1, 2], [0, 1]]; arg_sort descending
	// [[0, 2, 1], [0, 1]]. arg_sort_by of one key is
	// its arg_sort, where Polars 1.44.1 answers [[1, 1, 0], [2, 0]].
	df, err := lf.GroupBy(c("g")).Agg(c("a").ArgSort(false, false).Alias("sort"), c("p").ArgTrue().Alias("true"),
		ursus.ArgWhere(c("b").Gt(6)).Alias("where"), c("a").ArgUnique().Alias("unique"),
		ursus.ArgSortBy(ursus.Asc(c("a"))).Alias("by"), c("a").ArgSort(true, false).Alias("desc")).
		Sort(ursus.Asc(c("g"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[1, 2, 0]", "[0, 1]", "[0, 2]", "[0]", "[2]", "[0, 1, 2]", "[0, 2, 1]"} {
		if !strings.Contains(df.String(), want) {
			t.Errorf("the lists lack Polars' %s:\n%s", want, df)
		}
	}
	if s := df.Schema().String(); strings.Count(s, "List(Uint32)") != 6 {
		t.Errorf("each is a List(UInt32): %s", s)
	}

	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"two keys in an aggregation":    {lf.GroupBy(c("g")).Agg(ursus.ArgSortBy(ursus.Asc(c("a")), ursus.Asc(c("b")))), "by 2 keys"},
		"arg_true of integers":          {lf.Select(c("a").ArgTrue()), "must be Boolean, got Int64\n  predicate: col(\"a\")"},
		"arg_true of integers, a group": {lf.GroupBy(c("g")).Agg(c("a").ArgTrue()), "takes Booleans"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
