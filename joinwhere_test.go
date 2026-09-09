package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// noCollapse is DefaultFlags with only the new rule switched off, which is what
// makes every test below a differential rather than a self-comparison. With the
// rule off the query runs as a cross join plus a filter — code that shares nothing
// with the hash path — so the two answers agreeing is evidence, not tautology.
func noCollapse() plan.Flags {
	f := plan.DefaultFlags()
	f.CollapseCrossJoin = false
	return f
}

// collapsed reports whether the rule fired, by reading the plan rather than by
// timing anything. A rule that silently stops firing is an optimisation that
// vanishes with no test failing — step 36 shipped a fleet of workers where only one
// ever ran for exactly this reason.
func collapsed(t *testing.T, lf *ursus.LazyFrame) bool {
	t.Helper()
	s, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return !strings.Contains(s, "CROSS")
}

// TestJoinWhereEqualsCrossPlusFilter is the load-bearing test of step 43.
//
// The rewrite has to hold across batch boundaries and thread counts, because the
// collapsed plan runs on the parallel hash probe and the uncollapsed one does not.
func TestJoinWhereEqualsCrossPlusFilter(t *testing.T) {
	query := func() *ursus.LazyFrame {
		return joinLeft().JoinWhere(joinRight(),
			ursus.Col("k").Eq(ursus.Col("k_right")),
			ursus.Col("lv").Lt(ursus.Col("rv")),
		)
	}

	want, err := query().Collect(t.Context(), ursus.WithOptFlags(noCollapse()))
	if err != nil {
		t.Fatal(err)
	}
	if !collapsed(t, query()) {
		t.Fatal("the rule did not fire, so the comparison below proves nothing")
	}

	for _, size := range []int{1, 2, 3, 7, 64, 8192} {
		for _, threads := range []int{1, 2, 3, 4, 8} {
			got, err := query().Collect(t.Context(),
				ursus.WithBatchSize(size), ursus.WithThreads(threads), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d, %d threads: %v", size, threads, err)
			}
			ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
		}
	}
}

