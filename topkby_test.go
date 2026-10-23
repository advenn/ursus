package ursus_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestTopKBy is step 330: Polars' top_k_by and bottom_k_by, a List a group as TopK
// is. Every answer is Polars 1.44.1's but where by ties, which Polars' sort orders as
// it finds them and ursus by the value.
func TestTopKBy(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}), i64s("a", 3, 1, nil, 5, 4, 1),
		i64s("b", 1, 5, 2, nil, 3, 5), ursus.Values("c", []string{"p", "q", "r", "s", "t", "u"}))
	lists := func(t *testing.T, q *ursus.LazyFrame, name string) []string {
		t.Helper()
		got, err := q.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := got.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = fmt.Sprint(r[name])
		}
		return out
	}
	same := func(t *testing.T, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, " | ") != strings.Join(want, " | ") {
			t.Errorf("got %q, want %q", got, want)
		}
	}

	t.Run("a whole frame's", func(t *testing.T) {
		// q and u tie at 5: the greater value first, where Polars' sort gave q.
		same(t, lists(t, lf.Select(c("c").TopKBy(c("b"), 3)), "c"), "[u q t]")
		same(t, lists(t, lf.Select(c("c").BottomKBy(c("b"), 3)), "c"), "[p r t]")
		// A value's null is kept, and a null by comes last.
		same(t, lists(t, lf.Select(c("a").TopKBy(c("b"), 6)), "a"), "[1 1 4 <nil> 3 5]")
		same(t, lists(t, lf.Select(c("a").BottomKBy(c("b"), 6)), "a"), "[3 <nil> 4 1 1 5]")
		// By text, and with k past the rows.
		same(t, lists(t, lf.Select(c("a").TopKBy(c("c"), 2)), "a"), "[1 4]")
	})

	t.Run("a group's", func(t *testing.T) {
		agg := func(e ursus.Expr) *ursus.LazyFrame {
			return lf.GroupBy(c("g")).Agg(e.Alias("x")).Sort(ursus.Asc(c("g")))
		}
		same(t, lists(t, agg(c("c").TopKBy(c("b"), 2)), "x"), "[q r]", "[u t]")
		same(t, lists(t, agg(c("c").BottomKBy(c("b"), 2)), "x"), "[p r]", "[t u]")
		same(t, lists(t, agg(c("c").TopKBy(c("b"), 5)), "x"), "[q r p]", "[u t s]")
	})

	t.Run("orders", func(t *testing.T) {
		v := func(by *ursus.Column, e ursus.Expr) []string {
			return lists(t, ursus.Frame(ursus.Values("v", []int64{1, 2, 3}), by).Select(e.Alias("x")), "x")
		}
		// NaN is the greatest float, as Polars has it.
		same(t, v(f64s("b", 1.0, math.NaN(), 2.0), c("v").TopKBy(c("b"), 2)), "[2 3]")
		// true above false; the tie between 1 and 3 to the greater, where Polars gave 1.
		same(t, v(ursus.Values("b", []bool{true, false, true}), c("v").TopKBy(c("b"), 2)), "[3 1]")
		ties := ursus.Frame(ursus.Values("v", []int64{1, 2, 3, 4}), ursus.Values("b", []int64{1, 1, 1, 0}))
		same(t, lists(t, ties.Select(c("v").BottomKBy(c("b"), 3).Alias("x")), "x"), "[4 1 2]")
		same(t, lists(t, ties.Select(c("v").TopKBy(c("b"), 2).Alias("x")), "x"), "[3 2]")
		// A null value after the values it ties with.
		nullTie := ursus.Frame(i64s("v", nil, 5, 3), ursus.Values("b", []int64{1, 1, 0}))
		same(t, lists(t, nullTie.Select(c("v").TopKBy(c("b"), 2).Alias("x")), "x"), "[5 <nil>]")
		// A thousand rows over three bys: each tie to the greater, whatever the order
		// the rows arrive in, so the parallel group-by agrees with the order a sort
		// gives.
		n := 1000
		vs, bs, gs := make([]int64, n), make([]int64, n), make([]int64, n)
		for i := range n {
			vs[i], bs[i], gs[i] = int64(i*7919%1000), int64(i%3), int64(i%4)
		}
		big := ursus.Frame(ursus.Values("v", vs), ursus.Values("b", bs), ursus.Values("g", gs))
		got := lists(t, big.GroupBy(c("g")).Agg(c("v").TopKBy(c("b"), 4).Alias("x")).Sort(ursus.Asc(c("g"))), "x")
		want := lists(t, big.Sort(ursus.Desc(c("b")), ursus.Desc(c("v"))).GroupBy(c("g")).Agg(c("v").Head(4).Alias("x")).
			Sort(ursus.Asc(c("g"))), "x")
		same(t, got, want...)
	})

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			e    ursus.Expr
			want string
		}{
			{"k of 0", c("c").TopKBy(c("b"), 0), "k must be at least 1"},
			{"a list value", c("c").Str().Split(",").TopKBy(c("b"), 1), "top_k_by() is not defined for List"},
			{"a struct by", c("c").TopKBy(ursus.Struct(c("a"), c("b")), 1), "has no order"},
		} {
			if _, err := lf.Select(tc.e).Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.want)
			}
		}
	})
}
