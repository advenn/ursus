package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestHaving is G2 of v0.6.7-scope.md (step 309): Polars' GroupBy.having, the groups
// whose predicates, aggregates of their own, all hold; a null one left out. It
// filters Agg, the shorthands, Head and Tail, and a dynamic group's windows. The
// groups kept are the having group's, Polars 2.1.0's with maintain_order=True
// (oracle_having_test.go); here stay the keyless frame, one group kept or left out
// whole, and the refusals.
func TestHaving(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(strs("k", "a", "b", "a", "b", "c", nil, nil), i64s("v", 1, 2, 3, 4, 5, nil, 7),
		f64s("w", 1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0))
	g := func() *ursus.GroupBy { return lf.GroupBy(c("k")).MaintainOrder() }
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		// No keys: the frame is one group, kept or not.
		{"no keys, kept", lf.GroupBy().Having(ursus.Len().Gt(1)).Head(2), lf.Head(2)},
		{"no keys, left out", lf.GroupBy().Having(ursus.Len().Gt(10)).Head(2), lf.Head(0)},
	} {
		got, err := tc.q.Collect(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want, err := tc.want.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(tc.name, func(t *testing.T) { ursustest.AssertFrameEqual(t, got, want) })
	}

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			// Polars: cannot cast List type (inner: 'Boolean', to: 'Boolean').
			{"a predicate of rows", g().Having(c("v").Gt(1)).Agg(ursus.Len()),
				`having's predicate (col("v") > lit(1)) is not an aggregate of its group`},
			// Polars keeps every group whose sum is not 0, casting it.
			{"a predicate not Boolean", g().Having(c("v").Sum()).Agg(ursus.Len()),
				`having's predicate col("v").sum() is Int128, where a group's predicate is Boolean`},
			{"an unknown column", g().Having(c("nope").Sum().Gt(1)).Agg(ursus.Len()), `"nope"`},
		} {
			_, err := tc.q.Collect(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})
}

// TestLenKeepsItsNameThroughACast (step 309): an expression that reads no column is
// named as its first input is, as Polars names it, so Len() cast or added to is
// "len"; a literal's is "literal". It was "literal" for every one of them, which
// TestHaving's casts of Len found.
func TestLenKeepsItsNameThroughACast(t *testing.T) {
	lf := ursus.Frame(i64s("k", 1, 1, 2))
	for _, tc := range []struct {
		e    ursus.Expr
		want string
	}{
		{ursus.Len().Cast(ursus.Int64), "len"},
		{ursus.Len().Add(1), "len"},
		{ursus.Len().Cast(ursus.Int64).Mul(2), "len"},
		{ursus.Lit(int64(1)).Cast(ursus.Int32), "literal"},
	} {
		s, err := lf.Select(tc.e).CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Names()[0]; got != tc.want {
			t.Errorf("%s is named %q, want %q", tc.e, got, tc.want)
		}
	}
}
