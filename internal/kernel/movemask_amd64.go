//go:build goexperiment.simd && amd64

package kernel

import (
	"simd"
	"simd/archsimd"
)

// maskBits64 extracts one bit per lane from a 64-bit-element mask, LSB = lane 0.
//
// # Why this function exists
//
// The portable `simd` package has NO movemask: simd.Mask64s offers exactly And,
// Or, String, ToArch and ToInt64s. The bit-extraction lives in `simd/archsimd`,
// reachable only through `ToArch() any` plus a type assertion.
//
// And that assertion is the trap. Vector width is a RUNTIME property — GODEBUG=simd=N
// reselects it at process start — so the concrete type is Mask64x8 on AVX-512,
// Mask64x4 on AVX2, Mask64x2 on SSE, and an unnameable simd/internal/bridge type
// under software emulation. Code that hardcodes
//
//	m.ToArch().(archsimd.Mask64x8)
//
// works perfectly on the machine it was written on and panics with
//
//	interface conversion: interface {} is archsimd.Mask64x4, not archsimd.Mask64x8
//
// on a slightly older CPU. Every movemask site funnels through here so that
// assertion is written exactly once.
//
// Bit order is LSB-first, which is not a coincidence: archsimd's ToBits() puts
// lane 0 in bit 0, and Arrow's validity bitmaps are also LSB-first, so a mask can
// be appended to a bitmap with no reversal.
//
// ok=false means "no hardware path" and the caller must fall back to scalar. That
// happens under GODEBUG=simd=0, which is exactly why the CI matrix runs that leg.
func maskBits64(m simd.Mask64s) (bits uint64, lanes int, ok bool) {
	switch a := m.ToArch().(type) {
	case archsimd.Mask64x8:
		return uint64(a.ToBits()), 8, true
	case archsimd.Mask64x4:
		return uint64(a.ToBits()), 4, true
	case archsimd.Mask64x2:
		return uint64(a.ToBits()), 2, true
	default:
		return 0, 0, false
	}
}
