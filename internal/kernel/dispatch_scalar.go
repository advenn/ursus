//go:build !(goexperiment.simd && amd64)

package kernel

// The scalar dispatch bindings, used when the simd experiment is off or the
// architecture has no hand-written kernels.
//
// Note these bind to the SAME generic functions the SIMD build also links: the
// scalar implementations are never conditionally compiled, only conditionally
// selected. See dispatch_simd_amd64.go.

var (
	cmpEqF64 TotalCompareScalar[float64] = eqScalarConst[float64]
	cmpNeF64 TotalCompareScalar[float64] = neScalarConst[float64]
	cmpLtF64 TotalCompareScalar[float64] = ltScalarConst[float64]
	cmpLeF64 TotalCompareScalar[float64] = leScalarConst[float64]
	cmpGtF64 TotalCompareScalar[float64] = gtScalarConst[float64]
	cmpGeF64 TotalCompareScalar[float64] = geScalarConst[float64]

	arithAddF64 TotalBinary[float64] = addScalar[float64]
	arithMulF64 TotalBinary[float64] = mulScalar[float64]
)

// Accelerated reports whether hardware SIMD kernels are in use.
func Accelerated() bool { return false }

// VectorBits returns the active SIMD vector width in bits, or 0 when scalar.
func VectorBits() int { return 0 }
