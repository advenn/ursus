package ursus_test

// Step 382: a filter of a group-by's keys moves below it, rewritten through the keys'
// definitions, and one of a window's partition keys below the window; a filter of an
// aggregate, of a key holding a float, of the frame's window or of a partition not
// every window shares stays above. Each answer is the one with predicate pushdown
// off, on one thread and four, and each plan puts the filter where it may go.

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// keyPushFrame is n rows of an integer key with nulls, a text key with nulls, a
// float key holding −0, +0 and NaN, a second integer key, and values. A text that is
// no number is a group of one row.
func keyPushFrame(n int) *ursus.LazyFrame {
	k, kOK := make([]int64, n), make([]bool, n)
	j := make([]int64, n)
	s, sOK := make([]string, n), make([]bool, n)
	f := make([]float64, n)
	v := make([]int64, n)
	x := make([]float64, n)
	floats := []float64{0, math.Copysign(0, -1), math.NaN(), 1.5, -2}
	for i := range n {
		h := uint64(i) * 0x9E3779B97F4A7C15 >> 7
		k[i], kOK[i] = int64(h%60)-10, i%23 != 4
		j[i] = int64(h % 7)
		s[i], sOK[i] = fmt.Sprint(h%40), i%19 != 3
		if h%97 == 0 {
			s[i] = "x" + fmt.Sprint(i) // no number, a group of one row
		}
		f[i] = floats[h%5]
		v[i] = int64(h % 1000)
		x[i] = float64(h%997) / 7
	}
	return ursus.Frame(ursus.ValuesNullable("k", k, kOK), ursus.Values("j", j),
		ursus.ValuesNullable("s", s, sOK), ursus.Values("f", f), ursus.Values("v", v), ursus.Values("x", x))
}

