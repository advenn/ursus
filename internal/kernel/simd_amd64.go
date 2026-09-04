//go:build goexperiment.simd && amd64

package kernel

import (
	"simd"

	"github.com/advenn/ursus/internal/bitmap"
)

// SIMD comparison kernels for float64 — the proof family.
//
// One family is implemented by hand to prove the whole path end to end: the
// LoadPart/StorePart loop, the width-safe movemask, the ragged tail, the
// emulated-mode fallback, and the differential test against the scalar twin.
// Every remaining family is the same shape and can be generated once this one is
// trusted.
//
// Note what is NOT here. The portable `simd` package lacks Mul, Min and Max on
// 64-bit integers, ordering comparisons on Uint8s, and Compress/Expand entirely.
// Integer and filter kernels therefore need archsimd rather than portable simd,
// which is why only float64 gets a portable implementation today.

// step returns how many elements the vector v actually covers of the remaining
// slice.
//
// # Do not trust LoadXxxPart's returned count
//
// LoadFloat64sPart is documented to return "the number of elements loaded", and in
// hardware mode it does: min(len(s), lanes). Under GODEBUG=simd=0 it returns
// len(s) instead, while still loading only `lanes` elements. Measured on
// go1.27.0, 2 lanes:
//
//	hardware:  LoadPart(10 elems) -> n=2   StorePart -> 2   correct
//	emulated:  LoadPart(10 elems) -> n=10  StorePart -> 2   WRONG
//
// A loop written to the documented idiom `i += n` therefore skips elements under
// emulation and silently produces wrong answers — and because the development
// machine has 8 float64 lanes, every test with fewer than 9 elements passes
// anyway. This is a bug in the simd package's emulation path, not in ursus, but
// the workaround belongs here: v.Len() is reliable in both modes, so the step is
// derived from it and LoadPart's count is discarded.
//
// The GODEBUG=simd=0 CI leg exists to catch exactly this, and it did.
func step(remaining int, lanes int) int { return min(remaining, lanes) }

// simdCompareF64 is the shared skeleton: load, compare, movemask, append.
//
// Three subtleties, each a silent-wrongness bug if missed:
//
//  1. LoadFloat64sPart zero-fills lanes past the end of the slice, so the final
//     vector compares garbage. The mask is trimmed to the live lane count.
//  2. The live lane count is not the vector width on the last iteration, so bits
//     are appended n at a time rather than lane-width at a time. At 128 bits that
//     means 2 bits per append, straddling byte boundaries — which is why
//     bitmap.Builder owns the packing rather than the kernel.
//  3. Under emulation there is no movemask at all, so each chunk falls back to the
//     scalar kernel rather than silently producing nothing.
func simdCompareF64(
	out *bitmap.Builder,
	a []float64,
	cmp func(v, s simd.Float64s) simd.Mask64s,
	scalarFallback func(*bitmap.Builder, []float64),
	s float64,
) {
	vs := simd.BroadcastFloat64s(s)
	for i := 0; i < len(a); {
		v, _ := simd.LoadFloat64sPart(a[i:])
		n := step(len(a)-i, v.Len())

		bits, _, ok := maskBits64(cmp(v, vs))
		if !ok {
			// Emulated mode: no hardware movemask. Do this chunk scalar-side.
			scalarFallback(out, a[i:i+n])
			i += n
			continue
		}
		if n < 64 {
			bits &= (uint64(1) << uint(n)) - 1 // drop the zero-filled tail lanes
		}
		out.AppendBits(bits, n)
		i += n
	}
}

func gtF64SIMDConst(out *bitmap.Builder, a []float64, s float64) {
	simdCompareF64(out, a,
		func(v, vs simd.Float64s) simd.Mask64s { return v.Greater(vs) },
		func(b *bitmap.Builder, chunk []float64) { gtScalarConst(b, chunk, s) }, s)
}

func geF64SIMDConst(out *bitmap.Builder, a []float64, s float64) {
	simdCompareF64(out, a,
		func(v, vs simd.Float64s) simd.Mask64s { return v.GreaterEqual(vs) },
		func(b *bitmap.Builder, chunk []float64) { geScalarConst(b, chunk, s) }, s)
}

func ltF64SIMDConst(out *bitmap.Builder, a []float64, s float64) {
	simdCompareF64(out, a,
		func(v, vs simd.Float64s) simd.Mask64s { return v.Less(vs) },
		func(b *bitmap.Builder, chunk []float64) { ltScalarConst(b, chunk, s) }, s)
}

func leF64SIMDConst(out *bitmap.Builder, a []float64, s float64) {
	simdCompareF64(out, a,
		func(v, vs simd.Float64s) simd.Mask64s { return v.LessEqual(vs) },
		func(b *bitmap.Builder, chunk []float64) { leScalarConst(b, chunk, s) }, s)
}

func eqF64SIMDConst(out *bitmap.Builder, a []float64, s float64) {
	simdCompareF64(out, a,
		func(v, vs simd.Float64s) simd.Mask64s { return v.Equal(vs) },
		func(b *bitmap.Builder, chunk []float64) { eqScalarConst(b, chunk, s) }, s)
}

func neF64SIMDConst(out *bitmap.Builder, a []float64, s float64) {
	simdCompareF64(out, a,
		func(v, vs simd.Float64s) simd.Mask64s { return v.NotEqual(vs) },
		func(b *bitmap.Builder, chunk []float64) { neScalarConst(b, chunk, s) }, s)
}

// addF64SIMD is the arithmetic proof. Note it advances by StorePart's return
// value, which is trustworthy in both modes, rather than by LoadPart's — see step.
func addF64SIMD(dst, a, b []float64) {
	for i := 0; i < len(a); {
		va, _ := simd.LoadFloat64sPart(a[i:])
		vb, _ := simd.LoadFloat64sPart(b[i:])
		n := va.Add(vb).StorePart(dst[i:])
		if n == 0 {
			// Defensive: a zero step would spin forever. Cannot happen with a
			// non-empty destination, but an infinite loop is a much worse failure
			// than a wrong answer, so it is worth one comparison per iteration.
			panic("kernel: StorePart made no progress")
		}
		i += n
	}
}

func mulF64SIMD(dst, a, b []float64) {
	for i := 0; i < len(a); {
		va, _ := simd.LoadFloat64sPart(a[i:])
		vb, _ := simd.LoadFloat64sPart(b[i:])
		n := va.Mul(vb).StorePart(dst[i:])
		if n == 0 {
			panic("kernel: StorePart made no progress")
		}
		i += n
	}
}

// simdAvailable reports whether hardware SIMD is in use, for Profile output and
// for tests that want to assert both paths were actually exercised.
func simdAvailable() bool { return !simd.Emulated() }

// simdWidth returns the active vector width in bits.
func simdWidth() int { return simd.VectorBitSize() }
