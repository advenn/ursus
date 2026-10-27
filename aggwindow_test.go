package ursus_test

// Step 386: a window function of one column in an Agg, Agg(Col("x").CumSum()), is
// the column's list a group with the function evaluated over each list, where it was
// a window partitioned by the group-by's keys beneath the aggregation. Its answer is
// the window's: the same function Over the keys, imploded a group, for every window
// function, of integers and floats with nulls, NaN and ties, reversed or not, on one
// thread and four. An Over within the group, a second column or an aggregate keeps
// the window beneath, and answers as before.

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

func TestAWindowInAnAggIsEvaluatedOverEachGroupsList(t *testing.T) {
	const n = 5000
	g, h := make([]int64, n), make([]int64, n)
	x, xOK := make([]int64, n), make([]bool, n)
	f, fOK := make([]float64, n), make([]bool, n)
	for i := range n {
		u := uint64(i) * 0x9E3779B97F4A7C15 >> 13
		g[i], h[i] = int64(u%40), int64(u%3)
		x[i], xOK[i] = int64(u%17)-8, u%11 != 0
		f[i], fOK[i] = float64(u%13)/4, u%7 != 0
		if u%29 == 0 {
			f[i] = math.NaN()
		}
	}
	src := ursus.Frame(ursus.Values("g", g), ursus.Values("h", h), ursus.ValuesNullable("x", x, xOK),
		ursus.ValuesNullable("f", f, fOK))
	c := ursus.Col
	cases := []struct {
		name   string
		e      func(col string) ursus.Expr
		listed bool // evaluated over the lists, not a window beneath
	}{
		{"cum_sum", func(s string) ursus.Expr { return c(s).CumSum(false) }, true},
		{"cum_sum reversed", func(s string) ursus.Expr { return c(s).CumSum(true) }, true},
		{"cum_max", func(s string) ursus.Expr { return c(s).CumMax(false) }, true},
		{"cum_min reversed", func(s string) ursus.Expr { return c(s).CumMin(true) }, true},
		{"cum_prod", func(s string) ursus.Expr { return c(s).Mod(int64(3)).Add(int64(1)).CumProd(false) }, true},
		{"cum_count", func(s string) ursus.Expr { return c(s).CumCount(false) }, true},
		{"shift", func(s string) ursus.Expr { return c(s).Shift(1) }, true},
		{"shift back two", func(s string) ursus.Expr { return c(s).Shift(-2) }, true},
		{"diff", func(s string) ursus.Expr { return c(s).Diff(1) }, true},
		{"rank, average", func(s string) ursus.Expr { return c(s).Rank(ursus.RankAverage, false) }, true},
		{"rank, dense descending", func(s string) ursus.Expr { return c(s).Rank(ursus.RankDense, true) }, true},
		{"arithmetic around a cum_sum", func(s string) ursus.Expr { return c(s).CumSum(false).Mul(int64(2)).Add(int64(1)) }, true},
		{"a cum_sum of a shift", func(s string) ursus.Expr { return c(s).Shift(1).CumSum(false) }, true},
		{"a conditional's cum_max", func(s string) ursus.Expr {
			return ursus.When(c(s).Gt(int64(0))).Then(c(s)).Otherwise(ursus.Lit(int64(0))).CumMax(false)
		}, true},
		{"an Over within the group", func(s string) ursus.Expr { return c(s).CumSum(false).Over(c("h")) }, false},
		{"an Over by itself within the group", func(s string) ursus.Expr { return c(s).CumSum(false).Over(c(s)) }, false},
		{"a second column", func(s string) ursus.Expr { return c(s).CumSum(false).Add(c("h")) }, false},
		{"an aggregate inside", func(s string) ursus.Expr { return c(s).Sub(c(s).Mean()).CumSum(false) }, false},
	}
	for _, col := range []string{"x", "f"} {
		for _, tc := range cases {
			name := fmt.Sprintf("%s of %s", tc.name, col)
			if col == "f" && strings.HasPrefix(tc.name, "cum_prod") {
				continue // a float's running product of these overflows to Inf alike; nothing to tell
			}
			e := tc.e(col)
			got := src.GroupBy(c("g")).MaintainOrder().Agg(e.Alias("w"))
			want := src.WithColumns(e.Over(c("g")).Alias("w")).GroupBy(c("g")).MaintainOrder().Agg(c("w"))
			plan, err := got.Explain(t.Context())
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if listed := strings.Contains(plan, "list.eval"); listed != tc.listed {
				t.Errorf("%s: evaluated over the lists %v, want %v\n%s", name, listed, tc.listed, plan)
			}
			w, err := want.Collect(t.Context(), ursus.WithThreads(1))
			if err != nil {
				t.Fatalf("%s, the window: %v", name, err)
			}
			for _, threads := range []int{1, 4} {
				out, err := got.Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(301))
				if err != nil {
					t.Fatalf("%s on %d threads: %v", name, threads, err)
				}
				ursustest.AssertFrameEqual(t, out, w)
			}
		}
	}
}
