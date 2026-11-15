// Package bitmap is ursus's validity-bitmap layer.
//
// # The offset problem
//
// Arrow arrays carry a row offset, and that offset applies to the validity bitmap
// as well as to the values buffer. Slicing an array is O(1) precisely because it
// only bumps the offset — which means a sliced column's validity bitmap starts at
// an arbitrary BIT, not at a byte boundary.
//
// This is the single most error-prone thing about writing Arrow kernels. The
// natural-looking code
//
//	if buf[i/8]&(1<<(i%8)) != 0 { ... }
//
// is correct for an unsliced array and silently wrong for a sliced one, and the
// wrongness is data-dependent, so it survives casual testing.
//
// This package's answer is to make the offset unforgeable. A View owns its buffer
// and offset in unexported fields; there is no way to obtain the raw bytes without
// also being handed the bit offset (RawBits), and index arithmetic is done here,
// once, rather than at every call site. Kernels take Views, never []byte.
//
// # Two bitmaps, not one
//
// A Boolean column has TWO bitmaps: values and validity. They have identical
// layout and both are Views. Conflating them is defect D13(3) in the design
// review — `null > 5` must be null, which needs the comparison kernel to write
// the value bit and the dispatcher to write the validity bit separately.
package bitmap

import (
	"encoding/binary"
	"math/bits"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow/bitutil"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/internal/arrowx"
)

// View is a read-only window onto a bit buffer.
//
// The zero View is valid and means "all bits set" — which for a validity bitmap is
// "no nulls". Arrow allows omitting the validity buffer entirely when null_count
// is zero, and representing that as the zero value keeps the common no-nulls path
// allocation-free and branch-predictable.
//
// # All clear has no storage either (step 529)
//
// Zeros, n bits all clear, is the other form with no buffer: the validity of a
// column of nulls, a Null column's above all. It held a bit a row, so a Null column
// of 2^40 rows, which an Arrow IPC stream of 232 bytes declares, needed 128 GiB to
// say what its type says already; Polars holds one in constant space. Every reader
// of a View is a method or a function of this package, so each knows both forms;
// RawBits, the one way to the bytes, hands over none for either.
type View struct {
	buf    []byte // nil means no storage: all set, or all clear at offset clearOffset
	offset int    // bit offset of bit 0 of this view within buf
	length int    // number of bits in this view
}

// clearOffset is the offset of the all-clear form, which has no buffer: the all-set
// form's is always 0.
const clearOffset = -1

// AllSet returns a view of n bits, all set, with no backing storage.
func AllSet(n int) View { return View{length: n} }

// NewView wraps buf as a bitmap of length bits starting at bit offset.
//
// The caller must not mutate buf afterwards: Views are shared freely between
// batches and goroutines and are assumed immutable.
func NewView(buf []byte, offset, length int) View {
	if buf == nil {
		return View{length: length}
	}
	return View{buf: buf, offset: offset, length: length}
}

// Len returns the number of bits.
func (v View) Len() int { return v.length }

// IsAllSet reports whether the view is the no-storage all-set form. It does NOT
// scan; a materialised bitmap that happens to be all ones returns false. Use it
// to take a fast path, never to decide correctness.
func (v View) IsAllSet() bool { return v.buf == nil && v.offset == 0 }

// IsAllClear reports whether the view is the no-storage all-clear form, Zeros'. Like
// IsAllSet it does not scan: a fast path, never a decision.
func (v View) IsAllClear() bool { return v.buf == nil && v.offset == clearOffset }

// Get returns bit i, where i is relative to this view.
func (v View) Get(i int) bool {
	if v.buf == nil {
		return v.offset == 0
	}
	return bitutil.BitIsSet(v.buf, v.offset+i)
}

// CountSet returns the number of set bits, i.e. the valid count.
func (v View) CountSet() int {
	if v.buf == nil {
		if v.offset == clearOffset {
			return 0
		}
		return v.length
	}
	return bitutil.CountSetBits(v.buf, v.offset, v.length)
}

// CountUnset returns the number of clear bits, i.e. the null count.
func (v View) CountUnset() int { return v.length - v.CountSet() }

// Slice returns bits [offset, offset+length) of v, composing the bit offsets.
// This is the only correct way to slice a bitmap and is O(1).
func (v View) Slice(offset, length int) View {
	if offset < 0 || length < 0 || offset+length > v.length {
		panic("bitmap: Slice out of range")
	}
	if v.buf == nil {
		return View{offset: v.offset, length: length} // either form, unchanged
	}
	return View{buf: v.buf, offset: v.offset + offset, length: length}
}

// RawBits exposes the backing buffer and this view's bit offset together, so a
// word-at-a-time kernel can use them without being able to forget the offset.
// Returns (nil, 0) for either form with no storage, all set or all clear: a caller
// asks IsAllSet which, never the nil.
func (v View) RawBits() (buf []byte, bitOffset int) {
	if v.buf == nil {
		return nil, 0
	}
	return v.buf, v.offset
}

