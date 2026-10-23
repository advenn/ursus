package ursus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestNestedWindowsAndAggregates is step 336: a window inside a window, and an
// aggregate inside an aggregate's input, computed beneath it in its scope, as Polars
// computes them. Every answer is Polars 1.44.1's, but one sum of only a null, which
// is null here and 0 there (feature-gaps §9).
func TestNestedWindowsAndAggregates(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(strs("g", "a", "a", "b", "a", "b", "b"), i64s("h", 1, 1, 1, 2, 2, 2),
		f64s("x", 1.0, 4.0, 2.0, nil, 8.0, 3.0), i64s("t", 3, 1, 2, 6, 5, 4))
	x := c("x")
	text := func(df *ursus.DataFrame, name string) string {
		rows, err := df.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(rows))
		for i, r := range rows {
			vals[i] = r[name]
		}
		return fmt.Sprint(vals)
	}
	byT := func(keys ...string) ursus.WindowSpec {
		s := ursus.WindowSpec{OrderBy: []ursus.SortKey{ursus.Asc(c("t"))}}
		for _, k := range keys {
			s.PartitionBy = append(s.PartitionBy, c(k))
		}
		return s
	}

	t.Run("a window inside a window", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			e    ursus.Expr
			want string
		}{
			{"shifted, then summed", x.Shift(1).CumSum(false), "[<nil> 1 5 7 <nil> 15]"},
			{"summed, then shifted", x.CumSum(false).Shift(1), "[<nil> 1 5 7 <nil> 15]"},
			{"within g", x.Shift(1).CumSum(false).Over(c("g")), "[<nil> 1 <nil> 5 2 10]"},
			{"a diff's within g", x.Diff(1).CumSum(false).Over(c("g")), "[<nil> 3 <nil> <nil> 6 1]"},
			{"an aggregate of a shift within g", x.Shift(1).Sum().Over(c("g")), "[5 5 10 5 10 10]"},
			{"within g, by t", x.Shift(1).CumSum(false).OverWith(byT("g")), "[4 <nil> <nil> 5 5 2]"},
			{"a window over g within h", x.Shift(1).Over(c("g")).CumSum(false).Over(c("h")),
				"[<nil> 1 <nil> <nil> <nil> 8]"},
			{"a window over g within h, by t", x.Shift(1).Over(c("g")).CumSum(false).OverWith(byT("h")),
				"[4 <nil> <nil> <nil> 3 <nil>]"},
			{"a window over g by t within h", x.Shift(1).OverWith(byT("g")).CumSum(false).Over(c("h")),
				"[4 <nil> <nil> <nil> 3 <nil>]"},
			{"a shift within h, by t", x.Shift(1).CumSum(false).OverWith(byT("h")), "[6 <nil> 4 11 3 <nil>]"},
			{"a window over g, then over the frame", x.Shift(1).Over(c("g")).CumSum(false),
				"[<nil> 1 <nil> 5 7 15]"},
			{"a sum over g, then over the frame", x.Sum().Over(c("g")).CumSum(false), "[5 10 23 28 41 54]"},
			// Polars sums h = 2's g = a, only a null, to 0, so its row is 0 there.
			{"a sum over g within h", x.Sum().Over(c("g")).CumSum(false).Over(c("h")), "[5 10 12 <nil> 11 22]"},
			{"a mean of ranks within g", x.Rank(ursus.RankAverage, false).Mean().Over(c("g")),
				"[1.5 1.5 2 1.5 2 2]"},
		} {
			got, err := lf.Select(tc.e.Alias("r")).Collect(ctx)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if s := text(got, "r"); s != tc.want {
				t.Errorf("%s: %s, want %s", tc.name, s, tc.want)
			}
		}
	})

	t.Run("a sum over g of a sum over g", func(t *testing.T) {
		// Each group's sum times its rows, Polars' [21, 60, 21, 60, 21, 0]: c's
		// sum of only a null is null here.
		got := collect(t, wf().Select(c("v").Sum().Over(c("g")).Sum().Over(c("g")).Alias("r")))
		if s := text(got, "r"); s != "[21 60 21 60 21 <nil>]" {
			t.Errorf("%s", s)
		}
	})

	t.Run("an aggregate inside a window", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			e    ursus.Expr
			want string
		}{
			{"a mean in a running sum within g", x.Sub(x.Mean()).CumSum(false).Over(c("g")),
				"[-1.5 0 -2.333333333333333 <nil> 1.333333333333334 8.881784197001252e-16]"},
			// The mean takes none of the order: its answer does not depend on it.
			{"a mean in a running sum within g, by t", x.Sub(x.Mean()).CumSum(false).OverWith(byT("g")),
				"[0 1.5 -2.333333333333333 <nil> 8.881784197001252e-16 -3.666666666666666]"},
		} {
			got, err := lf.Select(tc.e.Alias("r")).Collect(ctx)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if s := text(got, "r"); s != tc.want {
				t.Errorf("%s: %s, want %s", tc.name, s, tc.want)
			}
		}
	})

	t.Run("an aggregate inside an aggregate", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			e           ursus.Expr
			frame, byG  string
			withColumns string
		}{
			{"a sum of distances from the mean", x.Sub(x.Mean()).Abs().Sum(), "[9.6]", "[3 7.333333333333333]",
				"[9.6 9.6 9.6 9.6 9.6 9.6]"},
			{"the greatest share of the sum", x.Div(x.Sum()).Max(), "[0.4444444444444444]",
				"[0.8 0.6153846153846154]", ""},
			{"a count of values over the mean", x.Filter(x.Gt(x.Mean())).Count(), "[2]", "[1 1]", ""},
			{"a mean of a shift", x.Shift(1).Mean(), "[3.75]", "[2.5 5]", ""},
			{"a mean of a sum", x.Sum().Mean(), "[18]", "[5 13]", ""},
			{"a variance by hand", x.Sub(x.Mean()).Pow(2).Mean(), "[5.840000000000001]",
				"[2.25 6.888888888888889]", ""},
			{"the greatest distance over the mean", x.Sub(x.Mean()).Max(), "[4.4]", "[1.5 3.666666666666667]", ""},
			// A null sorts first, as Polars sorts one: g = a's least is its null.
			{"the t of the least distance from the mean", c("t").SortBy(x.Sub(x.Mean())).First(), "[6]", "[6 2]", ""},
		} {
			got, err := lf.Select(tc.e.Alias("r")).Collect(ctx)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if s := text(got, "r"); s != tc.frame {
				t.Errorf("%s over the frame: %s, want %s", tc.name, s, tc.frame)
			}
			got, err = lf.GroupBy(c("g")).Agg(tc.e.Alias("r")).Sort(ursus.Asc(c("g"))).Collect(ctx)
			if err != nil {
				t.Fatalf("%s by g: %v", tc.name, err)
			}
			if s := text(got, "r"); s != tc.byG {
				t.Errorf("%s by g: %s, want %s", tc.name, s, tc.byG)
			}
			if tc.withColumns == "" {
				continue
			}
			for _, q := range []*ursus.LazyFrame{lf.WithColumns(tc.e.Alias("r")), lf.Select(x, tc.e.Alias("r"))} {
				got, err = q.Collect(ctx)
				if err != nil {
					t.Fatalf("%s broadcast: %v", tc.name, err)
				}
				if s := text(got, "r"); s != tc.withColumns {
					t.Errorf("%s broadcast: %s, want %s", tc.name, s, tc.withColumns)
				}
			}
		}
		// A mix of an aggregate and a row's value, a list a group.
		for _, tc := range []struct {
			e    ursus.Expr
			want string
		}{
			{x.Sub(x.Mean()), "[[-1.5 1.5 <nil>] [-2.333333333333333 3.666666666666667 -1.333333333333333]]"},
			{x.Sum().Add(x), "[[6 9 <nil>] [15 21 16]]"},
		} {
			got, err := lf.GroupBy(c("g")).Agg(tc.e.Alias("r")).Sort(ursus.Asc(c("g"))).Collect(ctx)
			if err != nil {
				t.Fatalf("%s: %v", tc.e, err)
			}
			if s := text(got, "r"); s != tc.want {
				t.Errorf("%s: %s, want %s", tc.e, s, tc.want)
			}
		}
		got := collect(t, lf.WithColumns(x.Sub(x.Mean()).Abs().Sum().Over(c("g")).Alias("r")))
		if s := text(got, "r"); s != "[3 3 7.333333333333333 3 7.333333333333333 7.333333333333333]" {
			t.Errorf("over g: %s", s)
		}
	})

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			e    ursus.Expr
			want string
		}{
			// Polars answers these, an aggregate whose answer depends on the order
			// under an order; ursus refuses an ordering over an aggregate.
			{"a first in a running sum by t", x.Sub(x.First()).CumSum(false).OverWith(byT("g")),
				"an ordering is not yet honoured over an aggregate"},
			{"a first over g within h by t", x.First().Over(c("g")).CumSum(false).OverWith(byT("h")),
				"an ordering is not yet honoured over an aggregate"},
			{"a window's key a window", x.Sum().Over(x.CumSum(false)), "may not hold a window"},
		} {
			_, err := lf.Select(tc.e.Alias("r")).Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "optimize") {
				t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("partitioned by an integer, a null beside a zero", func(t *testing.T) {
		// Partitions of one integer key are numbered without encoding it since step
		// 336: a null key is its own, not 0's. Polars' answer.
		k := ursus.Frame(ursus.ValuesNullable("k", []int32{0, 0, 0, 0, 5}, []bool{true, false, true, false, true}),
			i64s("v", 1, 2, 3, 4, 5))
		got := collect(t, k.Select(c("v").Sum().Over(c("k")).Alias("s"), c("v").CumSum(false).Over(c("k")).Alias("c")))
		if s, cs := text(got, "s"), text(got, "c"); s != "[4 6 4 6 5]" || cs != "[1 2 4 6 5]" {
			t.Errorf("sums %s, running sums %s", s, cs)
		}
	})

	t.Run("streamed past a memory limit", func(t *testing.T) {
		const n = 3000
		g, v := make([]string, n), make([]float64, n)
		for i := range n {
			g[i], v[i] = fmt.Sprint("k", i%7), float64(i%31)
		}
		big := ursus.Frame(ursus.Values("g", g), ursus.Values("x", v))
		for _, e := range []ursus.Expr{x.Shift(1).CumSum(false).Over(c("g")), x.Sub(x.Mean()).Abs().Sum().Over(c("g")),
			x.Shift(1).Over(c("g")).CumSum(false)} {
			q := big.Select(c("g"), e.Alias("r"))
			want := collect(t, q)
			got, stats, err := spillCollect(t, q, 16<<10, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if stats.Spills == 0 {
				t.Errorf("%s: no spill", e)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
	})
}
