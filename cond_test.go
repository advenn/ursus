package ursus_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
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
	// All() is the call's operands, as Polars' coalesce(pl.all(), pl.lit(-1)) is:
	// one column, named after the first, each row's first value across a, b and -1
	// (step 302; before, each column was coalesced with -1 on its own).
	df, err := f.Select(ursus.Coalesce(ursus.All(), ursus.Lit(int64(-1)))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Width() != 1 {
		t.Fatalf("width = %d, want one column", df.Width())
	}
	a, err := df.Column[int64]("a")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Values(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("a = %v, want [1 2]", got)
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

// TestAnAggregateInsideAConditionalBroadcasts: an aggregate inside a conditional in
// Select was refused; since step 250 it is the frame's, broadcast to every row, as
// Polars has it: rows with qty over 1 get the total amount, 150, and the rest 0.
func TestAnAggregateInsideAConditionalBroadcasts(t *testing.T) {
	df, err := sales().Select(
		ursus.When(ursus.Col("qty").Gt(1)).Then(ursus.Col("amount").Sum()).Otherwise(0).Alias("s"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s, err := df.Column[float64]("s")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Values(); !slices.Equal(got, []float64{0, 150, 150, 150, 150, 150, 150}) {
		t.Errorf("got %v", got)
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

// TestConditionalOfNestedTypes (step 310): a conditional of Lists or Structs, each
// row a branch's, where it refused, "a conditional is not implemented for
// List(String)": Polars' when/then of them, and a Coalesce of Lists. A row whose
// condition is null is null here, where Polars takes Otherwise (feature-gaps.md §1).
func TestConditionalOfNestedTypes(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.ValuesNullable("b", []bool{true, false, false}, []bool{true, true, false}),
		ursus.ValuesNullable("s", []string{"1", "2,3", ""}, []bool{true, true, false}),
		ursus.Values("x", []int64{1, 2, 3})).
		WithColumns(c("s").Str().Split(",").List().Eval(ursus.Element().Cast(ursus.Int64)).Alias("l"),
			ursus.Struct(c("x")).Alias("st"))
	text := func(e ursus.Expr) ursus.Expr {
		return e.List().Eval(ursus.Element().Cast(ursus.String)).List().Join(",")
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: [[1], [9], [9]].
		{"a List", lf.Select(text(ursus.When(c("b")).Then(c("l")).Otherwise(ursus.Lit([]int64{9}))).Alias("w")),
			"w\n1\n9\n\n"},
		// Polars: [{x: 1}, {x: 0}, {x: 0}].
		{"a Struct", lf.Select(ursus.When(c("b")).Then(c("st")).Otherwise(ursus.Struct(ursus.Lit(int64(0)).Alias("x"))).
			Struct().Field("x").Alias("w")), "w\n1\n0\n\n"},
		// Polars: [[1], [2, 3], [7]].
		{"a Coalesce of Lists", lf.Select(text(ursus.Coalesce(c("l"), ursus.Lit([]int64{7}))).Alias("c")),
			"c\n1\n\"2,3\"\n7\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