// IsByteAligned reports whether the view starts on a byte boundary.
//
// No caller today. Kept because the offset arithmetic it encapsulates is easy to
// get wrong by hand, and because a View that is byte-aligned admits whole-byte
// copies that a shifted one does not.
func (v View) IsByteAligned() bool { return v.buf == nil || v.offset%8 == 0 }

// Words iterates the view as 64-bit little-endian words, LSB = lowest bit index,
// shifting to normalise away any bit offset. The final word is zero-padded above
// v.length bits; nbits reports how many bits of it are live.
//
// This is the abstraction that makes offset handling a solved problem rather than
// a recurring bug: a kernel written against Words is correct for sliced and
// unsliced input alike, at every vector width.
func (v View) Words(yield func(w uint64, nbits int) bool) {
	remaining := v.length
	if remaining == 0 {
		return
	}
	if v.buf == nil {
		clear := v.offset == clearOffset
		for remaining > 0 {
			n := min(remaining, 64)
			w := ^uint64(0)
			if clear {
				w = 0
			} else if n < 64 {
				w = (uint64(1) << uint(n)) - 1
			}
			if !yield(w, n) {
				return
			}
			remaining -= n
		}
		return
	}

	bitPos := v.offset
	for remaining > 0 {
		n := min(remaining, 64)
		if !yield(v.word(bitPos, n), n) {
			return
		}
		bitPos += n
		remaining -= n
	}
}

// word extracts n (<=64) bits starting at absolute bit position pos, normalised so
// the first bit lands in bit 0. Bits above n are zero.
func (v View) word(pos, n int) uint64 {
	byteIdx := pos >> 3
	shift := uint(pos & 7)

	// One 8-byte load, and the 9th byte only when the shift straddles it. The loop
	// below assembled every word a byte at a time, under every Words caller.
	if byteIdx+9 <= len(v.buf) {
		w := binary.LittleEndian.Uint64(v.buf[byteIdx:])
		if shift > 0 {
			w = w>>shift | uint64(v.buf[byteIdx+8])<<(64-shift)
		}
		return maskLow(w, n)
	}

	var w uint64
	// Read up to 9 bytes: 8 for the word plus 1 for a straddling shift.
	need := (int(shift) + n + 7) / 8
	for i := range need {
		if byteIdx+i < len(v.buf) {
			w |= uint64(v.buf[byteIdx+i]) << (8 * uint(i))
		}
	}
	if need > 8 {
		// The shift pushed us into a 9th byte; fold it into the top.
		if byteIdx+8 < len(v.buf) {
			hi := uint64(v.buf[byteIdx+8])
			w >>= shift
			w |= hi << (64 - shift)
			return maskLow(w, n)
		}
	}
	w >>= shift
	return maskLow(w, n)
}

// AppendSetPositions appends to dst every position, relative to the views, where a is
// set and b is too. Both views must have the same length.
//
// A word at a time: a run of unset bits costs one test, and a set bit one
// TrailingZeros. It is how a filter's mask becomes a selection.
func AppendSetPositions(dst []int32, a, b View) []int32 {
	if a.IsAllClear() || b.IsAllClear() {
		return dst
	}
	n := a.length
	for rel := 0; rel < n; rel += 64 {
		k := min(64, n-rel)
		w := a.wordRel(rel, k)
		if !b.IsAllSet() {
			w &= b.wordRel(rel, k)
		}
		for w != 0 {
			dst = append(dst, int32(rel+bits.TrailingZeros64(w)))
			w &= w - 1
		}
	}
	return dst
}

// wordRel is word at a position relative to the view, with the no-storage forms'
// all-ones and all-zeros.
func (v View) wordRel(rel, n int) uint64 {
	if v.buf == nil {
		if v.offset == clearOffset {
			return 0
		}
		return maskLow(^uint64(0), n)
	}
	return v.word(v.offset+rel, n)
}

func maskLow(w uint64, n int) uint64 {
	if n >= 64 {
		return w
	}
	return w & ((uint64(1) << uint(n)) - 1)
}

// PopCount returns the number of set bits, using Words so it is offset-correct.
// Prefer CountSet, which delegates to arrow's SIMD implementation; this exists as
// the reference the Words iterator is tested against.
func (v View) PopCount() int {
	n := 0
	v.Words(func(w uint64, _ int) bool {
		n += bits.OnesCount64(w)
		return true
	})
	return n
}

// --- combinators -------------------------------------------------------------

// And returns a AND b, allocating only when both operands are materialised.
//
// This is the operation behind null propagation for every total binary kernel:
// `valid_out = valid_a AND valid_b`. It is deliberately cheap in the common case
// where at least one side has no nulls, and in the case where one side is all null.
func And(a, b View) View {
	if a.length != b.length {
		panic("bitmap: And length mismatch")
	}
	switch {
	case a.IsAllClear():
		return a
	case b.IsAllClear():
		return b
	case a.IsAllSet():
		return b
	case b.IsAllSet():
		return a
	}
	return zip(a, b, func(x, y uint64) uint64 { return x & y })
}

