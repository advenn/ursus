package ursus_test

// TopK and BottomK inside a group-by (step 106).
//
// h2o gb8 — the two largest v3 per id6 — was answered with an ordinal rank over a
// partitioned window and a filter, the SQL formulation, and step 24 measured the
// window's sort at 92% of it. Polars answers it with top_k inside the group-by, a
// bounded accumulator, and these pin that ursus's does what the window did. The lists
// are Polars 2.1.0's since step 500, recorded in the topk_agg group of testdata/oracle
// and spelled in oracle_topk_agg_test.go, which took this file's table; what stays
// asserts TopK's own rule for a group of only nulls, and the refusals.

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

func TestTopKValues(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a", "a", "b", "b", "c", "c"}),
		ursus.ValuesNullable("i", []int64{5, 9, 1, 0, 7, 0, 0, 0}, []bool{true, true, true, false, true, false, false, false}),
	)
	// Exploding cannot tell a null list from an empty one — both become one null
	// row — so the all-null group's list is read before exploding. TopK's doc: nulls
	// are skipped, and a group with none gives null (Polars 2.1 keeps the nulls).
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
