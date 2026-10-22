package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestHaving is G2 of v0.6.7-scope.md (step 309): Polars' GroupBy.having, the groups
// whose predicates, aggregates of their own, all hold; a null one left out. It
// filters Agg, the shorthands, Head and Tail, and a dynamic group's windows. Every
// answer is Polars 1.44.1's with maintain_order=True; a sum is cast to the Int64
// Polars' is, ursus's being an Int128.
func TestHaving(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(strs("k", "a", "b", "a", "b", "c", nil, nil), i64s("v", 1, 2, 3, 4, 5, nil, 7),
		f64s("w", 1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0))
	g := func() *ursus.GroupBy { return lf.GroupBy(c("k")).MaintainOrder() }
	sum := c("v").Sum().Cast(ursus.Int64)
	frame := ursus.Frame
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want *ursus.LazyFrame
	}{
		{"len > 1", g().Having(ursus.Len().Gt(1)).Agg(sum), frame(strs("k", "a", "b", nil), i64s("v", 4, 6, 7))},
		{"a sum over 3", g().Having(c("v").Sum().Gt(3)).Agg(c("w").Mean()),
			frame(strs("k", "a", "b", "c", nil), f64s("w", 2.0, 3.0, 5.0, 6.5))},
		{"two predicates", g().Having(ursus.Len().Gt(1), c("v").Max().Lt(4)).Agg(sum), frame(strs("k", "a"), i64s("v", 4))},
		// Chained, both hold: c's max is under 6, but it is one row.
		{"chained", g().Having(ursus.Len().Gt(1)).Having(c("v").Max().Lt(6)).Agg(sum),
			frame(strs("k", "a", "b"), i64s("v", 4, 6))},
		// The null group's first v is null, and the group is left out.
		{"a null predicate", g().Having(c("v").First().Gt(0)).Agg(ursus.Len().Cast(ursus.Int64)),
			frame(strs("k", "a", "b", "c"), i64s("len", 2, 2, 1))},
		{"a predicate of a column not aggregated", g().Having(c("w").Min().Gt(2)).Agg(sum),
			frame(strs("k", "c", nil), i64s("v", 5, 7))},
		{"no aggregate", g().Having(ursus.Len().Gt(1)).Agg(), frame(strs("k", "a", "b", nil))},
		{"Sum", g().Having(ursus.Len().Gt(1)).Sum().WithColumns(c("v").Cast(ursus.Int64)),
			frame(strs("k", "a", "b", nil), i64s("v", 4, 6, 7), f64s("w", 4.0, 6.0, 13.0))},
		{"Len", g().Having(ursus.Len().Gt(1)).Len("len").WithColumns(c("len").Cast(ursus.Int64)),
			frame(strs("k", "a", "b", nil), i64s("len", 2, 2, 2))},
		{"Head", g().Having(ursus.Len().Gt(1)).Head(1),
			frame(strs("k", "a", "b", nil), i64s("v", 1, 2, nil), f64s("w", 1.0, 2.0, 6.0))},
		{"Tail", g().Having(c("v").Sum().Gt(4)).Tail(1),
			frame(strs("k", "b", "c", nil), i64s("v", 4, 5, 7), f64s("w", 4.0, 5.0, 7.0))},
		{"a literal true", g().Having(ursus.Lit(true)).Agg(ursus.Len().Cast(ursus.Int64)),
			frame(strs("k", "a", "b", "c", nil), i64s("len", 2, 2, 1, 2))},
		{"every group left out", g().Having(ursus.Len().Gt(10)).Agg(ursus.Len().Cast(ursus.Int64)),
			frame(strs("k"), i64s("len"))},
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

	t.Run("a dynamic group", func(t *testing.T) {
		days := ursus.Frame(ursus.Values("t", []time.Time{at(2024, 1, 1, 0), at(2024, 1, 2, 0), at(2024, 1, 3, 0),
			at(2024, 1, 6, 0)}), i64s("v", 1, 2, 3, 4)).WithColumns(c("t").Cast(ursus.Date))
		got, err := days.GroupByDynamic(c("t"), ursus.DynamicOptions{Every: ursus.Every("2d")}).
			Having(ursus.Len().Gt(1)).Agg(sum).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Polars: [(2024-01-02, 5)], its windows of Dec 31 and Jan 6 a row each.
		want, err := ursus.Frame(ursus.Values("t", []time.Time{at(2024, 1, 2, 0)}), i64s("v", 5)).
			WithColumns(c("t").Cast(ursus.Date)).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	})

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