// TestJoinWhereKeepsBothKeyColumns is the coalesce trap, and the fixture is chosen
// for it: joinLeft and joinRight both call the key "k", so an inherited Auto
// coalesce would MERGE them and the rewritten plan would return one column fewer
// than the plan it replaced. Renaming either side hides the bug completely.
func TestJoinWhereKeepsBothKeyColumns(t *testing.T) {
	lf := joinLeft().JoinWhere(joinRight(), ursus.Col("k").Eq(ursus.Col("k_right")))

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if !collapsed(t, lf) {
		t.Fatal("the rule did not fire")
	}

	for _, name := range []string{"k", "lv", "k_right", "rv"} {
		if df.Schema().IndexOf(name) < 0 {
			t.Errorf("column %q is missing from %v", name, df.Schema().Names())
		}
	}
	if df.Width() != 4 {
		t.Errorf("got %d columns, want 4: %v", df.Width(), df.Schema().Names())
	}

	// And the values, not only the shape: a coalesced key would still be four
	// columns wide if some other column came along instead.
	uncollapsed, err := lf.Collect(t.Context(), ursus.WithOptFlags(noCollapse()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, df, uncollapsed, ursustest.CheckNullability())
}

// TestJoinWhereNullKeysDoNotMatch is the legality argument in the rule's doc, made
// into a test. `l.k == r.k` over a cross product drops a null key because the
// comparison is null and Filter drops it; an inner join with NullsEqual false drops
// it too. The two agree, and that agreement is what makes the rewrite sound — so a
// rewrite that set NullsEqual true would invent a row here.
func TestJoinWhereNullKeysDoNotMatch(t *testing.T) {
	lf := joinLeft().JoinWhere(joinRight(), ursus.Col("k").Eq(ursus.Col("k_right")))

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// left k = [1,2,2,null,3], right k = [2,2,4,null]. Only the two 2s on each
	// side match, so 2x2 = 4 rows. The nulls contribute nothing.
	if df.Height() != 4 {
		t.Fatalf("got %d rows, want 4 — a null key must not match another null:\n%s",
			df.Height(), df)
	}
	for i := range df.Height() {
		if v, ok, err := df.At[int64](i, "k"); err != nil {
			t.Fatal(err)
		} else if !ok || v != 2 {
			t.Errorf("row %d: k = %v (ok=%v), want 2", i, v, ok)
		}
	}
}

// TestJoinWherePureInequalityStaysACrossJoin pins the rule's other half. A
// predicate with no equality is a loop join; the keyless probe already streams its
// pairs, so the rule must leave the plan alone rather than invent a key.
func TestJoinWherePureInequalityStaysACrossJoin(t *testing.T) {
	lf := joinLeft().JoinWhere(joinRight(), ursus.Col("lv").Lt(ursus.Col("rv")))

	if collapsed(t, lf) {
		s, _ := lf.Explain(t.Context())
		t.Fatalf("a predicate with no equality has no join key to extract:\n%s", s)
	}

	got, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	want, err := lf.Collect(t.Context(), ursus.WithOptFlags(noCollapse()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
}

// TestJoinWhereInterval is the documented use — an interval match, which takes two
// predicates and has no equality at all between the interval bounds. It is here
// because it is the shape the API doc advertises, and because it exercises a
// residual with TWO conjuncts.
func TestJoinWhereInterval(t *testing.T) {
	sessions := ursus.Frame(
		ursus.Values("sid", []int64{1, 2}),
		ursus.Values("start", []int64{10, 30}),
		ursus.Values("end", []int64{20, 40}),
	)
	events := ursus.Frame(
		ursus.Values("eid", []int64{100, 101, 102, 103}),
		ursus.Values("ts", []int64{5, 15, 32, 45}),
	)

	lf := sessions.JoinWhere(events,
		ursus.Col("ts").Ge(ursus.Col("start")),
		ursus.Col("ts").Lt(ursus.Col("end")),
	)
	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// ts=15 falls in [10,20); ts=32 falls in [30,40). 5 and 45 fall in neither.
	if df.Height() != 2 {
		t.Fatalf("got %d rows, want 2:\n%s", df.Height(), df)
	}
	for i, want := range []int64{101, 102} {
		if v, _, err := df.At[int64](i, "eid"); err != nil {
			t.Fatal(err)
		} else if v != want {
			t.Errorf("row %d: eid = %d, want %d", i, v, want)
		}
	}
}

// TestJoinWhereMixedPredicateOrder checks that the rule does not depend on which
// way round the user typed the equality, or on where it sits among the conjuncts.
func TestJoinWhereMixedPredicateOrder(t *testing.T) {
	forms := []struct {
		name  string
		preds []ursus.Expr
	}{
		{"equality first", []ursus.Expr{
			ursus.Col("k").Eq(ursus.Col("k_right")),
			ursus.Col("lv").Lt(ursus.Col("rv")),
		}},
		{"equality last", []ursus.Expr{
			ursus.Col("lv").Lt(ursus.Col("rv")),
			ursus.Col("k").Eq(ursus.Col("k_right")),
		}},
		{"right side first", []ursus.Expr{
			ursus.Col("k_right").Eq(ursus.Col("k")),
			ursus.Col("lv").Lt(ursus.Col("rv")),
		}},
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			lf := joinLeft().JoinWhere(joinRight(), f.preds...)
			if !collapsed(t, lf) {
				s, _ := lf.Explain(t.Context())
				t.Fatalf("the equality should have become a join key:\n%s", s)
			}
			got, err := lf.Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			want, err := lf.Collect(t.Context(), ursus.WithOptFlags(noCollapse()))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
		})
	}
}

// TestJoinWhereOneSidedPredicateIsNotAKey is the constant-key trap from the rule's
// doc, reached from the public API. `k == 2` is an OpEq whose right side is a
// literal, and a literal belongs to any namespace — so a rule that only asked
// "can this side be expressed over the right input" would join on a constant.
//
// Predicate pushdown normally moves this conjunct into the left input before the
// collapse rule ever sees it, so the test also runs with that pushdown OFF, which
// is what leaves the one-sided conjunct sitting where the trap is.
func TestJoinWhereOneSidedPredicateIsNotAKey(t *testing.T) {
	noJoinPush := plan.DefaultFlags()
	noJoinPush.PredicatePushdown = false

	lf := joinLeft().JoinWhere(joinRight(),
		ursus.Col("k").Eq(ursus.Lit(int64(2))),
		ursus.Col("k").Eq(ursus.Col("k_right")),
	)

	for _, tc := range []struct {
		name  string
		flags plan.Flags
	}{
		{"default", plan.DefaultFlags()},
		{"no predicate pushdown", noJoinPush},
		{"no collapse", noCollapse()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df, err := lf.Collect(t.Context(), ursus.WithOptFlags(tc.flags), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			// Both left 2s against both right 2s, and k == 2 changes nothing
			// because 2 is the only key that matches anyway.
			if df.Height() != 4 {
				t.Errorf("got %d rows, want 4:\n%s", df.Height(), df)
			}
		})
	}
}

// TestJoinWhereRequiresAPredicate — a JoinWhere with none is a cartesian product
// written as though it were filtered, and the cardinality difference is |L| vs
// |L|x|R|. Erroring is the same choice resolveJoin makes for a keyless equi-join.
func TestJoinWhereRequiresAPredicate(t *testing.T) {
	_, err := joinLeft().JoinWhere(joinRight()).Collect(t.Context())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "JoinCross") {
		t.Errorf("the error should point at the deliberate way to write it: %v", err)
	}
	if !errors.Is(err, uerr.ErrValue) {
		t.Errorf("kind should be Value: %v", err)
	}
}

// TestFilterOverInnerJoinIsNotCollapsed guards the rule's precondition. A two-sided
// equality above an EXISTING inner join is a legal plan — predicate pushdown leaves
// it there, because the "both sides" row of the legality table is a barrier for
// every kind — and the rule must not touch it.
//
// The failure it pins is not subtle once seen: the rewrite REPLACES LeftOn/RightOn,
// so firing on a join that already has keys would drop them and join on the
// predicate instead. The fixture is built so that those two answers differ, which
// the joinLeft/joinRight one does not: there, no lv ever equals an rv, so both the
// right plan and the wrong one return nothing.
func TestFilterOverInnerJoinIsNotCollapsed(t *testing.T) {
	left := ursus.Frame(
		ursus.Values("k", []int64{1, 1, 2}),
		ursus.Values("v", []string{"x", "y", "x"}),
	)
	right := ursus.Frame(
		ursus.Values("k", []int64{1, 2}),
		ursus.Values("v", []string{"x", "x"}),
	)

	lf := left.Join(right, ursus.JoinOn(ursus.Col("k"))).
		Filter(ursus.Col("v").Eq(ursus.Col("v_right")))

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// Inner on k gives (l0,r0), (l1,r0), (l2,r1); v == v_right keeps the first and
	// the third. Collapsing onto v instead would drop the k condition and return
	// four rows.
	if df.Height() != 2 {
		t.Fatalf("got %d rows, want 2 — the join's own keys must survive:\n%s",
			df.Height(), df)
	}

	want, err := lf.Collect(t.Context(), ursus.WithOptFlags(noCollapse()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, df, want, ursustest.CheckNullability())
}
