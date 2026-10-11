package ursus_test

// Step 179: audit rows O13 and O12. (I23 is writerstats_test.go.)

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// TestTypesThatRenderedAlikeStayApart: O13. Enum("a, b") and Enum("a", "b") both
// rendered Enum(a, b), and deduplication keys on renderings: two casts of one column,
// one to each, became one aggregate, which failed as ursus's bug, or one window
// function, whose second answer was silently the first's.
func TestTypesThatRenderedAlikeStayApart(t *testing.T) {
	c := ursus.Col
	one, two := dtype.Enum("a, b"), dtype.Enum("a", "b")
	if one.String() == two.String() {
		t.Fatalf("both render %s", one)
	}
	frame := ursus.Frame(ursus.Values("s", []string{"a", "b"}))
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		x, y string
	}{
		{"an aggregate", frame.GroupBy().Agg(c("s").CastLossy(one).First().Alias("x"), c("s").CastLossy(two).First().Alias("y")),
			"∅ ", "a "},
		{"a window", frame.Select(c("s").CastLossy(one).First().Over(ursus.Lit(1)).Alias("x"),
			c("s").CastLossy(two).First().Over(ursus.Lit(1)).Alias("y")), "∅ ∅ ", "a a "},
		{"an ordered window function", frame.Select(c("s").CastLossy(one).ForwardFill(0).Alias("x"),
			c("s").CastLossy(two).ForwardFill(0).Alias("y")), "∅ ∅ ", "a b "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df, err := tc.lf.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if x, y := rendered172(t, df, "x"), rendered172(t, df, "y"); x != tc.x || y != tc.y {
				t.Errorf("x %q, y %q, want %q and %q", x, y, tc.x, tc.y)
			}
		})
	}
}

func TestTypeRenderingQuotesOnlyWhatItMust(t *testing.T) {
	for _, tc := range []struct {
		dt   dtype.DataType
		want string
	}{
		{dtype.Enum("a", "b"), "Enum(a, b)"},
		{dtype.Enum("a, b"), `Enum("a, b")`},
		{dtype.Enum("low", "high", "N/A", "two words"), "Enum(low, high, N/A, two words)"},
		{dtype.Enum("", " x", "(", `"`), `Enum("", " x", "(", "\"")`},
		{dtype.Struct(dtype.Field{Name: "a", Type: dtype.Int64, Nullable: true}), "Struct(a: Int64)"},
		{dtype.Struct(dtype.Field{Name: "a: Int64, b", Type: dtype.String, Nullable: true}), `Struct("a: Int64, b": String)`},
		{dtype.List(dtype.Enum("x)")), `List(Enum("x)"))`},
	} {
		if got := tc.dt.String(); got != tc.want {
			t.Errorf("%q, want %q", got, tc.want)
		}
	}
}

// TestAFoldedOperandsErrorIsKept: O12. `x OR true` is true for every value of x, but
// a strict cast in x that fails on a row is an error the query has, as Polars' raises
// it; it is folded only when x cannot fail.
func TestAFoldedOperandsErrorIsKept(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("s", []string{"x", "1"}), ursus.Values("i", []int64{1, 2}))
	failing := c("s").Cast(ursus.Int64).Gt(0)
	for name, e := range map[string]ursus.Expr{
		"or true":   failing.Or(ursus.Lit(true)),
		"and false": failing.And(ursus.Lit(false)),
		"true or":   ursus.Lit(true).Or(failing),
	} {
		_, err := frame.Select(e.Alias("r")).Collect(t.Context())
		if err == nil || errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "cast") {
			t.Errorf("%s: %v, want the cast's error", name, err)
		}
	}

	// An operand that cannot fail is folded away.
	lf := frame.Select(c("i").Gt(0).Or(ursus.Lit(true)).Alias("r"))
	plan, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan, `col("i")`) {
		t.Errorf("an infallible operand of OR true was kept:\n%s", plan)
	}
}
