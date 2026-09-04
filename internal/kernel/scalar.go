package kernel

import (
	"math"

	"ursus/internal/bitmap"
)

// This file holds the SCALAR implementations of every kernel.
//
// They compile UNCONDITIONALLY — there is no build tag on this file. That is
// deliberate and it is the fix for defect D14: the original design put the scalar
// twin behind `//go:build !goexperiment.simd`, which means only one
// implementation is ever linked, so the "differential test asserting bit-identical
// results" it also mandated could never actually run.
//
// Here the scalar code is always present under its own name and only the DISPATCH
// is build-tagged (see simd.go / nosimd.go). One test binary can therefore call
// both and compare them.
//
// Being generic, these are also the entire kernel set for every type that does not
// have a hand-written SIMD version — which is most of them.

// --- arithmetic --------------------------------------------------------------

func addScalar[T Numeric](dst, a, b []T) {
	for i := range a {
		dst[i] = a[i] + b[i]
	}
}

func subScalar[T Numeric](dst, a, b []T) {
	for i := range a {
		dst[i] = a[i] - b[i]
	}
}

func mulScalar[T Numeric](dst, a, b []T) {
	for i := range a {
		dst[i] = a[i] * b[i]
	}
}

// divFloatScalar is total because IEEE division never traps: x/0 gives ±Inf or
// NaN, which are values rather than faults.
func divFloatScalar[T ~float32 | ~float64](dst, a, b []T) {
	for i := range a {
		dst[i] = a[i] / b[i]
	}
}

// signScalar is -1, 0 or 1 — except that for floats it returns the operand itself
// in the zero case, so sign(-0.0) is -0.0 and sign(NaN) is NaN. Both fall out of
// the ordering test with no special case, and both match NumPy.
func signScalar[T ~int8 | ~int16 | ~int32 | ~int64 | ~float32 | ~float64](dst, a []T) {
	for i, v := range a {
		switch {
		case v > 0:
			dst[i] = 1
		case v < 0:
			dst[i] = -1
		default:
			dst[i] = v
		}
	}
}

func powScalar[T ~float32 | ~float64](dst, a, b []T) {
	for i := range a {
		dst[i] = T(math.Pow(float64(a[i]), float64(b[i])))
	}
}

// modFloatScalar is total for the same reason divFloatScalar is: math.Mod returns
// NaN rather than faulting, so x % 0 is a value.
func modFloatScalar[T ~float32 | ~float64](dst, a, b []T) {
	for i := range a {
		dst[i] = T(math.Mod(float64(a[i]), float64(b[i])))
	}
}

func addScalarConst[T Numeric](dst, a []T, s T) {
	for i := range a {
		dst[i] = a[i] + s
	}
}

func subScalarConst[T Numeric](dst, a []T, s T) {
	for i := range a {
		dst[i] = a[i] - s
	}
}

func mulScalarConst[T Numeric](dst, a []T, s T) {
	for i := range a {
		dst[i] = a[i] * s
	}
}

func divFloatScalarConst[T ~float32 | ~float64](dst, a []T, s T) {
	for i := range a {
		dst[i] = a[i] / s
	}
}

func negScalar[T Numeric](dst, a []T) {
	for i := range a {
		dst[i] = -a[i]
	}
}

func absIntScalar[T ~int8 | ~int16 | ~int32 | ~int64](dst, a []T) {
	for i := range a {
		if a[i] < 0 {
			dst[i] = -a[i]
		} else {
			dst[i] = a[i]
		}
	}
}

func absFloatScalar[T ~float32 | ~float64](dst, a []T) {
	for i := range a {
		dst[i] = T(math.Abs(float64(a[i])))
	}
}

// --- integer division: PARTIAL, because Go panics on x/0 ---------------------

