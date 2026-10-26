//go:build goexperiment.simd && amd64

package csv

import (
	"simd/archsimd"

	"github.com/advenn/ursus/internal/kernel"
)

// vectorFields is whether an unquoted field's end is found in a mask of sixty-four
// bytes: on a CPU with AVX2, at the vector width the process runs at, so that
// GODEBUG=simd=128 and 0 run the eight-byte scan, as the tests' width matrix does.
var vectorFields = archsimd.X86.AVX2() && kernel.Accelerated() && kernel.VectorBits() >= 256

// endsMask has bit k set where b[k] is sep or a newline, for b's first sixty-four
// bytes.
func endsMask(b []byte, sep byte) uint64 {
	x, y := archsimd.LoadUint8x32(b[:32]), archsimd.LoadUint8x32(b[32:64])
	s, n := archsimd.BroadcastUint8x32(sep), archsimd.BroadcastUint8x32('\n')
	return uint64(x.Equal(s).ToBits()|x.Equal(n).ToBits()) | uint64(y.Equal(s).ToBits()|y.Equal(n).ToBits())<<32
}
