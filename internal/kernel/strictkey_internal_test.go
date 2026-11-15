package kernel

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/expr"
)

// strictFloats are generated floats over every class of bits: random words, which
// are mostly ordinary numbers, and around them the zeros, the infinities, the
// subnormals and NaNs of both signs and several payloads, signalling ones among them.
func strictFloats(r *rand.Rand, n int) []float64 {
	edges := []uint64{0, 1 << 63, 0x7FF0000000000000, 0xFFF0000000000000, 1, 1<<63 | 1,
		0x000FFFFFFFFFFFFF, 0x7FEFFFFFFFFFFFFF, 0xFFEFFFFFFFFFFFFF}
	out := make([]float64, n)
	for i := range out {
		b := r.Uint64()
		switch r.IntN(4) {
		case 0:
			b = edges[r.IntN(len(edges))]
		case 1: // a NaN: an exponent of all ones and a payload that is not zero
			b = b | 0x7FF0000000000000
			if b&0x000FFFFFFFFFFFFF == 0 {
				b |= 1
			}
		}
		out[i] = math.Float64frombits(b)
	}
	return out
}

// TestStrictKeyIsTheTotalOrderMadeStrict: StrictKeyF64 is one to one, StrictF64
// undoes it, and its order is OrderKeyF64's wherever that one has two floats apart,
// with no two floats of different bits equal; StrictKeyF32 likewise for float32s.
func TestStrictKeyIsTheTotalOrderMadeStrict(t *testing.T) {
	r := rand.New(rand.NewPCG(523, 1))
	v := strictFloats(r, 4000)
	for _, x := range v {
		if got := StrictF64(StrictKeyF64(x)); math.Float64bits(got) != math.Float64bits(x) {
			t.Fatalf("StrictF64(StrictKeyF64(%016x)) is %016x", math.Float64bits(x), math.Float64bits(got))
		}
	}
	for i := range 20000 {
		a, b := v[r.IntN(len(v))], v[r.IntN(len(v))]
		if i%5 == 0 {
			b = a
		}
		ka, kb := StrictKeyF64(a), StrictKeyF64(b)
		oa, ob := OrderKeyF64(a), OrderKeyF64(b)
		switch {
		case oa < ob && ka >= kb, oa > ob && ka <= kb:
			t.Fatalf("%016x and %016x: the order keys part them one way, the strict keys not",
				math.Float64bits(a), math.Float64bits(b))
		case (ka == kb) != (math.Float64bits(a) == math.Float64bits(b)):
			t.Fatalf("%016x and %016x: strict keys equal %v", math.Float64bits(a), math.Float64bits(b), ka == kb)
		}
		a32, b32 := float32(a), math.Float32frombits(uint32(math.Float64bits(b)))
		if i%5 == 0 {
			b32 = a32
		}
		k32a, k32b := StrictKeyF32(a32), StrictKeyF32(b32)
		o32a, o32b := OrderKeyF32(a32), OrderKeyF32(b32)
		switch {
		case o32a < o32b && k32a >= k32b, o32a > o32b && k32a <= k32b:
			t.Fatalf("%08x and %08x: the order keys part them one way, the strict keys not",
				math.Float32bits(a32), math.Float32bits(b32))
		case (k32a == k32b) != (math.Float32bits(a32) == math.Float32bits(b32)):
			t.Fatalf("%08x and %08x: strict keys equal %v", math.Float32bits(a32), math.Float32bits(b32), k32a == k32b)
		}
	}
}

// TestAQuantileIsTheSameBitsInAnyOrder: over a group of values the total order calls
// equal and their bits do not, -0 and +0 and NaNs of several payloads, quantileOf
// answers the bits of the strict sort's ranks whatever order the values come in, at
// every q and interpolation (step 523).
func TestAQuantileIsTheSameBitsInAnyOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(523, 2))
	atoms := []float64{math.Copysign(0, -1), 0, 1, -1}
	for _, b := range []uint64{0x7FF8000000000001, 0xFFF8000000000000, 0x7FF8DEADBEEF0001, 0xFFFC000000000005} {
		atoms = append(atoms, math.Float64frombits(b))
	}
	interps := []expr.Interpolation{expr.InterpLinear, expr.InterpLower, expr.InterpHigher, expr.InterpNearest, expr.InterpMidpoint}
	for trial := range 500 {
		v := make([]float64, 1+r.IntN(40))
		for i := range v {
			v[i] = atoms[r.IntN(len(atoms))]
		}
		sorted := slices.Clone(v)
		slices.SortFunc(sorted, compareStrictF64)
		for _, q := range []float64{0, 0.25, 0.5, 0.75, 1, r.Float64()} {
			for _, in := range interps {
				lo, hi, frac := ranks(len(v), q)
				want := math.Float64bits(interpolate(sorted[lo], sorted[hi], frac, in))
				for range 4 {
					w := slices.Clone(v)
					r.Shuffle(len(w), func(i, j int) { w[i], w[j] = w[j], w[i] })
					if got := math.Float64bits(quantileOf(w, q, in)); got != want {
						t.Fatalf("trial %d, q %v, interpolation %d: %016x, the strict sort's %016x", trial, q, in, got, want)
					}
				}
			}
		}
	}
}
