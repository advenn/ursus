package ursus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestALimitReachesThroughWhatKeepsRows: a Head or a Slice tells the scan or the
// sort beneath it how few rows it needs through a projection, a WithColumns, a row
// index, a union's inputs and the left input of a left join that keeps its order
// (step 371), and through nothing that changes the rows: a filter, a window, a
// group-by, another join. Each answers as it does with limit pushdown off, on one
// thread and on four.
func TestALimitReachesThroughWhatKeepsRows(t *testing.T) {
	c := ursus.Col
	ctx := t.Context()
	const n = 3000
	x := make([]int64, n)
	g := make([]int64, n)
	valid := make([]bool, n)
	for i := range n {
		x[i] = int64((i * 7919) % 500) // ties
		g[i] = int64(i % 13)
		valid[i] = i%17 != 5
	}
	lf := ursus.Frame(ursus.ValuesNullable("x", x, valid), ursus.Values("g", g), ursus.Values("i", spillInts(0, n)))
	right := ursus.Frame(ursus.Values("g", []int64{0, 1, 2, 2, 3}), ursus.Values("r", []int64{10, 11, 12, 13, 14}))

	cases := []struct {
		name  string
		q     *ursus.LazyFrame
		hints []string // in the plan, each once at least
		none  bool     // no hint at all
		times int      // the first hint's times, where more than once
	}{
		{"a head over a projection", lf.Select(c("x").Add(1), c("i")).Head(7),
			[]string{"max rows: 7"}, false, 0},
		{"a head over a WithColumns and a row index",
			lf.WithColumns(c("x").Mul(2).Alias("y")).WithRowIndex("r", 0).Head(9),
			[]string{"max rows: 9"}, false, 0},
		{"a head over a projected sort", lf.Sort(ursus.Asc(c("x")), ursus.Desc(c("i"))).Select(c("x"), c("i")).Head(11),
			[]string{"top 11"}, false, 0},
		{"a slice", lf.Select(c("i"), c("x")).Slice(100, 20),
			[]string{"max rows: 120"}, false, 0},
		{"a slice of a sort", lf.Sort(ursus.Desc(c("x"))).Slice(30, 15),
			[]string{"top 45"}, false, 0},
		{"a head over a union", ursus.Concat([]*ursus.LazyFrame{lf.Select(c("i")), lf.Select(c("i").Add(n))}).Head(5),
			[]string{"max rows: 5"}, false, 2},
		{"a head over an ordered left join",
			lf.Join(right, ursus.JoinOn(c("g")), ursus.JoinHow(ursus.JoinLeft),
				ursus.JoinMaintainOrder(ursus.JoinOrderLeft)).Head(8),
			[]string{"max rows: 8"}, false, 0},
		{"a head over two heads", lf.Head(50).Select(c("i")).Head(6),
			[]string{"max rows: 6"}, false, 0},
		{"a filter stops it", lf.Filter(c("x").Gt(100)).Select(c("i")).Head(5), nil, true, 0},
		{"a window stops it", lf.Select(c("x").CumSum(false)).Head(5), nil, true, 0},
		{"a group-by stops it", lf.GroupBy(c("g")).Agg(c("x").Sum()).Head(3), nil, true, 0},
		{"a join of no order stops it", lf.Join(right, ursus.JoinOn(c("g")), ursus.JoinHow(ursus.JoinLeft)).Head(8), nil, true, 0},
		{"an inner join stops it", lf.Join(right, ursus.JoinOn(c("g")), ursus.JoinMaintainOrder(ursus.JoinOrderLeft)).Head(8), nil, true, 0},
		{"a slice to the end does not hint", lf.Select(c("i")).Slice(2990, -1), nil, true, 0},
	}
	off := ursus.DefaultOptFlags()
	off.LimitPushdown = false
	for _, tc := range cases {
		plan, err := tc.q.Explain(ctx)
		if err != nil {
			t.Fatal(tc.name, err)
		}
		for _, h := range tc.hints {
			if !strings.Contains(plan, h) {
				t.Errorf("%s: no %q:\n%s", tc.name, h, plan)
			}
		}
		if tc.times > 0 && strings.Count(plan, tc.hints[0]) != tc.times {
			t.Errorf("%s: %q %d times, want %d:\n%s", tc.name, tc.hints[0], strings.Count(plan, tc.hints[0]), tc.times, plan)
		}
		if tc.none && (strings.Contains(plan, "max rows:") || strings.Contains(plan, " top ")) {
			t.Errorf("%s: a hint:\n%s", tc.name, plan)
		}
		want, err := tc.q.Collect(ctx, ursus.WithOptFlags(off), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(tc.name, err)
		}
		for _, threads := range []int{1, 4} {
			for _, size := range []int{0, 4, 64} {
				opts := []ursus.CollectOption{ursus.WithThreads(threads)}
				if size > 0 {
					opts = append(opts, ursus.WithBatchSize(size))
				}
				got, err := tc.q.Collect(ctx, opts...)
				if err != nil {
					t.Fatal(tc.name, err)
				}
				what := fmt.Sprintf("%s on %d threads in batches of %d", tc.name, threads, size)
				if tc.name == "a group-by stops it" {
					continue // its groups come in no order
				}
				sameFrame(t, what, got, want)
			}
		}
	}
}
