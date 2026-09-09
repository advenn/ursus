package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// keyed reports whether the collapse rule turned a residual conjunct into a hash
// key. Without it the join is a nested loop over one key group — correct, and a
// completely different code path — which is what makes every rule-off comparison
// below evidence rather than tautology.
func keyed(t *testing.T, lf *ursus.LazyFrame) bool {
	t.Helper()
	s, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(s, "] = [")
}

// TestWhereExistsEqualsTheKeylessForm is the load-bearing test of step 44.
//
// With the rule off the predicate is evaluated against every pair of one key group;
// with it on, the equality becomes a hash key and only the remainder is tested per
// candidate. Those share the verdict logic and nothing else, and they must agree at
// every batch size and thread count — the residual runs on the parallel probe.
func TestWhereExistsEqualsTheKeylessForm(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() *ursus.LazyFrame
	}{
		{"exists", func() *ursus.LazyFrame {
			return joinLeft().WhereExists(joinRight(),
				ursus.Col("k").Eq(ursus.Col("k_right")),
				ursus.Col("lv").Lt(ursus.Col("rv")))
		}},
		{"not exists", func() *ursus.LazyFrame {
			return joinLeft().WhereNotExists(joinRight(),
				ursus.Col("k").Eq(ursus.Col("k_right")),
				ursus.Col("lv").Lt(ursus.Col("rv")))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !keyed(t, tc.build()) {
				s, _ := tc.build().Explain(t.Context())
				t.Fatalf("the equality should have become a key:\n%s", s)
			}
			want, err := tc.build().Collect(t.Context(), ursus.WithOptFlags(noCollapse()))
			if err != nil {
				t.Fatal(err)
			}
			for _, size := range []int{1, 2, 3, 7, 64, 8192} {
				for _, threads := range []int{1, 2, 3, 4, 8} {
					got, err := tc.build().Collect(t.Context(),
						ursus.WithBatchSize(size), ursus.WithThreads(threads), ursus.WithVerify())
					if err != nil {
						t.Fatalf("batch %d, %d threads: %v", size, threads, err)
					}
					ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
				}
			}
		})
	}
}

// TestWhereExistsPartitionsTheLeftFrame is the property that holds on any fixture,
// and the reason `WhereNotExists` is named as the complement rather than as a join
// kind: every left row is in exactly one of the two answers.
//
// The equi-join version of this (TestSemiAndAntiPartitionTheLeftFrame) is what
// caught a shifted-window bug in the spilling probe. With a residual there is more
// to get wrong, not less.
func TestWhereExistsPartitionsTheLeftFrame(t *testing.T) {
	preds := func() []ursus.Expr {
		return []ursus.Expr{
			ursus.Col("k").Eq(ursus.Col("k_right")),
			ursus.Col("lv").Lt(ursus.Col("rv")),
		}
	}
	yes, err := joinLeft().WhereExists(joinRight(), preds()...).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	no, err := joinLeft().WhereNotExists(joinRight(), preds()...).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	if got := yes.Height() + no.Height(); got != 5 {
		t.Fatalf("exists %d + not-exists %d = %d, want 5\n%s\n%s",
			yes.Height(), no.Height(), got, yes, no)
	}
	seen := map[string]bool{}
	for _, df := range []*ursus.DataFrame{yes, no} {
		for i := range df.Height() {
			v, _, err := df.At[string](i, "lv")
			if err != nil {
				t.Fatal(err)
			}
			if seen[v] {
				t.Errorf("row %q is in both answers", v)
			}
			seen[v] = true
		}
	}
}

// TestWhereExistsDoesNotDuplicate is the difference between this and JoinWhere,
// stated as a test. One left row against three satisfying partners is ONE row.
//
// An inner join plus a filter returns three, which is why step 43 could not do this
// and why the predicate is evaluated before the verdict rather than after it.
func TestWhereExistsDoesNotDuplicate(t *testing.T) {
	left := ursus.Frame(
		ursus.Values("k", []int64{1}),
		ursus.Values("lv", []int64{10}),
	)
	right := ursus.Frame(
		ursus.Values("k", []int64{1, 1, 1}),
		ursus.Values("rv", []int64{1, 2, 3}),
	)
	preds := []ursus.Expr{
		ursus.Col("k").Eq(ursus.Col("k_right")),
		ursus.Col("rv").Lt(ursus.Col("lv")),
	}

	df, err := left.WhereExists(right, preds...).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Errorf("got %d rows, want 1 — three partners are still one answer:\n%s",
			df.Height(), df)
	}

	// And the contrast, so the test says what it is contrasting with.
	pairs, err := left.JoinWhere(right, preds...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if pairs.Height() != 3 {
		t.Errorf("JoinWhere should pair every satisfying row: got %d, want 3\n%s",
			pairs.Height(), pairs)
	}
}

