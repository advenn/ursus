//go:build goexperiment.simd && amd64

package kernel

import "simd/archsimd"

// utf8Vector is whether ValidUTF8 reads thirty-two bytes a step: on a CPU with AVX2,
// at the vector width the process runs at, so that GODEBUG=simd=128 and 0 run
// utf8.Valid, as the tests' width matrix does.
var utf8Vector = archsimd.X86.AVX2() && simdAvailable() && simdWidth() >= 256

// The errors a byte and the one before it can make, a bit each, by Keiser and
// Lemire's "Validating UTF-8 in less than one instruction per byte" (2021), as
// simdjson and simdutf8 find them: three tables of sixteen give each a byte's high
// nibble, the byte before's high nibble and its low nibble may make, and a pair is
// in error where the three agree on a bit.
const (
	u8TooShort   = 1 << 0 // a lead byte, then no continuation: 11______ 0_______ or 11______ 11______
	u8TooLong    = 1 << 1 // a continuation after ASCII: 0_______ 10______
	u8Overlong3  = 1 << 2 // 11100000 100_____
	u8TooLarge   = 1 << 3 // past U+10FFFF: 11110100 1001____, 11110100 101_____, 111101__ 10______, 11111___ 10______
	u8Surrogate  = 1 << 4 // 11101101 101_____
	u8Overlong2  = 1 << 5 // 1100000_ 10______
	u8TooLarge1k = 1 << 6 // 11110101 1000____, 1111011_ 1000____, 11111___ 1000____
	u8Overlong4  = 1 << 6 // 11110000 1000____
	u8TwoConts   = 1 << 7 // 10______ 10______, an error but where a third or a fourth byte is due
	u8Carry      = u8TooShort | u8TooLong | u8TwoConts
)

// Each table twice, once for each 128-bit group a lookup reads within.
var (
	u8Byte1High = twice([16]uint8{
		u8TooLong, u8TooLong, u8TooLong, u8TooLong, u8TooLong, u8TooLong, u8TooLong, u8TooLong,
		u8TwoConts, u8TwoConts, u8TwoConts, u8TwoConts,
		u8TooShort | u8Overlong2,
		u8TooShort,
		u8TooShort | u8Overlong3 | u8Surrogate,
		u8TooShort | u8TooLarge | u8TooLarge1k | u8Overlong4,
	})
	u8Byte1Low = twice([16]uint8{
		u8Carry | u8Overlong3 | u8Overlong2 | u8Overlong4,
		u8Carry | u8Overlong2,
		u8Carry,
		u8Carry,
		u8Carry | u8TooLarge,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k | u8Surrogate,
		u8Carry | u8TooLarge | u8TooLarge1k,
		u8Carry | u8TooLarge | u8TooLarge1k,
	})
	u8Byte2High = twice([16]uint8{
		u8TooShort, u8TooShort, u8TooShort, u8TooShort, u8TooShort, u8TooShort, u8TooShort, u8TooShort,
		u8TooLong | u8Overlong2 | u8TwoConts | u8Overlong3 | u8TooLarge1k | u8Overlong4,
		u8TooLong | u8Overlong2 | u8TwoConts | u8Overlong3 | u8TooLarge,
		u8TooLong | u8Overlong2 | u8TwoConts | u8Surrogate | u8TooLarge,
		u8TooLong | u8Overlong2 | u8TwoConts | u8Surrogate | u8TooLarge,
		u8TooShort, u8TooShort, u8TooShort, u8TooShort,
	})
	// A vector's last three bytes begin a character it does not end where they
	// pass these: 1111____ third last, 111_____ second last, 11______ last.
	u8Incomplete = func() (t [32]uint8) {
		for i := range t {
			t[i] = 0xFF
		}
		t[29], t[30], t[31] = 0xF0-1, 0xE0-1, 0xC0-1
		return t
	}()
)

func twice(t [16]uint8) (d [32]uint8) {
	copy(d[:16], t[:])
	copy(d[16:], t[:])
	return d
}

// validUTF8Vector is utf8.Valid thirty-two bytes a step, sixty-four of ASCII by one
// test. Of any other vector, each byte is checked with the three before it, the
// last of the vector before carried over; a vector of ASCII after one that ended
// within a character is an error, and the last is read padded with zeros, ASCII, so
// that a character it does not end is an error too.
func validUTF8Vector(b []byte) bool {
	t := u8Tables{
		hi1: archsimd.LoadUint8x32Array(&u8Byte1High), lo1: archsimd.LoadUint8x32Array(&u8Byte1Low),
		hi2: archsimd.LoadUint8x32Array(&u8Byte2High), maxv: archsimd.LoadUint8x32Array(&u8Incomplete),
		nibble: archsimd.BroadcastUint8x32(0x0F), top: archsimd.BroadcastUint8x32(0x80),
		third: archsimd.BroadcastUint8x32(0xE0 - 0x80), fourth: archsimd.BroadcastUint8x32(0xF0 - 0x80),
	}
	var zero, prev, errs, incomplete archsimd.Uint8x32
	i := 0
	for ; i+64 <= len(b); i += 64 {
		x, y := archsimd.LoadUint8x32(b[i:i+32]), archsimd.LoadUint8x32(b[i+32:i+64])
		if x.Or(y).And(t.top).IsZero() {
			errs = errs.Or(incomplete)
			prev, incomplete = zero, zero
			continue
		}
		errs = errs.Or(t.check(x, prev))
		errs = errs.Or(t.check(y, x))
		prev, incomplete = y, y.SubSaturated(t.maxv)
	}
	var last [64]uint8
	copy(last[:], b[i:])
	x, y := archsimd.LoadUint8x32Array((*[32]uint8)(last[:32])), archsimd.LoadUint8x32Array((*[32]uint8)(last[32:]))
	errs = errs.Or(t.check(x, prev)).Or(t.check(y, x))
	return errs.IsZero()
}

// u8Tables is the vectors validUTF8Vector reads with.
type u8Tables struct {
	hi1, lo1, hi2, maxv, nibble, top, third, fourth archsimd.Uint8x32
}

// check is the errors of the vector in, prev the one before it.
func (t *u8Tables) check(in, prev archsimd.Uint8x32) archsimd.Uint8x32 {
	// The previous vector's high group and this one's low group, from which each
	// group's bytes one, two and three before are shifted in.
	cross := prev.ConcatPermute128Scalars(1, 2, in)
	prev1 := in.ConcatShiftBytesRightGrouped(cross, 16-1)
	prev2 := in.ConcatShiftBytesRightGrouped(cross, 16-2)
	prev3 := in.ConcatShiftBytesRightGrouped(cross, 16-3)
	special := t.hi1.PermuteOrZeroGrouped(highNibble(prev1, t.nibble)).
		And(t.lo1.PermuteOrZeroGrouped(prev1.And(t.nibble).AsInt8x32())).
		And(t.hi2.PermuteOrZeroGrouped(highNibble(in, t.nibble)))
	// A third or a fourth byte is due where the byte two before is 111_____ or the
	// byte three before 1111____: the one pair of continuations that is no error.
	due := prev2.SubSaturated(t.third).Or(prev3.SubSaturated(t.fourth)).And(t.top)
	return due.Xor(special)
}

// highNibble is each byte's high four bits, as an index of a lookup.
func highNibble(v, nibble archsimd.Uint8x32) archsimd.Int8x32 {
	return v.AsUint16x16().ShiftAllRight(4).AsUint8x32().And(nibble).AsInt8x32()
}
