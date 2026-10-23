package ursus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestAggregationLists is step 331: a non-aggregate in an aggregation is each group's
// values as a list, as Polars answers it, a window in it computed a group at a time.
// Every answer is Polars 1.44.1's.
func TestAggregationLists(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2, 2}), i64s("x", 1, 2, 3, nil, 5),
		ursus.Values("y", []int64{5, 4, 3, 2, 1}), ursus.Values("h", []int64{1, 1, 1, 1, 1}))
	agg := func(t *testing.T, e ursus.Expr) (string, []string) {
		t.Helper()
		got, err := lf.GroupBy(c("g")).Agg(e).Sort(ursus.Asc(c("g"))).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f := got.Schema().Field(1)
		rows, err := got.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = fmt.Sprint(r[f.Name])
		}
		return f.Name + " " + f.Type.String(), out
	}
	for _, tc := range []struct {
		name  string
		e     ursus.Expr
		field string
		want  []string
	}{
		{"a column", c("x"), "x List(Int64)", []string{"[1 2]", "[3 <nil> 5]"}},
		{"arithmetic", c("x").Mul(2), "x List(Int64)", []string{"[2 4]", "[6 <nil> 10]"}},
		{"two columns", c("x").Add(c("y")), "x List(Int64)", []string{"[6 6]", "[6 <nil> 6]"}},
		{"a cast", c("x").Cast(ursus.Float64), "x List(Float64)", []string{"[1 2]", "[3 <nil> 5]"}},
		{"a conditional", ursus.When(c("y").Gt(2)).Then(c("x")).Otherwise(0), "x List(Int64)",
			[]string{"[1 2]", "[3 0 0]"}},
		{"an alias", c("x").Mul(2).Alias("z"), "z List(Int64)", []string{"[2 4]", "[6 <nil> 10]"}},
		{"a name function", c("x").Mul(2).Name().Suffix("_2"), "x_2 List(Int64)", []string{"[2 4]", "[6 <nil> 10]"}},
		{"a filter", c("x").Filter(c("y").Gt(2)), "x List(Int64)", []string{"[1 2]", "[3]"}},
		// Windows, a group at a time.
		// A sum of integers is an Int128 in ursus, cumulative or under Over (§9).
		{"a cumulative sum", c("x").CumSum(false), "x List(Int128)", []string{"[1 3]", "[3 <nil> 8]"}},
		{"a shift", c("x").Shift(1), "x List(Int64)", []string{"[<nil> 1]", "[<nil> 3 <nil>]"}},
		{"a diff", c("x").Diff(1), "x List(Int64)", []string{"[<nil> 1]", "[<nil> <nil> <nil>]"}},
		{"an interpolation", c("x").Interpolate(), "x List(Float64)", []string{"[1 2]", "[3 4 5]"}},
		{"a window under arithmetic", c("x").CumSum(false).Mul(10), "x List(Int128)", []string{"[10 30]", "[30 <nil> 80]"}},
		// An Over's partition within the group: every y differs, so each row's own x.
		// Polars sums a null to 0 where ursus's sum of nulls is null.
		{"an Over", c("x").Sum().Over(c("y")), "x List(Int128)", []string{"[1 2]", "[3 <nil> 5]"}},
		// One h across the groups: its Over within each group.
		{"an Over spanning the groups", c("x").Sum().Over(c("h")), "x List(Int128)", []string{"[3 3]", "[8 8 8]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field, got := agg(t, tc.e)
			if field != tc.field || strings.Join(got, " | ") != strings.Join(tc.want, " | ") {
				t.Errorf("%s %q, want %s %q", field, got, tc.field, tc.want)
			}
		})
	}

	// An aggregate stays one, and a literal a scalar, beside a list.
	got, err := lf.GroupBy(c("g")).Agg(c("x").Sum(), c("y").Alias("ys"), ursus.Lit(int64(1)).Alias("one")).
		Sort(ursus.Asc(c("g"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Schema().String(); !strings.Contains(s, "x: Int128") || !strings.Contains(s, "ys: List(Int64)") ||
		!strings.Contains(s, "one: Int64") {
		t.Errorf("an aggregate, a list and a literal: %s", s)
	}

	// A window inside an aggregate, a group at a time: each group's greatest running
	// total, Polars' [3, 8].
	got, err = lf.GroupBy(c("g")).Agg(c("x").CumSum(false).Max()).Sort(ursus.Asc(c("g"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := got.ToMaps(); len(rows) != 2 || fmt.Sprint(rows[0]["x"], rows[1]["x"]) != "3 8" {
		t.Errorf("a window inside an aggregate: %v", rows)
	}

	// A mix of an aggregate and a row's value, each row's value less its group's
	// mean, a list a group: Polars' [-0.5, 0.5] and [-1.0, None, 1.0] (step 336).
	got, err = lf.GroupBy(c("g")).Agg(c("x").Sub(c("x").Mean())).Sort(ursus.Asc(c("g"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := got.ToMaps(); len(rows) != 2 || fmt.Sprint(rows[0]["x"], rows[1]["x"]) != "[-0.5 0.5] [-1 <nil> 1]" {
		t.Errorf("a mix: %v", rows)
	}
}
