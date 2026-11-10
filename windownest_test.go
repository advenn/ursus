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
// computes them. Their answers are Polars 2.1.0's since step 500, recorded in the
// windownest group of testdata/oracle and spelled in oracle_windownest_test.go, which
// took this file's tables. What stays asserts the refusals and the stream past a
// memory limit.
func TestNestedWindowsAndAggregates(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(strs("g", "a", "a", "b", "a", "b", "b"), i64s("h", 1, 1, 1, 2, 2, 2),
		f64s("x", 1.0, 4.0, 2.0, nil, 8.0, 3.0), i64s("t", 3, 1, 2, 6, 5, 4))
	x := c("x")
	byT := func(keys ...string) ursus.WindowSpec {
		s := ursus.WindowSpec{OrderBy: []ursus.SortKey{ursus.Asc(c("t"))}}
		for _, k := range keys {
			s.PartitionBy = append(s.PartitionBy, c(k))
		}
		return s
	}

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
