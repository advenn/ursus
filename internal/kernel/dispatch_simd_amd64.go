//go:build goexperiment.simd && amd64

package kernel

// The SIMD dispatch bindings.
//
// THIS FILE IS THE ONLY BUILD-TAGGED PART OF THE KERNEL LAYER, and that is the
// point. The scalar implementations in scalar.go compile unconditionally, so both
// paths exist in the same binary and the differential test can call them side by
// side. The original design put the scalar twin behind an inverted build tag,
// which linked exactly one implementation and made the differential test it also
// required impossible to write (defect D14).

var (
	cmpEqF64 TotalCompareScalar[float64] = eqF64SIMDConst
	cmpNeF64 TotalCompareScalar[float64] = neF64SIMDConst
	cmpLtF64 TotalCompareScalar[float64] = ltF64SIMDConst
	cmpLeF64 TotalCompareScalar[float64] = leF64SIMDConst
	cmpGtF64 TotalCompareScalar[float64] = gtF64SIMDConst
	cmpGeF64 TotalCompareScalar[float64] = geF64SIMDConst

	arithAddF64 TotalBinary[float64] = addF64SIMD
	arithMulF64 TotalBinary[float64] = mulF64SIMD
)

// Accelerated reports whether hardware SIMD kernels are in use.
func Accelerated() bool { return simdAvailable() }

// VectorBits returns the active SIMD vector width in bits, or 0 when scalar.
func VectorBits() int { return simdWidth() }
