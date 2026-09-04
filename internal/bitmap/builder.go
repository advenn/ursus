package bitmap

import (
	"github.com/apache/arrow-go/v18/arrow/bitutil"

	"ursus/internal/arrowx"
)

// Builder accumulates bits and produces a View.
//
// # Why this exists rather than direct byte indexing
//
// A SIMD comparison kernel produces its result as a movemask: one bit per lane,
// packed into an integer. The number of lanes is a RUNTIME property — 8 float64
// lanes on AVX-512, 4 on AVX2, 2 on SSE/Neon, and 1 at a time under software
// emulation. On the AVX-512 development machine 8 lanes happens to be exactly one
// bitmap byte, which makes the naive
//
//	out[i/8] = mask.ToBits()
//
// look correct. At 128-bit width it writes 2 bits at a time and every write after
// the first straddles a byte boundary, silently corrupting the bitmap. That is
// defect D14b from the design review, and it is invisible on the machine that
// wrote the code.
//
// AppendBits removes the whole class of bug: the caller says "here are n bits" and
// the Builder owns the packing. It is correct at every vector width, and it is
// what makes the GODEBUG=simd=128 and simd=0 CI legs meaningful rather than
// decorative.
type Builder struct {
	b    []byte // destination, 64-byte aligned
	pos  int    // next byte to write
	acc  uint64 // pending bits, LSB first
	nacc int    // number of pending bits in acc
	n    int    // total bits appended
}

// NewBuilder returns a Builder preallocated for capBits bits.
func NewBuilder(capBits int) *Builder {
	if capBits < 0 {
		capBits = 0
	}
	return &Builder{b: arrowx.NewBuffer(int(bitutil.BytesForBits(int64(capBits)))).Bytes()}
}

// Len returns the number of bits appended so far.
func (w *Builder) Len() int { return w.n }

// Append adds one bit.
func (w *Builder) Append(v bool) {
	var bit uint64
	if v {
		bit = 1
	}
	w.acc |= bit << uint(w.nacc)
	w.nacc++
	w.n++
	w.drain()
}

// AppendBits adds the low n bits of word, LSB first — bit 0 of word becomes the
// next bit of the bitmap.
//
// LSB-first is not a choice: it is Arrow's bitmap bit order, and it is also the
// order archsimd's Mask.ToBits() produces (lane 0 in bit 0), which is why a mask
// can be appended directly with no bit reversal.
func (w *Builder) AppendBits(word uint64, n int) {
	if n < 0 || n > 64 {
		panic("bitmap: AppendBits n out of range")
	}
	for n > 0 {
		// Cap each chunk at 56 so that nacc (always < 8 after drain) plus the
		// chunk cannot exceed 63 bits and overflow the accumulator.
		take := min(n, 56)
		w.acc |= maskLow(word, take) << uint(w.nacc)
		w.nacc += take
		w.n += take
		word >>= uint(take)
		n -= take
		w.drain()
	}
}

// AppendMany adds n copies of v. Used for null-padding and for the all-valid case.
func (w *Builder) AppendMany(v bool, n int) {
	if !v {
		for n > 0 {
			take := min(n, 56)
			w.AppendBits(0, take)
			n -= take
		}
		return
	}
	for n > 0 {
		take := min(n, 56)
		w.AppendBits(^uint64(0), take)
		n -= take
	}
}

// AppendView copies another view's bits. Offset-correct by construction.
func (w *Builder) AppendView(v View) {
	v.Words(func(word uint64, nbits int) bool {
		w.AppendBits(word, nbits)
		return true
	})
}

// drain moves whole bytes out of the accumulator into the buffer.
func (w *Builder) drain() {
	for w.nacc >= 8 {
		w.ensure(w.pos + 1)
		w.b[w.pos] = byte(w.acc)
		w.pos++
		w.acc >>= 8
		w.nacc -= 8
	}
}

func (w *Builder) ensure(n int) {
	if n <= len(w.b) {
		return
	}
	grow := max(n, len(w.b)*2, 64)
	nb := arrowx.NewBuffer(grow).Bytes()
	copy(nb, w.b[:w.pos])
	w.b = nb
}

// Finish flushes any partial byte and returns the completed view. The Builder must
// not be used afterwards.
//
// Trailing bits in the final byte are left zero, which matters: Arrow permits any
// value there, but leaving them zero makes bitmaps byte-comparable in tests and
// makes a stray read past the end behave as "null" rather than as garbage.
func (w *Builder) Finish() View {
	if w.nacc > 0 {
		w.ensure(w.pos + 1)
		w.b[w.pos] = byte(w.acc)
		w.pos++
		w.acc, w.nacc = 0, 0
	}
	return NewView(w.b, 0, w.n)
}
