package ursus_test

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TruncDiv (step 489), Polars 2.0's truncdiv.

func TestTruncDiv(t *testing.T) {
	a, b := ursus.Col("a"), ursus.Col("b")
	inf, nan, negz := math.Inf(1), math.NaN(), math.Copysign(0, -1)
	pair := func(l, r *ursus.Column) *ursus.LazyFrame { return ursus.Frame(l, r).Select(a.TruncDiv(b)) }

	// Polars 2.1.0 gives these: the quotient toward zero, in FloorDiv's type, a zero
	// integer divisor a null, MinInt64 by -1 wrapped, an Int64 by a UInt64 an Int128.
	for _, c := range []struct {
		name string
		got  []any
		want []any
	}{
		{"Int64", typedCells[int64](t, pair(
			ursus.ValuesNullable("a", []int64{7, -7, 7, -7, 0, 5, 0, 3, math.MinInt64, math.MinInt64, math.MinInt64, 9},
				[]bool{true, true, true, true, true, true, false, true, true, true, true, true}),
			ursus.ValuesNullable("b", []int64{2, 2, -2, -2, 3, 0, 1, 0, -1, 1, 2, 9},
				[]bool{true, true, true, true, true, true, true, false, true, true, true, true}))),
			[]any{int64(3), int64(-3), int64(-3), int64(3), int64(0), nil, nil, nil,
				int64(math.MinInt64), int64(math.MinInt64), int64(-4611686018427387904), int64(1)}},
		{"Int8", typedCells[int8](t, pair(ursus.Values("a", []int8{-128, -7, 127}), ursus.Values("b", []int8{-1, 2, -2}))),
			[]any{int8(-128), int8(-3), int8(-63)}},
		{"UInt8", typedCells[uint8](t, pair(ursus.Values("a", []uint8{255, 7}), ursus.Values("b", []uint8{2, 0}))),
			[]any{uint8(127), nil}},
		{"Int32 by Int64", typedCells[int64](t, pair(ursus.Values("a", []int32{-7}), ursus.Values("b", []int64{2}))),
			[]any{int64(-3)}},
		{"Int64 by UInt64, an Int128", typedCells[string](t, pair(ursus.Values("a", []int64{-7}), ursus.Values("b", []uint64{2})).
			Select(a.Cast(ursus.String))), []any{"-3"}},
		{"Int128, -7 and MinInt128", typedCells[string](t, ursus.Frame(ursus.Values("a", []int64{-7, math.MinInt64}),
			ursus.Values("b", []int64{1, 1 << 62}), ursus.Values("k", []int64{1, 4})).
			Select(a.Cast(ursus.Int128).Mul(b).Mul(ursus.Col("k")).TruncDiv(2).Cast(ursus.String))),
			[]any{"-3", "-85070591730234615865843651857942052864"}},
		{"Float64", typedCells[float64](t, pair(
			ursus.ValuesNullable("a", []float64{7, -7, 7, -7, 1, -1, 0, nan, inf, -inf, -0.5, 0.5, 1e300, negz, 6, 5.5, 0.3, 1},
				[]bool{true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, false}),
			ursus.Values("b", []float64{2, 2, -2, -2, 0, 0, 0, 1, 2, 2, 2, 2, 1e-10, 2, -3, inf, 0.1, 1}))),
			[]any{3.0, -3.0, -3.0, 3.0, inf, -inf, nan, nan, inf, -inf, negz, 0.0, inf, negz, -2.0, 0.0, 2.0, nil}},
		{"Float32", typedCells[float32](t, pair(ursus.Values("a", []float32{-7, 7.5, 1e30}), ursus.Values("b", []float32{2, -2, 3}))),
			[]any{float32(-3), float32(-3), float32(3.333333383491554e+29)}},
		{"Int64 by a float", typedCells[float64](t, ursus.Frame(ursus.Values("a", []int64{-7})).Select(a.TruncDiv(2.0))),
			[]any{-3.0}},
		// Polars 2.1.0 refuses a Duration by an integer, "trunc_div not allowed on
		// duration[μs] and dyn int"; ursus divides its ticks, as FloorDiv floors them.
		{"Duration by an integer", typedCells[int64](t, ursus.Frame(ursus.Values("a", []time.Duration{-7, 7, 5})).
			Select(a.TruncDiv(2).Cast(ursus.Int64))), []any{int64(-3), int64(3), int64(2)}},
	} {
		if !sameCells(c.got, c.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", c.name, c.got, c.want)
		}
	}

	// A zero integer divisor makes a null of columns that have none.
	if s, err := pair(ursus.Values("a", []int64{1}), ursus.Values("b", []int64{0})).CollectSchema(t.Context()); err != nil || !s.Field(0).Nullable {
		t.Errorf("an integer TruncDiv is not nullable: %v, %v", s, err)
	}

	// Refused, where Polars 2.1.0 wraps an Int128's MinInt128 by -1 and divides a
	// Decimal at Decimal(38, 2), -7.50 by 2 to -3.00: Int128 arithmetic here is exact
	// or refused, and a Decimal's FloorDiv is refused too. Text is refused by both.
	for name, q := range map[string]*ursus.LazyFrame{
		"MinInt128 by -1": ursus.Frame(ursus.Values("a", []int64{math.MinInt64})).
			Select(a.Cast(ursus.Int128).Mul(int64(1) << 62).Mul(4).TruncDiv(-1)),
		"MinInt64 ns by -1": ursus.Frame(ursus.Values("a", []time.Duration{math.MinInt64})).Select(a.TruncDiv(-1)),
		"a Duration by 2.5": ursus.Frame(ursus.Values("a", []time.Duration{1})).Select(a.TruncDiv(2.5)),
		"a Decimal":         ursus.Frame(ursus.Values("a", []float64{-7.5})).Select(a.Cast(ursus.Decimal(5, 2)).TruncDiv(2)),
		"text":              ursus.Frame(ursus.Values("a", []string{"x"})).Select(a.TruncDiv(2)),
	} {
		if _, err := q.Collect(t.Context()); err == nil {
			t.Errorf("TruncDiv of %s: no error", name)
		}
	}

	// The oracle: Go's / truncates, and a == b*q + a%b. Over generated pairs at every
	// width's ends, and floats against math.Trunc of IEEE's quotient.
	rng := rand.New(rand.NewPCG(489, 1))
	edges := []int64{math.MinInt64, math.MinInt64 + 1, -1, 0, 1, math.MaxInt64}
	draw := func() int64 {
		if rng.IntN(4) == 0 {
			return edges[rng.IntN(len(edges))]
		}
		return rng.Int64N(2001) - 1000
	}
	n := 3000
	ia, ib := make([]int64, n), make([]int64, n)
	i8a, i8b := make([]int8, n), make([]int8, n)
	fa, fb := make([]float64, n), make([]float64, n)
	for i := range n {
		ia[i], ib[i] = draw(), draw()
		i8a[i], i8b[i] = int8(rng.IntN(256)-128), int8(rng.IntN(256)-128)
		fa[i], fb[i] = (rng.Float64()-0.5)*200, (rng.Float64()-0.5)*20
	}
	got64 := typedCells[int64](t, pair(ursus.Values("a", ia), ursus.Values("b", ib)))
	got8 := typedCells[int8](t, pair(ursus.Values("a", i8a), ursus.Values("b", i8b)))
	gotf := typedCells[float64](t, pair(ursus.Values("a", fa), ursus.Values("b", fb)))
	for i := range n {
		if ib[i] == 0 {
			if got64[i] != nil {
				t.Fatalf("%d truncdiv 0 = %v, want null", ia[i], got64[i])
			}
		} else if q, ok := got64[i].(int64); !ok || q != ia[i]/ib[i] || ib[i]*q+ia[i]%ib[i] != ia[i] {
			t.Fatalf("%d truncdiv %d = %v, Go %d", ia[i], ib[i], got64[i], ia[i]/ib[i])
		}
		if i8b[i] != 0 && got8[i] != i8a[i]/i8b[i] {
			t.Fatalf("Int8 %d truncdiv %d = %v, Go %d", i8a[i], i8b[i], got8[i], i8a[i]/i8b[i])
		}
		if want := math.Trunc(fa[i] / fb[i]); !sameCells(gotf[i:i+1], []any{want}) {
			t.Fatalf("%v truncdiv %v = %v, want %v", fa[i], fb[i], gotf[i], want)
		}
	}
}
