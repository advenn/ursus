package ursus_test

// Step 387: a window sink computes its windows at once, a thread each, where it
// computed them one after another. Each answer is the window's computed alone: rolling
// windows by an index, sorted and not, with nulls in the values and the index;
// cumulative ones, a rank, a shift and an aggregate over two partitionings. A window
// that fails fails the query, the others running beside it.

import (
	"math"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

func TestASinksWindowsAnswerTogetherAsAlone(t *testing.T) {
	const n = 3000
	g, h, tk := make([]int64, n), make([]int64, n), make([]int64, n)
	x, xOK := make([]int64, n), make([]bool, n)
	f, fOK := make([]float64, n), make([]bool, n)
	u, uOK := make([]int64, n), make([]bool, n)
	v := make([]int64, n)
	for i := range n {
		r := uint64(i) * 0x9E3779B97F4A7C15 >> 17
		g[i], h[i], tk[i] = int64(r%7), int64(r%3), int64(i/2)
		x[i], xOK[i] = int64(r%1000)-500, r%13 != 0
		f[i], fOK[i] = float64(r%97)/3, r%11 != 0
		u[i], uOK[i] = int64(r%5000), r%17 != 0 // an index out of order, with nulls
		v[i] = int64(r % 4000)                  // and one without
	}
	d := make([]time.Duration, n) // a running sum past a Duration's range
	for i := range d {
		d[i] = time.Duration(math.MaxInt64 / 100)
	}
	src := ursus.Frame(ursus.Values("g", g), ursus.Values("h", h), ursus.Values("t", tk),
		ursus.ValuesNullable("x", x, xOK), ursus.ValuesNullable("f", f, fOK), ursus.ValuesNullable("u", u, uOK),
		ursus.Values("v", v), ursus.Values("d", d))
	c := ursus.Col
	roll := func(e ursus.Expr, by string, period string) ursus.Expr {
		return e.Rolling(c(by), ursus.RollingOptions{Period: ursus.Every(period)})
	}
	windows := []ursus.Expr{
		roll(c("x").Sum(), "t", "10i").Alias("sum_t"),
		roll(c("f").Mean(), "t", "25i").Alias("mean_t"),
		roll(c("f").Max(), "t", "7i").Alias("max_t"),
		roll(c("x").Std(1), "t", "40i").Alias("std_t"),
		roll(c("x").Sum(), "u", "300i").Alias("sum_u"),
		roll(c("f").Mean(), "v", "200i").Alias("mean_v"),
		c("x").CumSum(false).Over(c("g")).Alias("cum_g"),
		c("f").Rank(ursus.RankAverage, false).Over(c("h")).Alias("rank_h"),
		c("x").Shift(1).Over(c("g")).Alias("shift_g"),
		c("x").Sum().Over(c("h")).Alias("sum_h"),
	}
	together, err := src.WithColumns(windows...).Collect(t.Context(), ursus.WithThreads(4), ursus.WithBatchSize(211))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range windows {
		alone, err := src.WithColumns(w).Collect(t.Context(), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(err)
		}
		name := alone.Columns()[len(alone.Columns())-1]
		got, err := together.Select(t.Context(), c(name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := alone.Select(t.Context(), c(name))
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}

	// A running sum that leaves a Duration's range fails the query; the windows
	// beside it run, and their answers go with it.
	_, err = src.WithColumns(append(windows,
		c("d").CumSum(false).Over(c("g")).Alias("d_cum"))...).Collect(t.Context(), ursus.WithThreads(4))
	if err == nil {
		t.Fatal("a Duration's running sum past its range answered")
	}
}
