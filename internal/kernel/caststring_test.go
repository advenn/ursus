package kernel_test

// TestCastToStringAgreesWithCanCast is the differential this file already runs in
// the other direction, run the way that was missing.
//
// namespace_test.go has TestNullCastsAgreeWithCanCast and
// TestStringCastsAgreeWithCanCast, both of which check String -> other. Nothing
// checked other -> String, and formatToString — the kernel behind every
// Cast(ursus.String) — had no test at all. It showed:
//
//	Int128  -> String   FAILED outright, and Int128 is what every integer Sum
//	                    outputs, so Col("x").Sum().Cast(String) was an ordinary
//	                    query that did not work
//	Uint64  -> String   routed through float64 and rounded above 2^53, and Count,
//	                    Len, NUnique and NullCount all output Uint64
//	Decimal -> String   was refused although the scale is known and the conversion
//	                    is exact
//
// All three were promises dtype.CanCast made that the kernel did not keep — the
// same class unary.go's own comment says was closed for string and null casts.

import (
	"math"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// One integer of each sign: the narrower widths share formatInts and formatUints by
// type parameter. A float's text is pinned against Polars by the root package's
// TestCastsAsPolars, and a Date's by the calendar tests that cast one to text.
func TestCastToStringAgreesWithCanCast(t *testing.T) {
	cases := []struct {
		dt   dtype.DataType
		col  func() *data.Column
		want string
	}{
		{dtype.Int64, func() *data.Column {
			return data.NewFixed("v", dtype.Int64, []int64{math.MinInt64}, bitmap.AllSet(1))
		}, "-9223372036854775808"},
		// Above 2^53. This is the one the float64 detour got wrong.
		{dtype.Uint64, func() *data.Column {
			return data.NewFixed("v", dtype.Uint64, []uint64{math.MaxUint64}, bitmap.AllSet(1))
		}, "18446744073709551615"},
		// Int128 is what every integer Sum produces.
		{dtype.Int128, func() *data.Column {
			return data.NewFixed("v", dtype.Int128,
				[]i128.Int128{{Hi: 1, Lo: 0}}, bitmap.AllSet(1))
		}, "18446744073709551616"},
		{dtype.Decimal(10, 2), func() *data.Column {
			return data.NewFixed("v", dtype.Decimal(10, 2),
				[]i128.Int128{i128.FromInt64(1234)}, bitmap.AllSet(1))
		}, "12.34"},
		{dtype.Bool, func() *data.Column {
			bits := bitmap.NewBuilder(1)
			bits.Append(true)
			return data.NewBool("v", bits.Finish(), bitmap.AllSet(1))
		}, "true"},
	}

	for _, c := range cases {
		t.Run(c.dt.String(), func(t *testing.T) {
			if !dtype.CanCast(c.dt, dtype.String) {
				t.Skipf("CanCast says no, so the kernel is free to refuse")
			}
			got, err := kernel.Cast("s", dtype.String, true, c.col())
			if err != nil {
				t.Fatalf("CanCast(%s, String) is true but the kernel refused: %v", c.dt, err)
			}
			if got.DType() != dtype.String {
				t.Fatalf("result is %s, want String", got.DType())
			}
			if v := got.Strings().Get(0); v != c.want {
				t.Errorf("%s formatted as %q, want %q", c.dt, v, c.want)
			}
		})
	}
}

// TestCastToStringPreservesNulls: a null formats as a null, not as the empty
// string — which would be indistinguishable from a genuine "" on the way back.
func TestCastToStringPreservesNulls(t *testing.T) {
	c := data.NewFixed("v", dtype.Int64, []int64{0, 7}, bitmap.Zeros(2))
	got, err := kernel.Cast("s", dtype.String, true, c)
	if err != nil {
		t.Fatal(err)
	}
	if got.NullCount() != 2 {
		t.Errorf("null count = %d, want 2", got.NullCount())
	}
}
