package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// M5 of v0.6.6-scope.md (step 283): round's modes, truncate, round_sig_figs, cot and
// the one-sided clips. Every answer is Polars 1.44.1's, compared by its bits, so -0
// is not 0 and a float's last bit counts.

// typedCells is the first column's values, a null nil, read as T.
func typedCells[T ursus.Literal](t *testing.T, q *ursus.LazyFrame) []any {
	t.Helper()
	df, err := q.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	col, err := df.Column[T](df.Schema().Field(0).Name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]any, df.Height())
	for i := range out {
		if v, ok := col.Get(i); ok {
			out[i] = v
		}
	}
	return out
}

// sameCells compares cells, a float by its bits, NaN with NaN.
func sameCells(got, want []any) bool {
	if len(got) != len(want) {
		return false
	}
	for i, w := range want {
		switch w := w.(type) {
		case float64:
			g, ok := got[i].(float64)
			if !ok || math.Float64bits(g) != math.Float64bits(w) && !(math.IsNaN(g) && math.IsNaN(w)) {
				return false
			}
		case float32:
			g, ok := got[i].(float32)
			if !ok || math.Float32bits(g) != math.Float32bits(w) && !(g != g && w != w) {
				return false
			}
		default:
			if got[i] != w {
				return false
			}
		}
	}
	return true
}

