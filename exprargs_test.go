package ursus_test

import (
	"bytes"
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
// row's own list, and list.get and list.contains with a per-row index and value. The
// answers are Polars 2.1.0's on the same frame, recorded in the exprargs group of
// testdata/oracle and spelled in oracle_exprargs_test.go since step 497, which took
// this file's pasted values. What stays is the same question across batch sizes, and
// a needle that is a float against a list of integers, which Polars 2.1 refuses and
// ursus meets as == meets them: the same as the integer needle.
func TestListArgumentsAreExpressions(t *testing.T) {
	c := ursus.Col
	lf := exprArgsFixture(t)
	queries := map[string]*ursus.LazyFrame{
		"plain": lf.Select(c("a").IsIn(c("tags")).Alias("in"),
			c("tags").List().Get(c("i")).Alias("get"),
			c("tags").List().Contains(c("v")).Alias("has")),
		"cast": lf.Select(c("a").Cast(ursus.Int8).IsIn(c("tags")).Alias("in"),
			c("tags").List().Get(c("i").Cast(ursus.Int8)).Alias("get"),
			c("tags").List().Contains(c("v").Cast(ursus.Float64)).Alias("has")),
		"strings": lf.Select(c("a").Cast(ursus.String).IsIn(c("tags").Cast(ursus.List(ursus.String))).Alias("in"),
			c("tags").Cast(ursus.List(ursus.String)).List().Get(c("i")).Alias("get"),
			c("tags").Cast(ursus.List(ursus.String)).List().Contains(c("v").Cast(ursus.String)).Alias("has")),
	}
	csv := func(q *ursus.LazyFrame, size int) string {
		var out bytes.Buffer
		if err := q.WriteCSV(t.Context(), &out, ursus.WithBatchSize(size)); err != nil {
			t.Fatalf("batch %d: %v", size, err)
		}
		return out.String()
	}
	for name, q := range queries {
		whole := csv(q, 8192)
		for _, size := range []int{1, 3} {
			if got := csv(q, size); got != whole {
				t.Errorf("%s, batch %d:\ngot  %q\nwant %q, the answer in one batch", name, size, got, whole)
			}
		}
	}
	// The float needle meets the integer list as == meets them.
	if got, want := csv(lf.Select(c("tags").List().Contains(c("v").Cast(ursus.Float64)).Alias("has")), 8192),
		csv(lf.Select(c("tags").List().Contains(c("v")).Alias("has")), 8192); got != want {
		t.Errorf("a Float64 needle:\ngot  %q\nwant %q, the Int64 needle's", got, want)
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
