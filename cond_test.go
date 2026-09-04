package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"ursus"
	"ursus/internal/uerr"
)

// cf is the conditional fixture: a nullable score column, so every test can reach
// the null-predicate case without building its own frame.
func cf() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("id", []int64{1, 2, 3, 4}),
		ursus.ValuesNullable("score", []int64{95, 85, 40, 0},
			[]bool{true, true, true, false}),
		ursus.Values("a", []int64{10, 20, 30, 40}),
		ursus.Values("b", []int64{-1, -2, -3, -4}),
	)
}

func TestWhenThenOtherwiseChain(t *testing.T) {
	df, err := cf().Select(
		ursus.Col("id"),
		ursus.When(ursus.Col("score").Ge(90)).Then("A").
			When(ursus.Col("score").Ge(80)).Then("B").
			Otherwise("F").Alias("grade"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	g, err := df.Column[string]("grade")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A", "B", "F"}
	for i, w := range want {
		if v, ok := g.Get(i); !ok || v != w {
			t.Errorf("row %d grade = %q (valid %v), want %q", i, v, ok, w)
		}
	}
	// Row 3's score is null, so no branch of the chain applies.
	if v, ok := g.Get(3); ok {
		t.Errorf("row 3 grade = %q, want null: the score is null so no condition holds", v)
	}
}

// TestCondNamesAfterThen pins the OutputName case.
//
// Leftmost-column-wins is the rule everywhere else, and applied blindly here it
// would name the result after the CONDITION's column — a column that need not
// appear in the output at all.
func TestCondNamesAfterThen(t *testing.T) {
	df, err := cf().Select(
		ursus.When(ursus.Col("score").Gt(50)).Then(ursus.Col("a")).Otherwise(ursus.Col("b")),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Names()[0]; got != "a" {
		t.Errorf("column name = %q, want %q — leftmost-column-wins would say \"score\"", got, "a")
	}
}

// TestCondNullPredicateTakesNeitherBranch is the single most likely silent wrong
// answer in the conditional: treating a null condition as false would make
// Otherwise absorb missing data.
func TestCondNullPredicateTakesNeitherBranch(t *testing.T) {
	df, err := cf().Select(
		ursus.Col("id"),
		ursus.When(ursus.Col("score").Gt(50)).Then(ursus.Col("a")).
			Otherwise(ursus.Col("b")).Alias("v"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	v, err := df.Column[int64]("v")
	if err != nil {
		t.Fatal(err)
	}

	// 95 > 50 → a; 85 > 50 → a; 40 > 50 is false → b; null > 50 is NULL → neither.
	for i, want := range []int64{10, 20, -3} {
		if got, ok := v.Get(i); !ok || got != want {
			t.Errorf("row %d = %v (valid %v), want %d", i, got, ok, want)
		}
	}
	if got, ok := v.Get(3); ok {
		t.Errorf("row 3 = %v, want null — a null condition must not fall through to Otherwise", got)
	}
}

// TestCondBroadcastsLiteralBranches: literals reach the evaluator as LENGTH-1
// columns, and the operator-boundary broadcast runs after Eval returns. If the
// conditional does not repeat them itself, this query fails or returns one row.
func TestCondBroadcastsLiteralBranches(t *testing.T) {
	df, err := cf().Select(
		ursus.When(ursus.Col("score").Ge(80)).Then(1).Otherwise(0).Alias("pass"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 4 {
		t.Fatalf("height = %d, want 4 — length-1 literal branches must broadcast", df.Height())
	}
	p, err := df.Column[int64]("pass")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{1, 1, 0} {
		if v, ok := p.Get(i); !ok || v != want {
			t.Errorf("row %d = %v (valid %v), want %d", i, v, ok, want)
		}
	}
}

// TestCondNullLiteralBranch: Null(...) becomes data.NewNull, which carries NO
// payload buffer. Take refuses it and the string path panics on it, so a null
// branch must be routed through kernel.NullColumn before the merge.
func TestCondNullLiteralBranch(t *testing.T) {
	for _, tc := range []struct {
		name string
		then ursus.Expr
		null ursus.Expr
	}{
		{"int", ursus.Lit(int64(7)), ursus.Null(ursus.NullT)},
		{"string", ursus.Lit("x"), ursus.Null(ursus.String)},
		{"float", ursus.Lit(1.5), ursus.Null(ursus.Float64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df, err := cf().Select(
				ursus.When(ursus.Col("score").Ge(90)).Then(tc.then).
					Otherwise(tc.null).Alias("v"),
			).Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("a null branch must produce nulls, not an error: %v", err)
			}
			if df.Height() != 4 {
				t.Fatalf("height = %d, want 4", df.Height())
			}
		})
	}
}

// TestCoalesceFirstNonNull is the ordinary use, and the reason the desugaring's
// double evaluation is acceptable: both operands are plain column reads.
func TestCoalesceFirstNonNull(t *testing.T) {
	f := ursus.Frame(
		ursus.ValuesNullable("a", []int64{1, 0, 0}, []bool{true, false, false}),
		ursus.ValuesNullable("b", []int64{0, 2, 0}, []bool{false, true, false}),
		ursus.Values("c", []int64{7, 8, 9}),
	)
	df, err := f.Select(ursus.Coalesce(ursus.Col("a"), ursus.Col("b"), ursus.Col("c")).Alias("v")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	v, err := df.Column[int64]("v")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{1, 2, 9} {
		if got, ok := v.Get(i); !ok || got != want {
			t.Errorf("row %d = %v (valid %v), want %d", i, got, ok, want)
		}
	}
}

// TestCoalesceThroughMatch is the reason substitute needed a Cond arm.
//
// Expand rewrites a multi-column selector by substituting each name into a COPY of
// the tree, and substitute rebuilds per node type with a default arm that PANICS.
// Coalesce(All(), …) puts a *Match inside a *Cond, which is not an exotic thing to
// write — and without the arm it takes down the process rather than returning an
// error.
func TestCoalesceThroughMatch(t *testing.T) {
	f := ursus.Frame(
		ursus.ValuesNullable("a", []int64{1, 0}, []bool{true, false}),
		ursus.ValuesNullable("b", []int64{0, 2}, []bool{false, true}),
	)
	df, err := f.Select(ursus.Coalesce(ursus.All(), ursus.Lit(int64(-1)))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Width() != 2 {
		t.Fatalf("width = %d, want 2 — All() should expand to both columns", df.Width())
	}
	a, err := df.Column[int64]("a")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := a.Get(1); !ok || v != -1 {
		t.Errorf("a[1] = %v (valid %v), want -1", v, ok)
	}
}

// TestCondInsideGroupByAgg is conditional aggregation — how a pivot is written —
// and it exercises the extractAggs arm.
func TestCondInsideGroupByAgg(t *testing.T) {
	df, err := sales().
		GroupBy(ursus.Col("region")).
		Agg(
			ursus.When(ursus.Col("amount").Sum().Gt(60)).Then("big").
				Otherwise("small").Alias("size"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 {
		t.Fatalf("height = %d, want 3 regions", df.Height())
	}
	if !strings.Contains(df.Schema().String(), "size") {
		t.Errorf("schema %s should contain the aliased conditional", df.Schema())
	}
}

// TestCondRejectedInSelect: an aggregate anywhere inside a conditional is still an
// aggregate, and Select must refuse it. This works with no new code — HasAgg is a
// plain Walk — so the test pins behaviour rather than fixing it.
func TestCondRejectedInSelect(t *testing.T) {
	_, err := sales().Select(
		ursus.When(ursus.Col("qty").Gt(1)).Then(ursus.Col("amount").Sum()).Otherwise(0),
	).Collect(t.Context())
	if err == nil {
		t.Fatal("an aggregate inside a conditional must be rejected in Select")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}
	if !strings.Contains(err.Error(), "aggregate expression is not allowed") {
		t.Errorf("error should name the problem:\n%v", err)
	}
}

func TestCondRefusals(t *testing.T) {
	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		want string
	}{
		{"non-Boolean condition", func() *ursus.LazyFrame {
			return cf().Select(ursus.When(ursus.Col("a")).Then(1).Otherwise(0))
		}, "must be Boolean"},
		{"branches with no common type", func() *ursus.LazyFrame {
			return cf().Select(
				ursus.When(ursus.Col("score").Gt(0)).Then(ursus.Col("a")).Otherwise("text"))
		}, "no common type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf().Collect(t.Context())
			if err == nil {
				t.Fatal("expected a type error")
			}
			if !errors.Is(err, uerr.ErrType) {
				t.Errorf("kind should be Type: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should explain %q:\n%v", c.want, err)
			}
		})
	}
}

// TestCondBatchSizeAndThreadsAreIndependent: the conditional runs inside a
// projectOp, which parallelise hands to N workers. Nothing here may be
// batch-dependent or shared-mutable.
func TestCondBatchSizeAndThreadsAreIndependent(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return cf().Select(
			ursus.Col("id"),
			ursus.When(ursus.Col("score").Ge(90)).Then("A").
				When(ursus.Col("score").Ge(80)).Then("B").
				Otherwise("F").Alias("grade"),
			ursus.Coalesce(ursus.Col("score"), ursus.Lit(int64(-1))).Alias("filled"),
		)
	}
	ref, err := q().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := ref.String()

	for _, bs := range []int{1, 2, 3, 8192} {
		for _, th := range []int{1, 2, 4} {
			got, err := q().Collect(t.Context(),
				ursus.WithBatchSize(bs), ursus.WithThreads(th), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d threads %d: %v", bs, th, err)
			}
			if got.String() != want {
				t.Errorf("batch %d threads %d changed the answer:\n%s\nwant:\n%s",
					bs, th, got, want)
			}
		}
	}
}