// divIntScalar produces a null where the divisor is zero, matching SQL and
// Polars. It must consult `in` before dividing: evaluating a null lane whose
// garbage divisor happens to be 0 would panic and take the process down.
func divIntScalar[T Integer](dst []T, ok *bitmap.Builder, a, b []T, in bitmap.View) {
	for i := range a {
		if !in.Get(i) || b[i] == 0 {
			ok.Append(false)
			continue
		}
		dst[i] = a[i] / b[i]
		ok.Append(true)
	}
}

func modIntScalar[T Integer](dst []T, ok *bitmap.Builder, a, b []T, in bitmap.View) {
	for i := range a {
		if !in.Get(i) || b[i] == 0 {
			ok.Append(false)
			continue
		}
		dst[i] = a[i] % b[i]
		ok.Append(true)
	}
}

func divIntScalarConst[T Integer](dst []T, ok *bitmap.Builder, a []T, s T, in bitmap.View) {
	if s == 0 {
		ok.AppendMany(false, len(a))
		return
	}
	for i := range a {
		if !in.Get(i) {
			ok.Append(false)
			continue
		}
		dst[i] = a[i] / s
		ok.Append(true)
	}
}

// --- comparisons -------------------------------------------------------------
//
// Each writes exactly len(a) payload bits. Ordering follows IEEE for floats:
// NaN compares false against everything, including itself. Sorting and grouping
// need a different, total order — see order.go — and must never call these.

func eqScalar[T Numeric](out *bitmap.Builder, a, b []T) {
	for i := range a {
		out.Append(a[i] == b[i])
	}
}

func neScalar[T Numeric](out *bitmap.Builder, a, b []T) {
	for i := range a {
		out.Append(a[i] != b[i])
	}
}

func ltScalar[T Numeric](out *bitmap.Builder, a, b []T) {
	for i := range a {
		out.Append(a[i] < b[i])
	}
}

func leScalar[T Numeric](out *bitmap.Builder, a, b []T) {
	for i := range a {
		out.Append(a[i] <= b[i])
	}
}

func gtScalar[T Numeric](out *bitmap.Builder, a, b []T) {
	for i := range a {
		out.Append(a[i] > b[i])
	}
}

func geScalar[T Numeric](out *bitmap.Builder, a, b []T) {
	for i := range a {
		out.Append(a[i] >= b[i])
	}
}

func eqScalarConst[T Numeric](out *bitmap.Builder, a []T, s T) {
	for i := range a {
		out.Append(a[i] == s)
	}
}

func neScalarConst[T Numeric](out *bitmap.Builder, a []T, s T) {
	for i := range a {
		out.Append(a[i] != s)
	}
}

func ltScalarConst[T Numeric](out *bitmap.Builder, a []T, s T) {
	for i := range a {
		out.Append(a[i] < s)
	}
}

func leScalarConst[T Numeric](out *bitmap.Builder, a []T, s T) {
	for i := range a {
		out.Append(a[i] <= s)
	}
}

func gtScalarConst[T Numeric](out *bitmap.Builder, a []T, s T) {
	for i := range a {
		out.Append(a[i] > s)
	}
}

func geScalarConst[T Numeric](out *bitmap.Builder, a []T, s T) {
	for i := range a {
		out.Append(a[i] >= s)
	}
}

// --- NaN predicates ----------------------------------------------------------
//
// Float-only by construction. Asking whether an Int64 is NaN is a category error
// that the type checker already rejects; these never see one.

func isNanScalar[T ~float32 | ~float64](out *bitmap.Builder, a []T) {
	for i := range a {
		v := float64(a[i])
		out.Append(v != v)
	}
}

func isFiniteScalar[T ~float32 | ~float64](out *bitmap.Builder, a []T) {
	for i := range a {
		out.Append(!math.IsInf(float64(a[i]), 0) && float64(a[i]) == float64(a[i]))
	}
}

func isInfScalar[T ~float32 | ~float64](out *bitmap.Builder, a []T) {
	for i := range a {
		out.Append(math.IsInf(float64(a[i]), 0))
	}
}
