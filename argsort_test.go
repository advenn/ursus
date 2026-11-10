package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// F2 of v0.6.6-scope.md (step 267): Nth, First and Last as selectors, and the index
// functions arg_where, arg_true, arg_sort, arg_sort_by and arg_unique. Their answers
// are Polars 2.1.0's since step 500, recorded in the argsort group of testdata/oracle
// and spelled in oracle_argsort_test.go, which took this file's tables. What stays
// asserts the refusals.

func argFixture() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2, 1}),
		ursus.ValuesNullable("a", []int64{3, 1, 0, 1, 2}, []bool{true, true, false, true, true}),
		ursus.Values("b", []int64{5, 6, 7, 8, 9}),
		ursus.ValuesNullable("p", []bool{true, false, true, false, true}, []bool{true, true, true, false, true}))
}

func TestSelectionByPosition(t *testing.T) {
	lf := argFixture()
	if _, err := lf.Select(ursus.Nth(9)).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "nth(9) of a frame of 4 columns") {
		t.Errorf("past the end: %v", err)
	}
}

func TestIndexFunctions(t *testing.T) {
	c := ursus.Col
	lf := argFixture()
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
