package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// bitsFrame is Polars' frame: a [12, -1, None, 5], b [10, 3, 7, None], u, a UInt8,
// [12, 255, 0, 5], and i8, an Int8, [12, -1, 0, 5].
func bitsFrame() *ursus.LazyFrame {
	c := ursus.Col
	return ursus.Frame(
		ursus.ValuesNullable("a", []int64{12, -1, 0, 5}, []bool{true, true, false, true}),
		ursus.ValuesNullable("b", []int64{10, 3, 7, 0}, []bool{true, true, true, false}),
		ursus.Values("u", []int64{12, 255, 0, 5}), ursus.Values("i8", []int64{12, -1, 0, 5})).
		Select(c("a"), c("b"), c("u").Cast(ursus.Uint8), c("i8").Cast(ursus.Int8))
}

// TestIntegersAsBits is M2 of v0.6.6-scope.md (step 278): And, Or, Xor and Not of
// integers are their bits'. Every answer is Polars 1.44.1's.
func TestIntegersAsBits(t *testing.T) {
	c := ursus.Col
	a, b := c("a"), c("b")
	lf := bitsFrame()
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"and", a.And(b), "8\n3\n\n"},
		{"or", a.Or(b), "14\n-1\n\n"},
		{"xor", a.Xor(b), "6\n-4\n\n"},
		{"not", a.Not(), "-13\n0\n\n-6"},
		{"not of a UInt8", c("u").Not(), "243\n0\n255\n250"},
		{"and a constant", a.And(6), "4\n6\n\n4"},
		{"a UInt8 and an Int8, in Int16", c("u").And(c("i8")), "12\n255\n0\n5"},
		{"a UInt8 or an Int64", c("u").Or(a), "12\n-1\n\n5"},
		{"not not", a.Not().Not(), "12\n-1\n\n5"},
	} {
		if got, want := csvOf(t, lf.Select(tc.e.Alias("r"))), "r\n"+tc.want+"\n"; got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, want)
		}
	}
	for _, tc := range []struct {
		e    ursus.Expr
		want ursus.DataType
	}{
		{c("u").And(c("i8")), ursus.Int16}, {c("i8").And(a), ursus.Int64}, {c("u").Not(), ursus.Uint8},
		{a.And(b), ursus.Int64},
		// A Go integer beside a UInt8 is a UInt8, as Polars keeps it.
		{c("u").And(6), ursus.Uint8},
	} {
		got, err := lf.Select(tc.e).Collect(t.Context())
		if err != nil || got.Schema().Field(0).Type != tc.want {
			t.Errorf("%s: %v, %v, want %s", tc.e, got, err, tc.want)
		}
	}
	i128 := ursus.Frame(ursus.Values("x", []int64{5})).Select(c("x").Cast(ursus.Int128))
	if got := csvOf(t, i128.Select(c("x").And(ursus.Lit(int64(3)).Cast(ursus.Int128)), c("x").Not().Alias("n"))); got != "x,n\n1,-6\n" {
		t.Errorf("Int128: %q", got)
	}
	// A null of no type beside an integer is a null integer.
	if got := csvOf(t, lf.Select(ursus.Null(ursus.NullT).And(a).Alias("r"))); got != "r\n\n\n\n\n" {
		t.Errorf("a null: %q", got)
	}
	// Booleans are Kleene logic, as before.
	bools := ursus.Frame(ursus.ValuesNullable("p", []bool{true, false, false}, []bool{true, true, false}))
	if got := csvOf(t, bools.Select(c("p").And(false).Alias("and"), c("p").Or(true).Alias("or"),
		c("p").Not().Alias("not"))); got != "and,or,not\nfalse,true,false\nfalse,true,true\nfalse,true,\n" {
		t.Errorf("Booleans: %q", got)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars deprecates a Boolean beside an integer, for the 2.0 that refuses it.
		"a Boolean and an integer": {lf.Select(a.Gt(0).And(a)), "mixes logic and bits"},
		"a float":                  {lf.Select(a.Cast(ursus.Float64).And(a)), "Boolean or integer operands"},
		"a float's not":            {lf.Select(a.Cast(ursus.Float64).Not()), "Boolean or an integer"},
		// Polars: "filter predicate must be of type `Boolean`, got `Int64`".
		"a filter of bits": {lf.Filter(a.And(b)), "Bool"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
