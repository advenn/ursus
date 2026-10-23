//go:build goexperiment.simd && amd64

package ndjson

import (
	"math/bits"
	"simd/archsimd"

	"github.com/advenn/ursus/internal/kernel"
)

// vectorScan is whether an object is read sixty-four bytes at a time: on a CPU with
// AVX2, at the vector width the process runs at, so that GODEBUG=simd=128 and 0 run
// the byte scan, as the tests' width matrix does.
var vectorScan = archsimd.X86.AVX2() && kernel.Accelerated() && kernel.VectorBits() >= 256

// objectVector reads the object begun from b[r] a block of sixty-four bytes at a
// time, as objectBytes reads it a byte at a time, while a whole block is left and
// holds no backslash, and gives where it stopped and whether the object ended there.
//
// A block is read as five masks of a bit a byte: its quotes, its opening and closing
// brackets, its newlines and its backslashes. With no backslash, every quote opens or
// closes a string, so the bits a string spans, its opening quote's to its closing
// one's, are the quotes' running parity, prefixXor, flipped where the block starts in
// one; brackets and newlines outside them are the object's. Its brackets are followed
// one by one only where the block holds as many closing ones as are open, and so may
// end the object.
func (o *objectLines) objectVector(b []byte, r int) (int, bool) {
	quote := archsimd.BroadcastUint8x32('"')
	slash := archsimd.BroadcastUint8x32('\\')
	newline := archsimd.BroadcastUint8x32('\n')
	lower := archsimd.BroadcastUint8x32(0x20) // '[' | 0x20 is '{', and ']' | 0x20 '}'
	open := archsimd.BroadcastUint8x32('{')
	shut := archsimd.BroadcastUint8x32('}')
	for ; r+64 <= len(b); r += 64 {
		x, y := archsimd.LoadUint8x32(b[r:r+32]), archsimd.LoadUint8x32(b[r+32:r+64])
		if x.Equal(slash).ToBits()|y.Equal(slash).ToBits() != 0 {
			return r, false
		}
		lx, ly := x.Or(lower), y.Or(lower)
		in := prefixXor(mask(x.Equal(quote), y.Equal(quote)))
		if o.str {
			in = ^in
		}
		opens := mask(lx.Equal(open), ly.Equal(open)) &^ in
		shuts := mask(lx.Equal(shut), ly.Equal(shut)) &^ in
		newlines := mask(x.Equal(newline), y.Equal(newline)) &^ in
		if shuts != 0 && o.depth <= bits.OnesCount64(shuts) {
			d := o.depth
			for m := opens | shuts; m != 0; m &= m - 1 {
				i := bits.TrailingZeros64(m)
				if opens>>i&1 == 1 {
					d++
					continue
				}
				if d--; d == 0 {
					spaces(b[r:], newlines&(1<<i-1))
					o.depth, o.str, o.nl = 0, false, true
					return r + i + 1, true
				}
			}
		}
		spaces(b[r:], newlines)
		o.depth += bits.OnesCount64(opens) - bits.OnesCount64(shuts)
		o.str = in>>63 == 1
	}
	return r, false
}

// mask is two blocks' masks as one of sixty-four bits, m's the low ones.
func mask(m, n archsimd.Mask8x32) uint64 { return uint64(m.ToBits()) | uint64(n.ToBits())<<32 }

// prefixXor is the running parity of m's bits: bit i of it is the xor of m's bits 0
// to i.
func prefixXor(m uint64) uint64 {
	m ^= m << 1
	m ^= m << 2
	m ^= m << 4
	m ^= m << 8
	m ^= m << 16
	return m ^ m<<32
}

// spaces makes a space of each byte of b that m marks.
func spaces(b []byte, m uint64) {
	for ; m != 0; m &= m - 1 {
		b[bits.TrailingZeros64(m)] = ' '
	}
}
