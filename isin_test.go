package ursus_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

func TestIsInSelectsMembers(t *testing.T) {
	df, err := frame().
		Filter(ursus.Col("region").IsIn("eu", "apac")).
		Select(ursus.Col("id"), ursus.Col("region")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	id, err := df.Column[int64]("id")
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{1, 3, 4, 6}
	if df.Height() != len(want) {
		t.Fatalf("height = %d, want %d\n%s", df.Height(), len(want), df)
	}
	for i, w := range want {
		if v, _ := id.Get(i); v != w {
			t.Errorf("row %d id = %d, want %d", i, v, w)
		}
	}
}

// TestIsInEmptySetMatchesNothing pins SQL's `x IN ()`.
func TestIsInEmptySetMatchesNothing(t *testing.T) {
	df, err := frame().
		Filter(ursus.Col("region").IsIn[string]()).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 0 {
		t.Errorf("height = %d, want 0 — an empty set matches nothing\n%s", df.Height(), df)
	}
}

// TestIsInNullIsNull: a missing value is not a member and is not a non-member.
// Filter drops it either way, so the visible consequence is that IsIn and its
// negation do not partition — the same property comparison has.
func TestIsInNullIsNull(t *testing.T) {
	df, err := frame().
		Select(
			ursus.Col("id"),
			ursus.Col("discount").IsIn(0.1, 0.25).Alias("hit"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	hit, err := df.Column[bool]("hit")
	if err != nil {
		t.Fatal(err)
	}
	// discount is [0.1, null, 0.25, null, 0.5, null].
	if v, ok := hit.Get(0); !ok || !v {
		t.Errorf("row 0 = %v (valid %v), want true", v, ok)
	}
	if v, ok := hit.Get(1); ok {
		t.Errorf("row 1 = %v, want null: null is not a member and not a non-member", v)
	}
	if v, ok := hit.Get(4); !ok || v {
		t.Errorf("row 4 = %v (valid %v), want false", v, ok)
	}

	// The partition property, stated directly.
	kept, err := frame().Filter(ursus.Col("discount").IsIn(0.1)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := frame().Filter(ursus.Col("discount").IsIn(0.1).Not()).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if kept.Height()+dropped.Height() == 6 {
		t.Error("IsIn and its negation must NOT partition: the three null rows " +
			"belong to neither side")
	}
}

// TestIsInUsesGroupingEquality is the reason InSet goes through
// kernel.NewGroupKeyEncoder rather than comparing values directly.
//
// IEEE says NaN != NaN and -0.0 == +0.0. ursus's grouping equality says NaN == NaN
// and -0.0 == +0.0. If IsIn used IEEE, `x.IsIn(NaN)` would match nothing while
// GroupBy(x) and Distinct happily group the NaNs together — two parts of the same
// engine disagreeing about whether two values are the same value.
func TestIsInUsesGroupingEquality(t *testing.T) {
	nan := math.NaN()
	negZero := math.Copysign(0, -1)

	// Duplicates on purpose: two NaNs and both signed zeros, so Unique has
	// something to collapse and the cross-check below is not vacuous.
	f := ursus.Frame(ursus.Values("v", []float64{nan, nan, 1, negZero, 0, 2}))

	df, err := f.Select(
		ursus.Col("v").IsIn(nan).Alias("isNaN"),
		ursus.Col("v").IsIn(0.0).Alias("isZero"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	isNaN, _ := df.Column[bool]("isNaN")
	isZero, _ := df.Column[bool]("isZero")

	if v, ok := isNaN.Get(0); !ok || !v {
		t.Errorf("NaN.IsIn(NaN) = %v (valid %v), want true — IEEE equality would say "+
			"false and disagree with GroupBy", v, ok)
	}
	if v, ok := isNaN.Get(1); !ok || !v {
		t.Errorf("the second NaN must match too, got %v (valid %v)", v, ok)
	}
	if v, ok := isZero.Get(3); !ok || !v {
		t.Errorf("(-0.0).IsIn(0.0) = %v (valid %v), want true", v, ok)
	}
	if v, ok := isNaN.Get(2); !ok || v {
		t.Errorf("1.IsIn(NaN) = %v (valid %v), want false", v, ok)
	}

	// The cross-check that makes the claim concrete: Distinct groups these the same
	// way, so a value IsIn its own column's distinct values, always.
	distinct, err := f.Unique().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Six rows collapse to four: the two NaNs become one, and -0.0 and 0.0 become
	// one. IsIn agreed with exactly that above.
	if distinct.Height() != 4 {
		t.Errorf("Unique height = %d, want 4 (the two NaNs collapse, and so do "+
			"-0.0 and 0.0)\n%s", distinct.Height(), distinct)
	}
}

func TestIsInRefusals(t *testing.T) {
	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		kind error
		want string
	}{
		{"unrepresentable value", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Col("qty").IsIn(int64(5_000_000_000)))
		}, uerr.ErrValue, "not representable as Int32"},
		{"unparseable text against a number", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Col("qty").IsIn("abc"))
		}, uerr.ErrValue, "abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf().Collect(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, c.kind) {
				t.Errorf("wrong kind: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should name %q:\n%v", c.want, err)
			}
		})
	}
}

// TestIsInIsParallelSafe: the probe set is built once and cached on the node, then
// read by N workers concurrently — the same arrangement step 6 made for compiled
// regexes. A map that were still being written during evaluation would be a
// concurrent-map-write throw, not a race the detector merely reports.
func TestIsInIsParallelSafe(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return frame().Select(
			ursus.Col("id"),
			ursus.Col("region").IsIn("eu", "us").Alias("a"),
			ursus.Col("qty").IsIn(int32(10), int32(40)).Alias("b"),
		)
	}
	ref, err := q().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := ref.String()

	for _, bs := range []int{1, 2, 8192} {
		for _, th := range []int{1, 2, 4, 8} {
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
