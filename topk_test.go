package ursus_test

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/ursustest"
)

// TestLimitPushdownReachesSort wakes up a path that was written, documented,
// tie-tested, and unreachable.
//
// plan.Sort.Limit's own doc said "Set by the limit-pushdown rule" and no rule set
// it — no composite literal in the repo assigned the field — so sortSink's ArgTopK
// branch could never be taken and Sort(...).Head(k) always did a full O(n log n)
// sort. The `top N` in the plan is the observable proof the rule fired.
func TestLimitPushdownReachesSort(t *testing.T) {
	lf := frame().Sort(ursus.Desc(ursus.Col("price"))).Head(3)

	plan, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "top 3") {
		t.Errorf("the plan must show the sort bounded to a top-k:\n%s", plan)
	}

	// Unoptimized, the bound must NOT appear — otherwise the assertion above would
	// pass for a Sort that was born with a limit rather than given one by the rule.
	raw, err := lf.Explain(t.Context(), ursus.Optimized(false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "top 3") {
		t.Errorf("the unoptimized plan must not carry the bound:\n%s", raw)
	}
}

// TestLimitPushdownNeverWidens: Sort(...).Head(5).Head(10) must keep 5.
func TestLimitPushdownNeverWidens(t *testing.T) {
	plan, err := frame().Sort(ursus.Desc(ursus.Col("price"))).Head(2).Head(9).
		Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan, "top 9") {
		t.Errorf("an outer limit must never widen an inner one:\n%s", plan)
	}
}

// TestTopKMatchesSortHead is the ArgTopK contract seen from outside.
//
// `ArgTopK(n, k, cmp) == ArgSort(n, cmp)[:min(k, n)]` EXACTLY, ties included — and
// ties are the whole difficulty, because a bounded heap is not naturally stable.
// The fixture is built almost entirely of ties for that reason.
func TestTopKMatchesSortHead(t *testing.T) {
	rng := rand.New(rand.NewPCG(31, 37))
	const n = 400
	vals := make([]int64, n)
	ids := make([]int64, n)
	for i := range n {
		vals[i] = int64(rng.IntN(6)) // six distinct values across 400 rows
		ids[i] = int64(i)
	}
	f := ursus.Frame(ursus.Values("v", vals), ursus.Values("id", ids))

	for _, k := range []int{1, 2, 7, 50, 399, 400, 401} {
		// The optimized path takes ArgTopK; disabling the rule takes ArgSort. They
		// must agree row for row, including which of the tied rows survive.
		fast, err := f.Sort(ursus.Asc(ursus.Col("v"))).Head(k).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		slow, err := f.Sort(ursus.Asc(ursus.Col("v"))).Head(k).
			Collect(t.Context(), ursus.WithOptFlags(noLimitPushdown()))
		if err != nil {
			t.Fatal(err)
		}
		if fast.String() != slow.String() {
			t.Fatalf("k=%d: top-k and full sort disagree\ntop-k:\n%s\nsort:\n%s",
				k, fast, slow)
		}
	}
}

// TestTopKAndBottomK: TopK is the largest k, BottomK the smallest — and the null
// placement must not move when the direction is inverted.
func TestTopKAndBottomK(t *testing.T) {
	f := ursus.Frame(
		ursus.ValuesNullable("v", []int64{3, 1, 4, 1, 5, 0},
			[]bool{true, true, true, true, true, false}),
	)
	top, err := f.TopK(2, ursus.Asc(ursus.Col("v"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	bottom, err := f.BottomK(2, ursus.Asc(ursus.Col("v"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	tv, err := top.Column[int64]("v")
	if err != nil {
		t.Fatal(err)
	}
	bv, err := bottom.Column[int64]("v")
	if err != nil {
		t.Fatal(err)
	}
	gotTop := []int64{}
	for i := range top.Height() {
		v, _ := tv.Get(i)
		gotTop = append(gotTop, v)
	}
	if !slices.Equal(gotTop, []int64{5, 4}) {
		t.Errorf("TopK(2) = %v, want [5 4]", gotTop)
	}
	gotBottom := []int64{}
	for i := range bottom.Height() {
		v, _ := bv.Get(i)
		gotBottom = append(gotBottom, v)
	}
	if !slices.Equal(gotBottom, []int64{1, 1}) {
		t.Errorf("BottomK(2) = %v, want [1 1]", gotBottom)
	}
	// Neither end may contain the null: a null has no rank, so it belongs to
	// neither the top nor the bottom of the column.
	for i := range bottom.Height() {
		if _, ok := bv.Get(i); !ok {
			t.Errorf("BottomK returned a null row; a null has no rank:\n%s", bottom)
		}
	}
	for i := range top.Height() {
		if _, ok := tv.Get(i); !ok {
			t.Errorf("TopK returned a null row; a null has no rank:\n%s", top)
		}
	}
}

func noLimitPushdown() plan.Flags {
	f := plan.DefaultFlags()
	f.LimitPushdown = false
	return f
}

func TestGoldenTopKPlan(t *testing.T) {
	lf := frame().Sort(ursus.Desc(ursus.Col("price"))).Head(3).
		Select(ursus.Col("id"), ursus.Col("price"))
	ursustest.AssertPlan(t, lf, "testdata/plans/topk.txt")
}
