package ursus_test

// TopK and BottomK inside a group-by (step 106).
//
// h2o gb8 — the two largest v3 per id6 — was answered with an ordinal rank over a
// partitioned window and a filter, the SQL formulation, and step 24 measured the
// window's sort at 92% of it. Polars answers it with top_k inside the group-by, a
// bounded accumulator, and these pin that ursus's does what the window did.

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

func TestTopKValues(t *testing.T) {
	c := ursus.Col
	day := func(d int) time.Time { return time.Date(2024, 1, d, 0, 0, 0, 0, time.UTC) }
	frame := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a", "a", "b", "b", "c", "c"}),
		ursus.ValuesNullable("i", []int64{5, 9, 1, 0, 7, 0, 0, 0}, []bool{true, true, true, false, true, false, false, false}),
		ursus.Values("f", []float64{1.5, math.NaN(), -2, 3, 0.5, 0.25, 1, 2}),
		ursus.Values("s", []string{"pear", "fig", "apple", "kiwi", "plum", "date", "lime", "yuzu"}),
		ursus.Values("d", []time.Time{day(3), day(1), day(9), day(4), day(2), day(8), day(5), day(6)}),
	).WithColumns(c("d").Cast(ursus.Date), c("i").Cast(ursus.Decimal(10, 2)).Alias("dec"))

	// Each want is the groups' lists, exploded: "group value" per element, in list
	// order, and "group ∅" for a null list.
	cases := []struct {
		name string
		agg  ursus.Expr
		want []string
	}{
		{"the two largest ints, nulls skipped", c("i").TopK(2), []string{"a 9", "a 5", "b 7", "c ∅"}},
		{"the two smallest ints", c("i").BottomK(2), []string{"a 1", "a 5", "b 7", "c ∅"}},
		{"NaN is the largest float", c("f").TopK(2), []string{"a NaN", "a 3", "b 0.5", "b 0.25", "c 2", "c 1"}},
		{"strings", c("s").TopK(3), []string{"a pear", "a kiwi", "a fig", "b plum", "b date", "c yuzu", "c lime"}},
		{"dates", c("d").BottomK(1), []string{"a 2024-01-01", "b 2024-01-02", "c 2024-01-05"}},
		{"decimals", c("dec").TopK(5), []string{"a 9.00", "a 5.00", "a 1.00", "b 7.00", "c ∅"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			df, err := frame.GroupBy(c("g")).Agg(tc.agg.Alias("top")).
				Sort(ursus.Asc(c("g"))).Explode("top").Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, gs := textOf(t, df, "g")
			_, vs := textOf(t, df, "top")
			got := make([]string, len(gs))
			for i := range gs {
				got[i] = gs[i] + " " + vs[i]
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("%v, want %v", got, tc.want)
			}
		})
	}
	// Exploding cannot tell a null list from an empty one — both become one null
	// row — so the all-null group's list is read before exploding.
	t.Run("an all-null group is a null list, not an empty one", func(t *testing.T) {
		df, err := frame.GroupBy(c("g")).Agg(c("i").TopK(2).Alias("top")).
			Sort(ursus.Asc(c("g"))).Select(c("top").IsNull().Alias("null")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, got := textOf(t, df, "null"); !slices.Equal(got, []string{"false", "false", "true"}) {
			t.Errorf("null per group: %v, want [false false true]", got)
		}
	})
	t.Run("k below 1 is refused while planning", func(t *testing.T) {
		_, err := frame.GroupBy(c("g")).Agg(c("i").TopK(0)).CollectSchema(t.Context())
		if !errors.Is(err, ursus.ErrValue) {
			t.Errorf("want a value error, got %v", err)
		}
	})
	t.Run("a Boolean column is refused", func(t *testing.T) {
		_, err := ursus.Frame(ursus.Values("b", []bool{true})).
			GroupBy().Agg(c("b").TopK(1)).CollectSchema(t.Context())
		if !errors.Is(err, ursus.ErrType) {
			t.Errorf("want a type error, got %v", err)
		}
	})
}

// TestTopKIsTheRankWindow: the two largest per group, from the accumulator, are the
// rows an ordinal rank ≤ 2 keeps — gb8's two formulations — and TopK(1) is Max, at
// one thread and at eight.
func TestTopKIsTheRankWindow(t *testing.T) {
	c := ursus.Col
	r := rand.New(rand.NewPCG(106, 1))
	n := 20000
	g := make([]int64, n)
	v := make([]float64, n)
	for i := range n {
		g[i] = int64(r.IntN(900))
		v[i] = math.Round(r.Float64()*1e4) / 100 // ties, as real data has
	}
	frame := ursus.Frame(ursus.Values("g", g), ursus.Values("v", v))

	byRank, err := frame.
		WithColumns(c("v").Rank(ursus.RankOrdinal, true).Over(c("g")).Alias("r")).
		Filter(c("r").Le(2)).Select(c("g"), c("v")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	maxes, err := frame.GroupBy(c("g")).Agg(c("v").Max().Alias("v")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, threads := range []int{1, 8} {
		t.Run(fmt.Sprintf("%d threads", threads), func(t *testing.T) {
			top2, err := frame.GroupBy(c("g")).Agg(c("v").TopK(2).Alias("v")).Explode("v").
				Collect(t.Context(), ursus.WithThreads(threads))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, sortedByAll(t, top2), sortedByAll(t, byRank))

			top1, err := frame.GroupBy(c("g")).Agg(c("v").TopK(1).Alias("v")).Explode("v").
				Collect(t.Context(), ursus.WithThreads(threads))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, sortedByAll(t, top1), sortedByAll(t, maxes))
		})
	}
}