// zip combines two views word by word.
//
// # Why this is our loop and not bitutil.BitmapAnd
//
// arrow-go v18.7.0's alignedBitmapOp computes its trailing-byte mask as
//
//	endMask := (lOffset + length%8)      // bitmaps.go:535
//
// which parses as `lOffset + (length % 8)` where it plainly means
// `(lOffset + length) % 8` — "does this range end mid-byte?". The consequence is
// exact and silent: whenever the source offset is non-zero and the range ends on
// a byte boundary, endMask is non-zero, lastByteMask becomes TrailingBitmask[0]
// which is 0xFF, and the final byte of the output is masked out entirely — left
// as whatever the freshly allocated destination held, which is zeros.
//
// So every And, Or and AndNot over a view with a non-zero bit offset lost its
// LAST EIGHT BITS. Every operand sliced out of a larger column has such an
// offset, which is every batch after the first: h2o gb7 returned 96 nulls out of
// 100,000 groups because `max(v1) - min(v2)` combines two validity bitmaps per
// output batch, and eleven of its twelve batches started at a non-zero offset.
//
// Words and AppendBits already exist precisely because "offset handling is a
// solved problem rather than a recurring bug" — this is that claim being cashed.
//
// Each operand is read through wordRel, so either may be a form with no storage,
// though the combinators answer those without a loop.
func zip(a, b View, op func(x, y uint64) uint64) View {
	out := NewBuilder(a.length)
	for pos := 0; pos < a.length; {
		n := min(a.length-pos, 64)
		out.AppendBits(op(a.wordRel(pos, n), b.wordRel(pos, n)), n)
		pos += n
	}
	return out.Finish()
}

// AndMulti folds And over any number of views. Views that are all-set are skipped.
func AndMulti(vs ...View) View {
	var acc View
	first := true
	for _, v := range vs {
		if first {
			acc, first = v, false
			continue
		}
		if !v.IsAllSet() {
			acc = And(acc, v)
		}
	}
	return acc
}

// Or returns a OR b.
func Or(a, b View) View {
	if a.length != b.length {
		panic("bitmap: Or length mismatch")
	}
	switch {
	case a.IsAllSet() || b.IsAllSet():
		return AllSet(a.length) // one side is all-set, so the result is
	case a.IsAllClear():
		return b
	case b.IsAllClear():
		return a
	}
	return zip(a, b, func(x, y uint64) uint64 { return x | y })
}

// AndNot returns a AND NOT b.
func AndNot(a, b View) View {
	if a.length != b.length {
		panic("bitmap: AndNot length mismatch")
	}
	switch {
	case b.IsAllSet() || a.IsAllClear():
		return Zeros(a.length) // NOT all-set is all-clear, and so is anything AND it
	case b.IsAllClear():
		return a
	case a.IsAllSet():
		return Not(b)
	}
	return zip(a, b, func(x, y uint64) uint64 { return x &^ y })
}

// Not returns the complement of v.
func Not(v View) View {
	switch {
	case v.IsAllSet():
		return Zeros(v.length)
	case v.IsAllClear():
		return AllSet(v.length)
	}
	// Our own loop for the same reason zip has one: this took bitutil.InvertBitmap,
	// whose neighbours in that file carry the trailing-byte defect zip documents.
	// AppendBits masks to the live bit count, so the garbage above n in ^word is
	// discarded rather than needing a mask here.
	out := NewBuilder(v.length)
	for pos := 0; pos < v.length; {
		n := min(v.length-pos, 64)
		out.AppendBits(^v.word(v.offset+pos, n), n)
		pos += n
	}
	return out.Finish()
}

// Zeros returns n bits, all clear, with no storage (step 529): the all-clear form,
// which allocates nothing at any n. It was a materialised bitmap of zeros, a bit a
// row of a column that is all null.
func Zeros(n int) View {
	if MaterialiseZeros.Load() {
		return NewView(arrowx.NewBuffer(int(bitutil.BytesForBits(int64(n)))).Bytes(), 0, n)
	}
	return View{offset: clearOffset, length: n}
}

// MaterialiseZeros makes Zeros allocate its bits, as it did before step 529: the
// reference of the differential that holds every answer the same with the all-clear
// form and without it. Set by a test, never during a query.
var MaterialiseZeros atomic.Bool

// Buffer returns the view's storage as an Arrow buffer for handing to array.NewData,
// materialising the all-clear form if necessary. Returns nil for an all-set view,
// which is what Arrow expects when null_count is zero.
func (v View) Buffer() *memory.Buffer {
	if v.IsAllClear() {
		// Arrow has no form of all null without a buffer but the Null type's, so
		// the bits are made here, and only here: zeroed by the allocator.
		return arrowx.NewBuffer(int(bitutil.BytesForBits(int64(v.length))))
	}
	if v.buf == nil {
		return nil
	}
	if v.offset == 0 {
		return memory.NewBufferBytes(v.buf)
	}
	// A non-zero offset must be normalised before the bitmap can stand alone.
	out := arrowx.NewBuffer(int(bitutil.BytesForBits(int64(v.length))))
	bitutil.CopyBitmap(v.buf, v.offset, v.length, out.Bytes(), 0)
	return out
}
