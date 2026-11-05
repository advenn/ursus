package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// bitsFrame is the frame the bits group's recorded cases hold: a [12, -1, None, 5],
// b [10, 3, 7, None], u, a Uint8, [12, 255, 0, 5], and i8, an Int8, [12, -1, 0, 5].
func bitsFrame() *ursus.LazyFrame {
	c := ursus.Col
	return ursus.Frame(
		ursus.ValuesNullable("a", []int64{12, -1, 0, 5}, []bool{true, true, false, true}),
		ursus.ValuesNullable("b", []int64{10, 3, 7, 0}, []bool{true, true, true, false}),
		ursus.Values("u", []int64{12, 255, 0, 5}), ursus.Values("i8", []int64{12, -1, 0, 5})).
		Select(c("a"), c("b"), c("u").Cast(ursus.Uint8), c("i8").Cast(ursus.Int8))
}

// TestIntegersAsBits is M2 of v0.6.6-scope.md (step 278): And, Or, Xor and Not of
// integers are their bits'. The answers are Polars 2.1.0's, recorded in the bits
// group of testdata/oracle and spelled in oracle_bits_test.go since step 497, which
// took this file's pasted values. What stays asserts the result types and the
// refusals.
func TestIntegersAsBits(t *testing.T) {
	c := ursus.Col
	a, b := c("a"), c("b")
	lf := bitsFrame()
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
