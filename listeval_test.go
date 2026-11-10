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

// List().Eval, Agg and Filter, §3 of v0.6.6-scope.md (step 268): every answer over
// evalFixture is Polars 2.1.0's, recorded in the listeval group of testdata/oracle
// and spelled in oracle_listeval_test.go since step 499, which took this file's hand
// table; what stays asserts the refusals.

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
