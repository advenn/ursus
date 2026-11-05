package ursus_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
)

// Erf and Erfc (step 488), Polars' erf and erfc.

// withinUlps reports whether got is want, by its bits where want is not finite or is
// zero, so -0 is not 0, and otherwise within n units in the last place.
func withinUlps(got, want float64, n int) bool {
	if math.IsNaN(want) || math.IsInf(want, 0) || want == 0 {
		return math.Float64bits(got) == math.Float64bits(want) || math.IsNaN(got) && math.IsNaN(want)
	}
	ulp := math.Nextafter(math.Abs(want), math.Inf(1)) - math.Abs(want)
	return math.Abs(got-want) <= float64(n)*ulp
}

func TestErfAndErfc(t *testing.T) {
	x := ursus.Col("x")
	inf, nan, negz := math.Inf(1), math.NaN(), math.Copysign(0, -1)

	// Polars 2.1.0 gives these, a Float64 each. Go's math.Erf is within an ulp of
	// its glibc one and math.Erfc within four; the infinities, zeros and NaN exactly.
	polars := []struct{ x, erf, erfc float64 }{
		{nan, nan, nan},
		{inf, 1, 0},
		{-inf, -1, 2},
		{negz, negz, 1},
		{0, 0, 1},
		{1e-300, 1.1283791670955126e-300, 1},
		{0.5, 0.5204998778130465, 0.4795001221869535},
		{-0.5, -0.5204998778130465, 1.5204998778130465},
		{1, 0.8427007929497149, 0.15729920705028513},
		{2.5, 0.999593047982555, 0.0004069520174449589},
		{-2.5, -0.999593047982555, 1.999593047982555},
		{4.5, 0.9999999998033839, 1.9661604415428873e-10},
		{6, 1, 2.1519736712498916e-17},
		{27, 1, 5.23705e-319},
		{-30, -1, 2},
	}
	xs := make([]float64, 0, len(polars)+1)
	for _, p := range polars {
		xs = append(xs, p.x)
	}
	valid := make([]bool, len(xs)+1)
	for i := range xs {
		valid[i] = true
	}
	frame := ursus.Frame(ursus.ValuesNullable("x", append(xs, 0), valid))
	erf := typedCells[float64](t, frame.Select(x.Erf()))
	erfc := typedCells[float64](t, frame.Select(x.Erfc()))
	for i, p := range polars {
		if g, ok := erf[i].(float64); !ok || !withinUlps(g, p.erf, 1) {
			t.Errorf("erf(%v) = %v, Polars %v", p.x, erf[i], p.erf)
		}
		if g, ok := erfc[i].(float64); !ok || !withinUlps(g, p.erfc, 4) {
			t.Errorf("erfc(%v) = %v, Polars %v", p.x, erfc[i], p.erfc)
		}
	}
	if erf[len(xs)] != nil || erfc[len(xs)] != nil {
		t.Errorf("erf and erfc of a null: %v and %v, want nulls", erf[len(xs)], erfc[len(xs)])
	}

	// Polars 2.1.0: an Int8 widens to a Float64 and a Float32 stays one, its answer
	// the Float64's rounded once.
	typed := ursus.Frame(
		ursus.ValuesNullable("i", []int8{1, -2, 0, 0}, []bool{true, true, false, true}),
		ursus.ValuesNullable("x", []float32{1, -2, 0, 4}, []bool{true, true, false, true}))
	for _, c := range []struct {
		name string
		got  []any
		want []any
	}{
		{"erf(Int8)", typedCells[float64](t, typed.Select(ursus.Col("i").Erf())),
			[]any{0.8427007929497149, -0.9953222650189527, nil, 0.0}},
		{"erfc(Int8)", typedCells[float64](t, typed.Select(ursus.Col("i").Erfc())),
			[]any{0.15729920705028513, 1.9953222650189528, nil, 1.0}},
		{"erf(Float32)", typedCells[float32](t, typed.Select(x.Erf())),
			[]any{float32(0.8427007794380188), float32(-0.9953222870826721), nil, float32(1)}},
		{"erfc(Float32)", typedCells[float32](t, typed.Select(x.Erfc())),
			[]any{float32(0.15729920566082), float32(1.9953222274780273), nil, float32(1.5417258580896487e-08)}},
	} {
		if !sameCells(c.got, c.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", c.name, c.got, c.want)
		}
	}

	// Polars 2.1.0 refuses text: "`erf` operation not supported for dtype `str`". It
	// answers a Boolean and a Decimal as Float64s, which ursus refuses, as it refuses
	// their Sqrt.
	for name, f := range map[string]*ursus.LazyFrame{
		"String":        ursus.Frame(ursus.Values("x", []string{"1"})),
		"Boolean":       ursus.Frame(ursus.Values("x", []bool{true})),
		"Decimal(5, 2)": ursus.Frame(ursus.Values("x", []float64{1.5})).Select(x.Cast(ursus.Decimal(5, 2))),
	} {
		if _, err := f.Select(x.Erf()).Collect(t.Context()); err == nil {
			t.Errorf("erf of a %s: no error", name)
		}
	}

	// The oracle: Go's math.Erf and math.Erfc, to the bit, over a sweep through
	// every branch of their rational approximations, and a Float32's rounded once.
	rng := rand.New(rand.NewPCG(488, 1))
	sweep := make([]float64, 4000)
	for i := range sweep {
		switch i % 4 {
		case 0:
			sweep[i] = rng.Float64()*12 - 6
		case 1:
			sweep[i] = (rng.Float64()*2 - 1) * 1e-3
		case 2:
			sweep[i] = rng.Float64()*30 - 3
		default:
			sweep[i] = -rng.Float64() * 30
		}
	}
	sf := ursus.Frame(ursus.Values("x", sweep))
	for _, fn := range []struct {
		name string
		expr ursus.Expr
		want func(float64) float64
	}{{"erf", x.Erf(), math.Erf}, {"erfc", x.Erfc(), math.Erfc}} {
		got := typedCells[float64](t, sf.Select(fn.expr))
		got32 := typedCells[float32](t, sf.Select(x.Cast(ursus.Float32)).Select(fn.expr))
		for i, v := range sweep {
			if want := fn.want(v); !sameCells(got[i:i+1], []any{want}) {
				t.Fatalf("%s(%v) = %v, math %v", fn.name, v, got[i], want)
			}
			if want := float32(fn.want(float64(float32(v)))); !sameCells(got32[i:i+1], []any{want}) {
				t.Fatalf("%s(Float32 %v) = %v, math %v", fn.name, float32(v), got32[i], want)
			}
		}
	}
}
