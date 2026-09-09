package ursus_test

import (
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestGoldenPlan exercises the plan-snapshot machinery on the query the whole
// step exists to make work.
//
// The value of a golden plan is that it catches a regression the result-comparison
// tests cannot: if projection pushdown broke, this query would still return the
// right two columns — it would just read all six to do it. Only the plan diff
// notices.
//
// Run `go test -update` to rewrite testdata/plans/*.txt.
func TestGoldenPlan(t *testing.T) {
	lf := frame().
		Filter(ursus.Col("price").Gt(5)).
		Select(ursus.Col("id"), ursus.Col("price"))

	ursustest.AssertPlan(t, lf, "testdata/plans/skeleton_optimized.txt")
	ursustest.AssertPlan(t, lf, "testdata/plans/skeleton_raw.txt", ursus.Optimized(false))
}

// TestFrameEqual checks the assertion helper itself against a query whose answer
// is written out by hand.
func TestFrameEqual(t *testing.T) {
	got, err := frame().
		Filter(ursus.Col("region").Eq("eu")).
		Select(ursus.Col("id"), ursus.Col("region")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want, err := ursus.Frame(
		ursus.Values("id", []int64{1, 3, 6}),
		ursus.Values("region", []string{"eu", "eu", "eu"}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	ursustest.AssertFrameEqual(t, got, want)
	ursustest.AssertSchemaEqual(t, got.Schema(), want.Schema())
}

// TestGoldenJoinPlan snapshots a join, which is where the left/right markers earn
// their place: without them Join(A, B) and Join(B, A) render identically, so this
// file could not see a rule that swapped the sides.
//
// It also pins the projection pushed into each side — a semi join reading a
// forty-column dimension table would still return the right rows, and only a plan
// diff would notice.
func TestGoldenJoinPlan(t *testing.T) {
	wide := ursus.Frame(
		ursus.Values("k", []int64{2}),
		ursus.Values("w1", []string{"x"}),
		ursus.Values("w2", []string{"y"}),
	)

	inner := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
		ursus.JoinHow(ursus.JoinLeft))
	ursustest.AssertPlan(t, inner, "testdata/plans/join_left.txt")

	semi := joinLeft().Join(wide, ursus.JoinOn(ursus.Col("k")),
		ursus.JoinHow(ursus.JoinSemi))
	ursustest.AssertPlan(t, semi, "testdata/plans/join_semi.txt")
}

// TestGoldenJoinWherePlan snapshots the collapse, which is the only place a user
// can SEE whether their JoinWhere became a hash join or stayed a loop over every
// pair. The raw plan is the cross-plus-filter the API builds; the optimized one is
// what actually runs, and the difference between the two files is the rule.
func TestGoldenJoinWherePlan(t *testing.T) {
	lf := joinLeft().JoinWhere(joinRight(),
		ursus.Col("k").Eq(ursus.Col("k_right")),
		ursus.Col("lv").Lt(ursus.Col("rv")),
	)
	ursustest.AssertPlan(t, lf, "testdata/plans/join_where_raw.txt", ursus.Optimized(false))
	ursustest.AssertPlan(t, lf, "testdata/plans/join_where.txt")
}

// TestGoldenWhereExistsPlan snapshots the residual semi join. The raw plan carries
// every predicate inside the join; the optimized one has the equality as a key and
// the rest still inside. That split is the only place a user can see whether their
// WhereExists is a hash join or a scan of every pair.
func TestGoldenWhereExistsPlan(t *testing.T) {
	lf := joinLeft().WhereExists(joinRight(),
		ursus.Col("k").Eq(ursus.Col("k_right")),
		ursus.Col("lv").Lt(ursus.Col("rv")),
	)
	ursustest.AssertPlan(t, lf, "testdata/plans/where_exists_raw.txt", ursus.Optimized(false))
	ursustest.AssertPlan(t, lf, "testdata/plans/where_exists.txt")
}

// TestGoldenConditionalPlan snapshots a conditional and an is_in.
//
// A node's String() is load-bearing beyond display: predicatePushdown detects its
// own fixpoint by comparing Explain output before and after a pass, and
// extractAggs deduplicates aggregates by keying a map on the rendering. A Cond or
// an Agg whose String hid part of its meaning would break the optimizer, not just
// the picture — which is exactly what an unparameterised quantile did.
//
// The plan also pins that the predicate containing a conditional is pushed BELOW
// the projection, which is what opening substitutable to *Cond bought.
func TestGoldenConditionalPlan(t *testing.T) {
	lf := frame().
		Select(
			ursus.Col("id"),
			ursus.When(ursus.Col("price").Gt(10)).Then("high").Otherwise("low").Alias("band"),
			ursus.Coalesce(ursus.Col("discount"), ursus.Lit(0.0)).Alias("disc"),
		).
		Filter(ursus.Col("id").IsIn(int64(1), int64(3)))

	ursustest.AssertPlan(t, lf, "testdata/plans/conditional_optimized.txt")
	ursustest.AssertPlan(t, lf, "testdata/plans/conditional_raw.txt", ursus.Optimized(false))
}

// TestGoldenAggregateParametersPlan pins that an aggregate's parameters survive
// into the plan text. Two quantiles that rendered identically silently became one.
func TestGoldenAggregateParametersPlan(t *testing.T) {
	lf := sales().
		GroupBy(ursus.Col("region")).
		Agg(
			ursus.Col("amount").Quantile(0.5, ursus.InterpLinear).Alias("p50"),
			ursus.Col("amount").Quantile(0.99, ursus.InterpNearest).Alias("p99"),
			ursus.Col("amount").Std(1).Alias("sd"),
		)
	ursustest.AssertPlan(t, lf, "testdata/plans/agg_params.txt")
}

// TestGoldenWindowPlan snapshots the Window node.
//
// Three things are pinned that nothing else would catch: that Resolve splices a
// WINDOW node beneath the projection rather than leaving the window in the
// expression; that the window's String renders its partition key, which is what
// stops two different windows sharing one temporary; and that projection pushdown
// reads exactly the columns the windows mention.
func TestGoldenWindowPlan(t *testing.T) {
	lf := frame().Select(
		ursus.Col("id"),
		ursus.Col("price").Sum().Over(ursus.Col("region")).Alias("region_total"),
		ursus.Col("price").Rank(ursus.RankDense, true).Over(ursus.Col("region")).Alias("rk"),
	)
	ursustest.AssertPlan(t, lf, "testdata/plans/window_optimized.txt")
	ursustest.AssertPlan(t, lf, "testdata/plans/window_raw.txt", ursus.Optimized(false))
}