func TestAFilterOfKeysMovesBelowAGroupByOrAWindow(t *testing.T) {
	c := ursus.Col
	src := keyPushFrame(3000)
	aggs := []ursus.Expr{c("v").Sum().Alias("sv"), ursus.Len().Alias("n"), c("x").Mean().Alias("mx"),
		c("v").First().Alias("first")}
	gb := func(keys ...ursus.Expr) *ursus.LazyFrame { return src.GroupBy(keys...).Agg(aggs...) }
	ordered := func(keys ...ursus.Expr) *ursus.LazyFrame { return src.GroupBy(keys...).MaintainOrder().Agg(aggs...) }
	win := func(e ...ursus.Expr) *ursus.LazyFrame { return src.WithColumns(e...) }
	cases := []struct {
		name         string
		q            *ursus.LazyFrame
		node         string // the plan node the filter is above or below
		below, above bool   // whether a filter is below the node, above it
		inOrder      bool   // the answer's row order is defined
	}{
		{"a key", gb(c("k")).Filter(c("k").Lt(int64(10))), "AGGREGATE", true, false, false},
		{"a key's null", gb(c("k")).Filter(c("k").IsNull()), "AGGREGATE", true, false, false},
		{"keys defined by expressions", gb(c("k").Mod(int64(7)).Alias("m"), c("s")).
			Filter(c("m").Lt(int64(3)), c("s").Str().StartsWith(ursus.Lit("1"))), "AGGREGATE", true, false, false},
		{"in first-appearance order", ordered(c("k"), c("j")).Filter(c("j").Eq(int64(2))), "AGGREGATE", true, false, true},
		{"an aggregate: HAVING", gb(c("k")).Filter(c("sv").Gt(int64(20000))), "AGGREGATE", false, true, false},
		{"an aggregate named as an input column", src.GroupBy(c("k")).Agg(c("v").Sum()).Filter(c("v").Gt(int64(20000))),
			"AGGREGATE", false, true, false},
		{"a key and an aggregate apart", gb(c("k")).Filter(c("k").Gt(int64(2)), c("sv").Gt(int64(20000))),
			"AGGREGATE", true, true, false},
		{"a key and an aggregate in one conjunct", gb(c("k")).Filter(c("k").Gt(int64(2)).Or(c("sv").Gt(int64(20000)))),
			"AGGREGATE", false, true, false},
		{"a float key, −0 and +0 told apart", gb(c("f")).Filter(c("f").Cast(ursus.String).Eq(ursus.Lit("0"))),
			"AGGREGATE", false, true, false},
		{"no key", src.GroupBy().Agg(aggs...).Filter(c("sv").Gt(int64(0))), "AGGREGATE", false, true, true},
		// Simplified to a LIMIT 0 before pushdown sees it, so no filter is left.
		{"no column read", gb(c("k")).Filter(ursus.Lit(false)), "AGGREGATE", false, false, false},
		{"a cast held behind HAVING", gb(c("s")).Filter(c("n").Gt(int64(3)), c("s").Cast(ursus.Int64).Gt(int64(5))),
			"AGGREGATE", false, true, false},

		{"a partition key", win(c("v").Sum().Over(c("k")).Alias("w")).Filter(c("k").Lt(int64(10))), "WINDOW", true, false, true},
		{"one of two partition keys", win(c("v").Sum().Over(c("k"), c("s")).Alias("w")).
			Filter(c("s").Eq(ursus.Lit("7"))), "WINDOW", true, false, true},
		{"ordered within its partition", win(c("v").CumSum(false).OverWith(ursus.WindowSpec{
			PartitionBy: []ursus.Expr{c("k")}, OrderBy: []ursus.SortKey{ursus.Asc(c("x"))}}).Alias("w")).
			Filter(c("k").Gt(int64(30))), "WINDOW", true, false, true},
		{"a key of every window", win(c("v").Sum().Over(c("k"), c("j")).Alias("w1"), c("x").Mean().Over(c("j")).Alias("w2")).
			Filter(c("j").Lt(int64(3))), "WINDOW", true, false, true},
		{"a key of one window of two", win(c("v").Sum().Over(c("k")).Alias("w1"), c("x").Mean().Over(c("j")).Alias("w2")).
			Filter(c("k").Lt(int64(10))), "WINDOW", false, true, true},
		{"a column the partition is computed from", win(c("v").Sum().Over(c("k").Mod(int64(7))).Alias("w")).
			Filter(c("k").Lt(int64(10))), "WINDOW", false, true, true},
		{"the frame's window", win(c("v").Sum().Alias("w")).Filter(c("k").Lt(int64(10))), "WINDOW", false, true, true},
		{"a partition key beside the frame's window", win(c("v").Sum().Over(c("k")).Alias("w1"), c("x").Mean().Alias("w2")).
			Filter(c("k").Lt(int64(10))), "WINDOW", false, true, true},
		{"a column no window partitions by", win(c("v").Sum().Over(c("k")).Alias("w")).Filter(c("v").Lt(int64(500))),
			"WINDOW", false, true, true},
		{"a float partition, −0 and +0 told apart", win(c("v").Sum().Over(c("f")).Alias("w")).
			Filter(c("f").Cast(ursus.String).Eq(ursus.Lit("0"))), "WINDOW", false, true, true},
	}
	off := ursus.DefaultOptFlags()
	off.PredicatePushdown = false
	for _, tc := range cases {
		plan, err := tc.q.Explain(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if below, above := filterAround(plan, tc.node); below != tc.below || above != tc.above {
			t.Errorf("%s: a filter below the %s %v, above it %v; want %v and %v\n%s",
				tc.name, tc.node, below, above, tc.below, tc.above, plan)
		}
		for _, threads := range []int{1, 4} {
			want, err := tc.q.Collect(t.Context(), ursus.WithThreads(threads), ursus.WithOptFlags(off))
			if err != nil {
				t.Fatalf("%s, pushdown off: %v", tc.name, err)
			}
			got, err := tc.q.Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(256), ursus.WithVerify())
			if err != nil {
				t.Fatalf("%s on %d threads: %v", tc.name, threads, err)
			}
			if tc.inOrder || threads == 1 {
				ursustest.AssertFrameEqual(t, got, want)
			} else {
				ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
			}
		}
	}
}

// filterAround reports whether a FILTER line of the plan is below its first line
// naming node, and whether one is above it.
func filterAround(plan, node string) (below, above bool) {
	at := -1
	for i, l := range strings.Split(plan, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case at < 0 && strings.HasPrefix(l, node):
			at = i
		case strings.HasPrefix(l, "FILTER"):
			if at < 0 {
				above = true
			} else {
				below = true
			}
		}
	}
	return below, above
}