// TestWhereExistsHighFanOut is the shape no existing fixture could produce: one
// probe row with far more candidates than a batch holds.
//
// The residual materialises candidates a batch at a time, so this is the only test
// that exercises more than one chunk per probe row — and the only one that would
// catch a verdict formed from a partial candidate list. joinLeft's fan-out is two.
func TestWhereExistsHighFanOut(t *testing.T) {
	const n = 20000
	ks := make([]int64, n)
	rv := make([]int64, n)
	for i := range n {
		ks[i] = 1
		rv[i] = int64(i)
	}
	right := ursus.Frame(ursus.Values("k", ks), ursus.Values("rv", rv))

	// Three left rows against the same 20,000 partners: one where only the very
	// last candidate satisfies the predicate, one where only the first does, and
	// one where none do. The first is what a truncated candidate list gets wrong.
	left := ursus.Frame(
		ursus.Values("k", []int64{1, 1, 1}),
		ursus.Values("lim", []int64{n - 1, 1, n + 1}),
	)
	preds := []ursus.Expr{
		ursus.Col("k").Eq(ursus.Col("k_right")),
		ursus.Col("rv").Ge(ursus.Col("lim")),
	}

	for _, size := range []int{1, 2, 3, 7, 64, 8192} {
		for _, threads := range []int{1, 4} {
			yes, err := left.WhereExists(right, preds...).
				Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithThreads(threads),
					ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d: %v", size, err)
			}
			if yes.Height() != 2 {
				t.Fatalf("batch %d, %d threads: got %d rows, want 2\n%s",
					size, threads, yes.Height(), yes)
			}
			no, err := left.WhereNotExists(right, preds...).
				Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithThreads(threads),
					ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d: %v", size, err)
			}
			if no.Height() != 1 {
				t.Fatalf("batch %d, %d threads: got %d not-exists rows, want 1\n%s",
					size, threads, no.Height(), no)
			}
		}
	}
}

// TestWhereExistsPureInequality pins the other half of the rule. With no equality
// among the predicates there is no key to extract, so the join stays keyless and
// tests every pair — which must still be the right answer.
func TestWhereExistsPureInequality(t *testing.T) {
	lf := joinLeft().WhereExists(joinRight(), ursus.Col("lv").Lt(ursus.Col("rv")))
	if keyed(t, lf) {
		s, _ := lf.Explain(t.Context())
		t.Fatalf("there is no equality to turn into a key:\n%s", s)
	}

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// Every left value a..e against right values p,q,r,s: a<p holds for a,b,c,d,e
	// except where no rv exceeds lv. All five are less than "s".
	if df.Height() != 5 {
		t.Errorf("got %d rows, want 5\n%s", df.Height(), df)
	}
}

// TestWhereExistsResidualColumnSurvivesProjection is the projection-pushdown edge.
//
// A semi join emits no right column, so pushdown narrows the right input to the key
// columns alone — which is exactly the optimisation that would delete the column
// the residual reads. `rv` is in no key and appears only inside the predicate.
func TestWhereExistsResidualColumnSurvivesProjection(t *testing.T) {
	right := ursus.Frame(
		ursus.Values("k", []int64{1, 2}),
		ursus.Values("rv", []int64{100, 0}),
		ursus.Values("unused", []string{"x", "y"}),
	)
	left := ursus.Frame(
		ursus.Values("k", []int64{1, 2}),
		ursus.Values("lv", []int64{5, 5}),
	)

	lf := left.WhereExists(right,
		ursus.Col("k").Eq(ursus.Col("k_right")),
		ursus.Col("rv").Gt(ursus.Col("lv")))

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Fatalf("got %d rows, want 1 (only k=1 has rv > lv)\n%s", df.Height(), df)
	}
	if v, _, err := df.At[int64](0, "k"); err != nil {
		t.Fatal(err)
	} else if v != 1 {
		t.Errorf("k = %d, want 1", v)
	}

	// The plan should still prune `unused`, or the test proves nothing about
	// pushdown having run at all.
	s, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "unused") {
		t.Errorf("the column no one reads should still be pruned:\n%s", s)
	}
}

