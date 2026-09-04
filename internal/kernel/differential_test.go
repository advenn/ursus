package kernel

// The differential test: every SIMD kernel must agree with its scalar twin,
// bit for bit.
//
// This test is the reason scalar.go has no build tag. The original design put the
// scalar implementation behind `//go:build !goexperiment.simd`, which links
// exactly one implementation per build and makes this comparison impossible to
// write — defect D14 from the design review. Here the scalar code always compiles
// under its own name and only the dispatch vars are build-tagged, so both paths
// are reachable from one test binary.
//
// Run it under the full width matrix (`make test-widths`). GODEBUG=simd=128 is the
// leg that matters most: at 128 bits a float64 vector has 2 lanes, so movemask
// bits are appended two at a time and every write after the first straddles a byte
// boundary. GODEBUG=simd=0 exercises the emulated-mode fallback, where ToArch()
// returns a type this package cannot even name.

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
)

// lengths covers every interesting relationship between a slice length and the
// vector width: empty, sub-vector, exact multiples, and one-past every boundary.
// 63/64/65 and 127/128/129 matter because they also straddle the Builder's
// internal 64-bit accumulator.
var lengths = []int{
	0, 1, 2, 3, 4, 5, 7, 8, 9, 15, 16, 17, 31, 32, 33,
	63, 64, 65, 127, 128, 129, 255, 256, 257, 1000, 4096, 4097,
}

func randFloats(rng *rand.Rand, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		switch rng.IntN(20) {
		case 0:
			out[i] = math.NaN() // NaN compares false against everything, including itself
		case 1:
			out[i] = math.Inf(1)
		case 2:
			out[i] = math.Inf(-1)
		case 3:
			out[i] = 0
		case 4:
			out[i] = math.Copysign(0, -1) // -0.0 == +0.0 under IEEE
		default:
			out[i] = (rng.Float64() - 0.5) * 100
		}
	}
	return out
}

func bitsOf(v bitmap.View) []bool {
	out := make([]bool, v.Len())
	for i := range out {
		out[i] = v.Get(i)
	}
	return out
}

func TestDifferentialComparisons(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 1337))

	ops := []struct {
		name     string
		dispatch TotalCompareScalar[float64]
		scalar   TotalCompareScalar[float64]
	}{
		{"Eq", cmpEqF64, eqScalarConst[float64]},
		{"Ne", cmpNeF64, neScalarConst[float64]},
		{"Lt", cmpLtF64, ltScalarConst[float64]},
		{"Le", cmpLeF64, leScalarConst[float64]},
		{"Gt", cmpGtF64, gtScalarConst[float64]},
		{"Ge", cmpGeF64, geScalarConst[float64]},
	}

	// Scalars chosen to land on and around values present in the data, so the
	// boundary cases (equality, NaN, ±Inf, ±0) are actually hit.
	scalars := []float64{0, math.Copysign(0, -1), 1, -1, 25, -25,
		math.NaN(), math.Inf(1), math.Inf(-1)}

	for _, op := range ops {
		for _, n := range lengths {
			a := randFloats(rng, n)
			for _, s := range scalars {
				wantB := bitmap.NewBuilder(n)
				op.scalar(wantB, a, s)
				want := bitsOf(wantB.Finish())

				gotB := bitmap.NewBuilder(n)
				op.dispatch(gotB, a, s)
				got := bitsOf(gotB.Finish())

				if len(got) != len(want) {
					t.Fatalf("%s n=%d s=%v: produced %d bits, want %d",
						op.name, n, s, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("%s n=%d s=%v: bit %d = %v, scalar says %v (a[%d]=%v)",
							op.name, n, s, i, got[i], want[i], i, a[i])
					}
				}
			}
		}
	}
}

func TestDifferentialArithmetic(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 99))

	ops := []struct {
		name     string
		dispatch TotalBinary[float64]
		scalar   TotalBinary[float64]
	}{
		{"Add", arithAddF64, addScalar[float64]},
		{"Mul", arithMulF64, mulScalar[float64]},
	}

	for _, op := range ops {
		for _, n := range lengths {
			a, b := randFloats(rng, n), randFloats(rng, n)

			want := make([]float64, n)
			op.scalar(want, a, b)

			got := make([]float64, n)
			op.dispatch(got, a, b)

			for i := range want {
				// Bit-identical, not approximately equal: the SIMD and scalar
				// paths execute the same IEEE operations in the same order, so any
				// difference is a bug rather than rounding. Comparing bit patterns
				// also makes NaN and -0.0 comparable at all.
				if math.Float64bits(got[i]) != math.Float64bits(want[i]) {
					t.Fatalf("%s n=%d: index %d = %v, scalar says %v (a=%v b=%v)",
						op.name, n, i, got[i], want[i], a[i], b[i])
				}
			}
		}
	}
}

// TestNaNComparisonSemantics pins IEEE behaviour for Expr-level comparisons.
//
// NaN compares FALSE against everything, including itself. This is deliberately
// different from the total order that sorting and hash-grouping will need, where
// NaN must equal NaN and sort above everything. Confusing the two produces a sort
// that never terminates or a group-by that splits NaNs across groups, so the
// distinction is worth a test even before the sort exists.
func TestNaNComparisonSemantics(t *testing.T) {
	nan := math.NaN()
	a := []float64{nan, 1, nan, 2}

	check := func(name string, k TotalCompareScalar[float64], s float64, want []bool) {
		t.Helper()
		b := bitmap.NewBuilder(len(a))
		k(b, a, s)
		got := bitsOf(b.Finish())
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s(a[%d]=%v, %v) = %v, want %v", name, i, a[i], s, got[i], want[i])
			}
		}
	}

	check("Eq", cmpEqF64, nan, []bool{false, false, false, false})
	check("Ne", cmpNeF64, nan, []bool{true, true, true, true})
	check("Lt", cmpLtF64, 1.5, []bool{false, true, false, false})
	check("Gt", cmpGtF64, 1.5, []bool{false, false, false, true})
}

// TestZeroSignIgnoredByComparison: +0.0 == -0.0 under IEEE. (Sorting and hashing
// will need to canonicalise them; comparison must not.)
func TestZeroSignIgnoredByComparison(t *testing.T) {
	a := []float64{0, math.Copysign(0, -1)}
	b := bitmap.NewBuilder(2)
	cmpEqF64(b, a, 0)
	got := bitsOf(b.Finish())
	if !got[0] || !got[1] {
		t.Errorf("+0.0 and -0.0 must both equal 0, got %v", got)
	}
}

// TestAcceleratedReporting documents which path the current process is on, so a
// CI log shows at a glance whether the SIMD leg actually ran with hardware.
func TestAcceleratedReporting(t *testing.T) {
	t.Logf("accelerated=%v vectorBits=%d", Accelerated(), VectorBits())
}