func TestRoundModesAndTruncate(t *testing.T) {
	c := ursus.Col
	x := c("x")
	nan, inf, negz := math.NaN(), math.Inf(1), math.Copysign(0, -1)
	frame := func(v []float64, valid ...bool) *ursus.LazyFrame {
		if valid == nil {
			return ursus.Frame(ursus.Values("x", v))
		}
		return ursus.Frame(ursus.ValuesNullable("x", v, valid))
	}
	ties := frame([]float64{0.5, 1.5, 2.5, -0.5, -2.5, 2.675, 0.125, 1e300, nan, 0, -0.4},
		true, true, true, true, true, true, true, true, true, false, true)
	exact := frame([]float64{1.005, 1.015, 1.025, 0.285, 1.0000000000000002, 4503599627370497.0, 4503599627370496.5})
	sub := frame([]float64{1.5e-310, 2.5e-310, -1.5e-310, 1.25e-305, 0.1, 3.0})
	trunc := frame([]float64{0.25, -0.25, 0.99, -0.99, 2.675, -2.675, 1e300, nan, inf, 0, negz, 1.5e300, 123.456, 0.29},
		true, true, true, true, true, true, true, true, true, false, true, true, true, true)
	// 0.30000000000000004 as a literal: Go folds the constant 0.1 + 0.2 exactly, to 0.3.
	thirds := frame([]float64{0.30000000000000004, 1.0 / 3, 2.0 / 3, 123456.789e-10})
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"to even, 0", ties.Select(x.Round(0, ursus.RoundHalfToEven)),
			[]any{0.0, 2.0, 2.0, negz, -2.0, 3.0, 0.0, 1e300, nan, nil, negz}},
		{"to even, 2", ties.Select(x.Round(2, ursus.RoundHalfToEven)),
			[]any{0.5, 1.5, 2.5, -0.5, -2.5, 2.68, 0.12, 1e300, nan, nil, -0.4}},
		{"away, 0", ties.Select(x.Round(0)), []any{1.0, 2.0, 3.0, -1.0, -3.0, 3.0, 0.0, 1e300, nan, nil, negz}},
		{"away, 2", ties.Select(x.Round(2, ursus.RoundHalfAwayFromZero)),
			[]any{0.5, 1.5, 2.5, -0.5, -2.5, 2.68, 0.13, 1e300, nan, nil, -0.4}},
		{"to even, the floats there are", exact.Select(x.Round(2, ursus.RoundHalfToEven)),
			[]any{1.0, 1.01, 1.02, 0.28, 1.0, 4503599627370497.0, 4503599627370496.0}},
		{"to even, 0, past 2^52", exact.Select(x.Round(0, ursus.RoundHalfToEven)),
			[]any{1.0, 1.0, 1.0, 0.0, 1.0, 4503599627370497.0, 4503599627370496.0}},
		{"15 places", thirds.Select(x.Round(15, ursus.RoundHalfToEven)),
			[]any{0.3, 0.333333333333333, 0.666666666666667, 1.23456789e-05}},
		{"23 places, past 10^22", thirds.Select(x.Round(23, ursus.RoundHalfToEven)),
			[]any{0.30000000000000004, 0.3333333333333333, 0.6666666666666666, 1.23456789e-05}},
		{"300 places, 2^d and 5^d apart", sub.Select(x.Round(300, ursus.RoundHalfToEven)),
			[]any{0.0, 0.0, negz, 0.0, 0.09999999999999999, 3.0}},
		{"310 places", sub.Select(x.Round(310, ursus.RoundHalfToEven)),
			[]any{2e-310, 3e-310, -2e-310, 1.2499999999999992e-305, 0.1, 3.0}},
		{"310 places away", sub.Select(x.Round(310)), []any{2e-310, 3e-310, -2e-310, 1.2499999999999992e-305, 0.1, 3.0}},
		{"325 places", sub.Select(x.Round(325, ursus.RoundHalfToEven)), []any{1.5e-310, 2.5e-310, -1.5e-310, 1.25e-305, 0.1, 3.0}},
		{"326 places, as it is", sub.Select(x.Round(326)), []any{1.5e-310, 2.5e-310, -1.5e-310, 1.25e-305, 0.1, 3.0}},
		{"20 places of 1.5e300, its own", frame([]float64{1.5e300, 0.1}).Select(x.Round(20, ursus.RoundHalfToEven)),
			[]any{1.5e300, 0.1}},
		{"truncate, 0", trunc.Select(x.Truncate(0)),
			[]any{0.0, negz, 0.0, negz, 2.0, -2.0, 1e300, nan, inf, nil, negz, 1.5e300, 123.0, 0.0}},
		{"truncate, 1", trunc.Select(x.Truncate(1)),
			[]any{0.2, -0.2, 0.9, -0.9, 2.6, -2.6, 1e300, nan, inf, nil, negz, 1.5e300, 123.4, 0.2}},
		{"truncate, 2", trunc.Select(x.Truncate(2)),
			[]any{0.25, -0.25, 0.99, -0.99, 2.67, -2.67, 1e300, nan, inf, nil, negz, 1.5e300, 123.45, 0.28}},
		{"truncate, 20", trunc.Select(x.Truncate(20)),
			[]any{0.25, -0.25, 0.99, -0.99, 2.675, -2.675, 1e300, nan, inf, nil, negz, 1.5e300, 123.456, 0.29}},
		{"truncate, 30", thirds.Select(x.Truncate(30)),
			[]any{0.30000000000000004, 0.3333333333333333, 0.6666666666666666, 1.23456789e-05}},
		{"truncate, 310", sub.Select(x.Truncate(310)), []any{1e-310, 2e-310, -1e-310, 1.2499999999999992e-305, 0.1, 3.0}},
	} {
		if got := typedCells[float64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	f32 := ursus.Frame(ursus.Values("x", []float32{0.125, 2.5, 1e-30}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"a Float32 to even, 2", f32.Select(x.Round(2, ursus.RoundHalfToEven)), []any{float32(0.11999999731779099), float32(2.5), float32(0)}},
		{"a Float32 to even, 0", f32.Select(x.Round(0, ursus.RoundHalfToEven)), []any{float32(0), float32(2), float32(0)}},
		{"a Float32, 25", f32.Select(x.Round(25, ursus.RoundHalfToEven)), []any{float32(0.125), float32(2.5), float32(0)}},
		{"a Float32, 46", f32.Select(x.Round(46, ursus.RoundHalfToEven)), []any{float32(0.125), float32(2.5), float32(1e-30)}},
		{"a Float32, 47, as it is", f32.Select(x.Round(47, ursus.RoundHalfToEven)), []any{float32(0.125), float32(2.5), float32(1e-30)}},
		{"a Float32 truncated", ursus.Frame(ursus.Values("x", []float32{0.29, -2.675})).Select(x.Truncate(2)),
			[]any{float32(0.2800000011920929), float32(-2.6700000762939453)}},
	} {
		if got := typedCells[float32](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	ints := ursus.Frame(ursus.Values("x", []int64{5, -5}))
	if got := typedCells[int64](t, ints.Select(x.Truncate(1))); !sameCells(got, []any{int64(5), int64(-5)}) {
		t.Errorf("an integer truncated: %v", got)
	}
	if got := typedCells[int64](t, ints.Select(x.Round(1, ursus.RoundHalfToEven))); !sameCells(got, []any{int64(5), int64(-5)}) {
		t.Errorf("an integer rounded to even: %v", got)
	}
	// The default's plan is the one it always had; a mode is the call's argument.
	if got := x.Round(2, ursus.RoundHalfAwayFromZero).String(); got != x.Round(2).String() {
		t.Errorf("an explicit default changed the plan: %s", got)
	}
	if got := x.Round(2, ursus.RoundHalfToEven).String(); !strings.Contains(got, "half_to_even") {
		t.Errorf("the mode is not in the plan: %s", got)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"a mode that is none":   {ties.Select(x.Round(1, "up")), "not a rounding mode"},
		"two modes":             {ties.Select(x.Round(1, ursus.RoundHalfToEven, ursus.RoundHalfToEven)), "one mode"},
		"truncate to -1":        {ties.Select(x.Truncate(-1)), "must not be negative"},
		"truncate of text":      {ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.Truncate(0)), "requires a number"},
		"round to even of text": {ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.Round(0, ursus.RoundHalfToEven)), "numeric"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

func TestRoundSigFigs(t *testing.T) {
	c := ursus.Col
	x := c("x")
	nan, inf, negz := math.NaN(), math.Inf(1), math.Copysign(0, -1)
	mixed := ursus.Frame(ursus.ValuesNullable("x",
		[]float64{1234.5678, -0.0012345, 0, negz, nan, inf, 0, 9.995, 1e-320, math.MaxFloat64},
		[]bool{true, true, true, true, true, true, false, true, true, true}))
	edges := ursus.Frame(ursus.Values("x", []float64{1.5e-315, 1.7e308, 9.6e307, 0.95, 99.5, 123.456, 1e-5,
		5e-324, 2.5e-308, 8.5e307, -1.7e308, 1.23456789e-310}))
	tens := ursus.Frame(ursus.Values("x", []float64{1000, 100, 0.001, 1e23, 1e22, 1e-7, 999.9999999999999, 1e308, 1e-300}))
	halves := ursus.Frame(ursus.Values("x", []float64{0.125, 0.135, 2.5, 3.5, 15, 25, -2.5}))
	many := ursus.Frame(ursus.Values("x", []float64{1000, 1e5, 1e-3, 1e10, 1e20, 1e-10, 100}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"1 figure", mixed.Select(x.RoundSigFigs(1)),
			[]any{1000.0, -0.001, 0.0, negz, nan, inf, nil, 10.0, 1e-320, math.MaxFloat64}},
		{"2 figures", mixed.Select(x.RoundSigFigs(2)),
			[]any{1200.0, -0.0012, 0.0, negz, nan, inf, nil, 10.0, 1e-320, math.MaxFloat64}},
		{"3 figures", mixed.Select(x.RoundSigFigs(3)),
			[]any{1230.0, -0.00123, 0.0, negz, nan, inf, nil, 9.99, 1e-320, math.MaxFloat64}},
		{"edges, 1", edges.Select(x.RoundSigFigs(1)), []any{2e-315, 1.7e308, 1.0000000000000004e308, 1.0, 100.0, 100.0,
			1e-5, 5e-324, 2.999999999999998e-308, 8.000000000000004e307, -1.7e308, 1e-310}},
		{"edges, 2", edges.Select(x.RoundSigFigs(2)), []any{1.5e-315, 1.7000000000000007e308, 9.600000000000007e307, 0.95,
			100.0, 120.0, 1e-5, 5e-324, 2.499999999999999e-308, 8.500000000000005e307, -1.7000000000000007e308, 1.2e-310}},
		{"edges, 20", edges.Select(x.RoundSigFigs(20)), []any{1.5e-315, 1.7e308, 9.6e307, 0.95, 99.5, 123.456, 1e-5,
			5e-324, 2.5e-308, 8.5e307, -1.7e308, 1.23456789e-310}},
		{"powers of ten, 1", tens.Select(x.RoundSigFigs(1)),
			[]any{1000.0, 100.0, 0.001, 1e23, 1e22, 1e-7, 1000.0, 1.0000000000000006e308, 9.999999999999994e-301}},
		{"powers of ten, 3", tens.Select(x.RoundSigFigs(3)),
			[]any{1000.0, 100.0, 0.001, 1.0000000000000001e23, 1e22, 1e-7, 1000.0, 1.0000000000000006e308, 9.999999999999995e-301}},
		{"powers of ten, 17", tens.Select(x.RoundSigFigs(17)),
			[]any{1000.0, 100.0, 0.001, 1e23, 1e22, 9.999999999999998e-08, 999.9999999999998, 1e308, 1e-300}},
		// Each power of ten's own exponent, as a correctly rounded log10 gives it.
		{"powers of ten, 17 figures", many.Select(x.RoundSigFigs(17)),
			[]any{1000.0, 100000.0, 0.001, 1e10, 1e20, 9.999999999999999e-11, 100.0}},
		{"powers of ten, 20 figures", many.Select(x.RoundSigFigs(20)), []any{1000.0, 100000.0, 0.001, 1e10, 1e20, 1e-10, 100.0}},
		{"powers of ten, 23 figures", many.Select(x.RoundSigFigs(23)),
			[]any{1000.0, 100000.0, 0.001, 1e10, 1e20, 9.999999999999999e-11, 100.0}},
		{"powers of ten, 25 figures", many.Select(x.RoundSigFigs(25)), []any{1000.0, 100000.0, 0.001, 1e10, 1e20, 1e-10, 100.0}},
		{"powers of ten, 30 figures", many.Select(x.RoundSigFigs(30)),
			[]any{1000.0, 100000.0, 0.001, 1e10, 9.999999999999998e19, 1e-10, 100.00000000000001}},
		{"halves away from zero, 2", halves.Select(x.RoundSigFigs(2)), []any{0.13, 0.14, 2.5, 3.5, 15.0, 25.0, -2.5}},
		{"halves away from zero, 1", halves.Select(x.RoundSigFigs(1)), []any{0.1, 0.1, 3.0, 4.0, 20.0, 30.0, -3.0}},
	} {
		if got := typedCells[float64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	// 10^23 and 10^46, where log10 scaled from Log2 falls short of the exponent: an
	// ulp apart at a figure count in two, without the floor's check.
	for _, tc := range []struct {
		v    float64
		want []float64
	}{
		{1e23, []float64{1e23, 1e23, 1.0000000000000001e23, 1.0000000000000001e23, 1.0000000000000001e23, 1e23, 1e23,
			1.0000000000000001e23, 1e23, 1.0000000000000001e23, 1e23, 1.0000000000000001e23, 1.0000000000000001e23,
			1e23, 1e23, 1e23, 1e23, 1e23}},
		{1e46, []float64{1e46, 1e46, 1e46, 9.999999999999999e45, 1.0000000000000001e46, 1e46, 1.0000000000000001e46,
			1.0000000000000001e46, 1e46, 1.0000000000000001e46, 1.0000000000000001e46, 1e46, 1.0000000000000001e46,
			1.0000000000000001e46, 1.0000000000000001e46, 9.999999999999999e45, 1.0000000000000001e46, 9.999999999999999e45}},
	} {
		one := ursus.Frame(ursus.Values("x", []float64{tc.v}))
		for d, w := range tc.want {
			if got := typedCells[float64](t, one.Select(x.RoundSigFigs(d+1))); !sameCells(got, []any{w}) {
				t.Errorf("%g to %d figures: %v, want %v", tc.v, d+1, got, w)
			}
		}
	}
	f32 := ursus.Frame(ursus.Values("x", []float32{3.4e38, 1.5e-44, 0.125, 2.5}))
	if got, want := typedCells[float32](t, f32.Select(x.RoundSigFigs(1))),
		[]any{float32(3.0000000054977558e38), float32(1.961817850054744e-44), float32(0.10000000149011612), float32(3)}; !sameCells(got, want) {
		t.Errorf("a Float32:\ngot  %v\nwant %v", got, want)
	}

	// An integer through a float64 and back, saturating at its type's ends.
	ints := func(name string, got, want []any) {
		t.Helper()
		if !sameCells(got, want) {
			t.Errorf("%s:\ngot  %v\nwant %v", name, got, want)
		}
	}
	i64 := ursus.Frame(ursus.ValuesNullable("x", []int64{123456, -987, 0, 0, 1<<62 + 12345}, []bool{true, true, true, false, true}))
	ints("Int64, 2", typedCells[int64](t, i64.Select(x.RoundSigFigs(2))),
		[]any{int64(120000), int64(-990), int64(0), nil, int64(4599999999999999488)})
	ends := ursus.Frame(ursus.Values("x", []int64{math.MaxInt64, math.MinInt64, 999, 950, 949, -950}))
	ints("Int64's ends, 1", typedCells[int64](t, ends.Select(x.RoundSigFigs(1))),
		[]any{int64(8999999999999998976), int64(-8999999999999998976), int64(1000), int64(1000), int64(900), int64(-1000)})
	ints("Int64's ends, 2", typedCells[int64](t, ends.Select(x.RoundSigFigs(2))),
		[]any{int64(9199999999999998976), int64(-9199999999999998976), int64(1000), int64(950), int64(950), int64(-950)})
	ints("Int64, 19", typedCells[int64](t, ursus.Frame(ursus.Values("x", []int64{math.MaxInt64, 1234567890123456789})).Select(x.RoundSigFigs(19))),
		[]any{int64(math.MaxInt64), int64(1234567890123456768)})
	u8 := ursus.Frame(ursus.Values("x", []uint8{251, 250, 249, 15, 0}))
	ints("UInt8, 1", typedCells[uint8](t, u8.Select(x.RoundSigFigs(1))), []any{uint8(255), uint8(255), uint8(200), uint8(20), uint8(0)})
	ints("UInt8, 2", typedCells[uint8](t, u8.Select(x.RoundSigFigs(2))), []any{uint8(250), uint8(250), uint8(250), uint8(15), uint8(0)})
	i8 := ursus.Frame(ursus.Values("x", []int8{127, -128, -127, 125, 99, -99}))
	ints("Int8, 1", typedCells[int8](t, i8.Select(x.RoundSigFigs(1))), []any{int8(100), int8(-100), int8(-100), int8(100), int8(100), int8(-100)})
	ints("Int8, 2", typedCells[int8](t, i8.Select(x.RoundSigFigs(2))), []any{int8(127), int8(-128), int8(-128), int8(127), int8(99), int8(-99)})
	i16 := ursus.Frame(ursus.Values("x", []int16{32767, 32749, 32750}))
	ints("Int16, 1", typedCells[int16](t, i16.Select(x.RoundSigFigs(1))), []any{int16(30000), int16(30000), int16(30000)})
	ints("Int16, 2", typedCells[int16](t, i16.Select(x.RoundSigFigs(2))), []any{int16(32767), int16(32767), int16(32767)})
	u64 := ursus.Frame(ursus.Values("x", []uint64{math.MaxUint64, 1<<63 + 5}))
	ints("UInt64, 1", typedCells[uint64](t, u64.Select(x.RoundSigFigs(1))), []any{uint64(math.MaxUint64), uint64(8999999999999998976)})
	ints("UInt64, 2", typedCells[uint64](t, u64.Select(x.RoundSigFigs(2))), []any{uint64(17999999999999997952), uint64(9199999999999998976)})

	// An integer Sum is an Int128 here, where Polars' is an Int64: the figures alike.
	sum := ursus.Frame(ursus.Values("x", []int64{123456, 1000})).Select(x.Sum().RoundSigFigs(2).Cast(ursus.Int64))
	if got := csvOf(t, sum); got != "x\n120000\n" {
		t.Errorf("an Int128 sum to two figures: %q", got)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "digits must be an integer >= 1".
		"0 figures": {mixed.Select(x.RoundSigFigs(0)), "at least 1"},
		// Polars: "round_sig_figs can only be used on numeric types".
		"text":       {ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.RoundSigFigs(2)), "requires a number"},
		"a Boolean":  {ursus.Frame(ursus.Values("x", []bool{true})).Select(x.RoundSigFigs(2)), "requires a number"},
		"a duration": {i64.Select(x.Cast(ursus.Duration(ursus.Micro)).RoundSigFigs(2)), "requires a number"},
		// Polars rounds a Decimal's digits; Round refuses one here, and so does this.
		"a Decimal": {i64.Select(x.Cast(ursus.Decimal(38, 2)).RoundSigFigs(2)), "requires a number other than a Decimal"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

func TestCot(t *testing.T) {
	c := ursus.Col
	x := c("x")
	nan, inf, negz := math.NaN(), math.Inf(1), math.Copysign(0, -1)
	// Go's tangent is within 3 ulps of Polars' glibc one, measured over 20,000 values;
	// 1/tan agrees to the bit three times in four. So a finite answer is held to 4
	// ulps, and the infinities, NaN and null exactly. Near a multiple of pi/2 Go
	// reduces the argument in three parts, so its relative error grows there, and
	// pi/2 is held by its absolute error instead.
	near := func(name string, got, want []any) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", name, got, want)
		}
		for i, w := range want {
			g, ok := got[i].(float64)
			w, wok := w.(float64)
			switch {
			case ok != wok:
				t.Errorf("%s row %d: %v, want %v", name, i, got[i], want[i])
			case !ok:
			case math.IsNaN(w) || math.IsInf(w, 0):
				if !sameCells([]any{g}, []any{w}) {
					t.Errorf("%s row %d: %v, want %v", name, i, g, w)
				}
			case math.Abs(g-w) > 4*(math.Nextafter(math.Abs(w), math.Inf(1))-math.Abs(w)):
				t.Errorf("%s row %d: %v, want %v within 4 ulps", name, i, g, w)
			}
		}
	}
	fl := ursus.Frame(ursus.ValuesNullable("x", []float64{0, negz, 1, 0, nan, 2, -3, 1e10, 1e300, 0.001},
		[]bool{true, true, true, false, true, true, true, true, true, true}))
	near("cot", typedCells[float64](t, fl.Select(x.Cot())), []any{inf, -inf, 0.6420926159343306, nil, nan,
		-0.45765755436028577, 7.015252551434534, -1.790992475467611, 0.7035075643976418, 999.9996666666443})
	pole := typedCells[float64](t, ursus.Frame(ursus.Values("x", []float64{math.Pi / 2})).Select(x.Cot()))
	if g, ok := pole[0].(float64); !ok || math.Abs(g-6.123233995736766e-17) > 1e-30 {
		t.Errorf("cot(pi/2) = %v, want 6.123233995736766e-17 within 1e-30", pole[0])
	}
	ints := ursus.Frame(ursus.ValuesNullable("x", []int64{123456, -987, 0, 0, 1<<62 + 12345}, []bool{true, true, true, false, true}))
	near("cot of integers, a Float64", typedCells[float64](t, ints.Select(x.Cot())),
		[]any{0.9081587304523457, -1.6686052939080995, inf, nil, -0.4705771065813635})
	if got, want := typedCells[float32](t, ursus.Frame(ursus.Values("x", []float32{1})).Select(x.Cot())),
		[]any{float32(0.6420925855636597)}; !sameCells(got, want) {
		t.Errorf("cot of a Float32, a Float32:\ngot  %v\nwant %v", got, want)
	}
	// Polars: "trigonometry operation not supported for dtype `str`".
	if _, err := ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.Cot()).Collect(t.Context()); err == nil {
		t.Error("cot of text: no error")
	}
}

func TestClipOneSidedAndNullBounds(t *testing.T) {
	c := ursus.Col
	x, f := c("x"), c("f")
	nan := math.NaN()
	lf := ursus.Frame(ursus.ValuesNullable("x", []int64{1, 5, 10, 0}, []bool{true, true, true, false}),
		ursus.ValuesNullable("lo", []int64{2, 0, 3, 1}, []bool{true, false, true, true}),
		ursus.ValuesNullable("f", []float64{1, nan, -1, 0}, []bool{true, true, true, false}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"lower", lf.Select(x.ClipLower(3)), []any{int64(3), int64(5), int64(10), nil}},
		{"upper", lf.Select(x.ClipUpper(6)), []any{int64(1), int64(5), int64(6), nil}},
		{"a bound null in a row", lf.Select(x.ClipLower(c("lo"))), []any{int64(2), int64(5), int64(10), nil}},
		{"a bound null in a row, both sides", lf.Select(x.Clip(c("lo"), 8)), []any{int64(2), int64(5), int64(8), nil}},
		{"a null lower bound", lf.Select(x.Clip(ursus.Null(ursus.Int64), 6)), []any{int64(1), int64(5), int64(6), nil}},
		{"a null upper bound", lf.Select(x.Clip(3, ursus.Null(ursus.Int64))), []any{int64(3), int64(5), int64(10), nil}},
		{"lo above hi", lf.Select(x.Clip(8, 2)), []any{int64(8), int64(8), int64(2), nil}},
	} {
		if got := typedCells[int64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"floats, NaN kept", lf.Select(f.Clip(0.0, 0.5)), []any{0.5, nan, 0.0, nil}},
		{"floats, lower", lf.Select(f.ClipLower(0.0)), []any{1.0, nan, 0.0, nil}},
		{"a NaN bound, none", lf.Select(f.ClipLower(nan)), []any{1.0, nan, -1.0, nil}},
	} {
		if got := typedCells[float64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	// A Go bound is weak, as Clip's are: an Int32 stays one.
	i32 := lf.Select(x.Cast(ursus.Int32).ClipLower(3))
	if s, err := i32.CollectSchema(t.Context()); err != nil || s.Field(0).Type != ursus.Int32 {
		t.Errorf("an Int32 clipped: %v, %v", s, err)
	}
	if got := typedCells[int32](t, i32); !sameCells(got, []any{int32(3), int32(5), int32(10), nil}) {
		t.Errorf("an Int32 clipped: %v", got)
	}
}