// TestWhereExistsSpills runs a residual join whose build side does not fit.
//
// The sub-sink a spill replay builds copies its parent's fields BY HAND, so a
// replayed bucket that lost the residual's namespace would answer "does a partner
// exist" for spilled keys and "does a satisfying partner exist" for resident ones —
// with no error anywhere.
func TestWhereExistsSpills(t *testing.T) {
	const rows = 4000
	mk := func(n int, mul int64) *ursus.LazyFrame {
		ks := make([]int64, n)
		vs := make([]int64, n)
		pad := make([]string, n)
		for i := range n {
			ks[i] = int64((i * 7919) % 600)
			vs[i] = int64(i) * mul
			pad[i] = strings.Repeat("x", 16)
		}
		return ursus.Frame(ursus.Values("k", ks), ursus.Values("v", vs),
			ursus.Values("pad", pad))
	}

	query := func(not bool) *ursus.LazyFrame {
		l, r := mk(rows, 1), mk(2000, 3)
		preds := []ursus.Expr{
			ursus.Col("k").Eq(ursus.Col("k_right")),
			ursus.Col("v_right").Gt(ursus.Col("v")),
		}
		if not {
			return l.WhereNotExists(r, preds...)
		}
		return l.WhereExists(r, preds...)
	}

	collect := func(not bool, limit int64) *ursus.DataFrame {
		t.Helper()
		var stats ursus.MemoryStats
		opts := []ursus.CollectOption{ursus.WithBatchSize(256), ursus.WithVerify(),
			ursus.WithMemoryStats(&stats)}
		if limit > 0 {
			opts = append(opts, ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()))
		}
		df, err := query(not).Collect(t.Context(), opts...)
		if err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		// A spill test that does not spill proves nothing, and the failure mode it
		// exists for — a replayed bucket that lost the residual — is only reachable
		// through the sub-sink. Assert the path was taken.
		if limit > 0 && stats.Spills == 0 {
			t.Fatalf("limit=%d produced no spill; the fixture fits and the replay "+
				"path is untested", limit)
		}
		return df
	}

	want := collect(false, 0)
	for _, limit := range []int64{8 << 10, 32 << 10} {
		got := collect(false, limit)
		ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder(),
			ursustest.CheckNullability())

		// The partition property survives spilling, which is what caught the
		// shifted-window bug in the equi-join version of this test.
		no := collect(true, limit)
		if n := got.Height() + no.Height(); n != rows {
			t.Errorf("limit=%d: exists %d + not-exists %d = %d, want %d",
				limit, got.Height(), no.Height(), n, rows)
		}
	}
}

// TestWhereExistsRequiresAPredicate — with none, every row of the other frame is a
// partner, so the answer is either every row or none, and neither is what anyone
// meant to ask.
func TestWhereExistsRequiresAPredicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"exists", joinLeft().WhereExists(joinRight())},
		{"not exists", joinLeft().WhereNotExists(joinRight())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.lf.Collect(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, uerr.ErrValue) {
				t.Errorf("kind should be Value: %v", err)
			}
			if !strings.Contains(err.Error(), "predicate") {
				t.Errorf("the error should say what is missing: %v", err)
			}
		})
	}
}

// TestWhereExistsPredicateErrorsNameThePairNamespace: the predicate is written in
// names the OUTPUT does not have, so an unknown column is the easiest mistake to
// make here and the error has to be worth reading.
func TestWhereExistsPredicateErrorsNameThePairNamespace(t *testing.T) {
	_, err := joinLeft().WhereExists(joinRight(), ursus.Col("nope").Eq(ursus.Col("k"))).
		Collect(t.Context())
	if err == nil {
		t.Fatal("expected an error for an unknown column")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("the error should name the column: %v", err)
	}

	_, err = joinLeft().WhereExists(joinRight(), ursus.Col("k")).Collect(t.Context())
	if err == nil {
		t.Fatal("expected an error for a non-Boolean predicate")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}
	if !strings.Contains(err.Error(), "_right") {
		t.Errorf("the hint should explain the naming: %v", err)
	}
}

// TestWhereExistsMatchesTheDistinctFormulation is the independent check: what
// WhereExists computes is what a hand-written "join, then keep the distinct left
// rows" would, on a fixture where the two could differ.
//
// It is the formulation this step rejected on cost grounds, which makes it a good
// oracle: it shares no code with the probe's verdict.
func TestWhereExistsMatchesTheDistinctFormulation(t *testing.T) {
	const n = 300
	lk := make([]int64, n)
	lv := make([]int64, n)
	for i := range n {
		lk[i] = int64(i % 40)
		lv[i] = int64(i) * 2
	}
	rk := make([]int64, n)
	rv := make([]int64, n)
	for i := range n {
		rk[i] = int64(i % 40)
		rv[i] = int64(i)
	}
	left := ursus.Frame(ursus.Values("k", lk), ursus.Values("lv", lv))
	right := ursus.Frame(ursus.Values("k", rk), ursus.Values("rv", rv))

	preds := []ursus.Expr{
		ursus.Col("k").Eq(ursus.Col("k_right")),
		ursus.Col("rv").Gt(ursus.Col("lv")),
	}

	got, err := left.WhereExists(right, preds...).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	// The oracle: pair every satisfying combination, then reduce to the distinct
	// left rows. Equivalent by definition, and built from different operators.
	indexed := left.WithRowIndex("__id", 0)
	ids := indexed.JoinWhere(right, preds...).Select(ursus.Col("__id")).Unique()
	want, err := indexed.
		Join(ids, ursus.JoinOn(ursus.Col("__id")), ursus.JoinHow(ursus.JoinSemi)).
		Select(ursus.Col("k"), ursus.Col("lv")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Height() == 0 || got.Height() == n {
		t.Fatalf("the fixture is not discriminating: %d of %d rows", got.Height(), n)
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
}
