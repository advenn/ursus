// Package i128 provides a signed 128-bit integer.
//
// It exists for one reason: `sum` of an integer column must not silently overflow.
// Summing a thousand Int8 values overflows an Int8 almost immediately, and a
// wrapped sum is a plausible-looking wrong number that nothing downstream can
// detect. ursus follows DuckDB and accumulates integer sums at 128 bits, where
// overflow is unreachable for any real dataset: even Int64 values at the maximum
// magnitude would need 2^63 rows to overflow.
//
// The type is deliberately small and total. It has no division and no modulo —
// 128-bit division needs Knuth algorithm D and `sum(x) % 7` is not a query anyone
// writes. Division is available by casting to Int64 or Float64 first.
package i128

import (
	"math"
	"math/bits"
)

// Int128 is a signed 128-bit integer in two's complement.
//
// Hi carries the sign; Lo is the unsigned low half. The zero value is 0.
//
// It is comparable, so it can be a map key, and it is 16 bytes with no pointers,
// so a column of them is a flat SIMD-friendly buffer like any other numeric type.
type Int128 struct {
	Hi int64
	Lo uint64
}

// Common values.
var (
	Zero = Int128{}
	One  = Int128{0, 1}
	Min  = Int128{Hi: math.MinInt64, Lo: 0}
	Max  = Int128{Hi: math.MaxInt64, Lo: math.MaxUint64}
)

// FromInt64 sign-extends an int64.
func FromInt64(v int64) Int128 {
	if v < 0 {
		return Int128{Hi: -1, Lo: uint64(v)}
	}
	return Int128{Hi: 0, Lo: uint64(v)}
}

// FromUint64 zero-extends a uint64.
//
// Every uint64 fits in an Int128 with room to spare, which is why unsigned sums
// can share the signed accumulator — and incidentally why widening to 128 bits
// dissolves the Uint64/Int64 promotion problem that otherwise has no answer.
func FromUint64(v uint64) Int128 { return Int128{Hi: 0, Lo: v} }

// FromFloat64 truncates toward zero. ok is false if v is NaN, infinite, or outside
// the Int128 range.
func FromFloat64(f float64) (Int128, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Zero, false
	}
	f = math.Trunc(f)
	if f >= 1.7014118346046923e38 || f < -1.7014118346046923e38 {
		return Zero, false
	}
	neg := f < 0
	if neg {
		f = -f
	}
	hi := math.Floor(f / (1 << 64))
	lo := f - hi*(1<<64)
	v := Int128{Hi: int64(hi), Lo: uint64(lo)}
	if neg {
		v = v.Neg()
	}
	return v, true
}

// Add returns a+b, wrapping on overflow.
//
// Wrapping is safe here in a way it is not at 64 bits: reaching 2^127 requires
// summing 2^63 values of maximum magnitude, so the accumulator cannot overflow on
// any input that fits in memory. Callers that narrow the result to a smaller type
// do check — see Int64.
func (a Int128) Add(b Int128) Int128 {
	lo, carry := bits.Add64(a.Lo, b.Lo, 0)
	hi, _ := bits.Add64(uint64(a.Hi), uint64(b.Hi), carry)
	return Int128{Hi: int64(hi), Lo: lo}
}

// Sub returns a-b.
func (a Int128) Sub(b Int128) Int128 {
	lo, borrow := bits.Sub64(a.Lo, b.Lo, 0)
	hi, _ := bits.Sub64(uint64(a.Hi), uint64(b.Hi), borrow)
	return Int128{Hi: int64(hi), Lo: lo}
}

// Neg returns -a. Neg(Min) is Min, matching two's complement at every other width.
func (a Int128) Neg() Int128 { return Zero.Sub(a) }

// Cmp returns -1, 0 or 1.
func (a Int128) Cmp(b Int128) int {
	switch {
	case a.Hi != b.Hi:
		if a.Hi < b.Hi {
			return -1
		}
		return 1
	case a.Lo != b.Lo:
		if a.Lo < b.Lo {
			return -1
		}
		return 1
	default:
		return 0
	}
}

// Sign returns -1, 0 or 1.
func (a Int128) Sign() int {
	switch {
	case a.Hi < 0:
		return -1
	case a.Hi == 0 && a.Lo == 0:
		return 0
	default:
		return 1
	}
}

// IsZero reports whether a is zero.
func (a Int128) IsZero() bool { return a.Hi == 0 && a.Lo == 0 }

// Int64 narrows to an int64. ok is false if the value does not fit.
//
// The check is the point: this is the boundary where a 128-bit accumulator becomes
// a user-visible number, and it is where an overflow that was impossible internally
// must be reported rather than truncated.
func (a Int128) Int64() (int64, bool) {
	switch {
	case a.Hi == 0 && a.Lo <= math.MaxInt64:
		return int64(a.Lo), true
	case a.Hi == -1 && a.Lo >= 1<<63:
		return int64(a.Lo), true
	default:
		return 0, false
	}
}

// Uint64 narrows to a uint64. ok is false if the value is negative or too large.
func (a Int128) Uint64() (uint64, bool) {
	if a.Hi != 0 {
		return 0, false
	}
	return a.Lo, true
}

// Float64 converts to a float64, rounding when the value exceeds 2^53.
func (a Int128) Float64() float64 {
	if a.Hi < 0 {
		return -a.Neg().Float64()
	}
	return float64(a.Hi)*(1<<64) + float64(a.Lo)
}

// String renders the value in base 10.
//
// Implemented by repeated division rather than via math/big, so that the
// differential test against arrow's decimal128 is testing two genuinely
// independent implementations rather than two wrappers around the same one.
func (a Int128) String() string {
	if a.IsZero() {
		return "0"
	}

	neg := a.Hi < 0
	hi, lo := uint64(a.Hi), a.Lo
	if neg {
		// Negate in place. Min negates to itself, which is handled below because
		// the digit loop works on the unsigned magnitude either way.
		lo, hi = ^lo+1, ^hi
		if lo == 0 {
			hi++
		}
	}

	// 2^127 has 39 decimal digits.
	var buf [40]byte
	pos := len(buf)
	for hi != 0 || lo != 0 {
		var r uint64
		hi, lo, r = divmod10(hi, lo)
		pos--
		buf[pos] = byte('0' + r)
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// DivUint64 divides by a positive count, truncating toward zero. r is the
// remainder's MAGNITUDE; the remainder itself carries a's sign.
//
// It is deliberately not called Div and is not general division: the package doc
// refuses Knuth algorithm D, and 128÷64 needs none — it is two bits.Div64 calls, the
// shape divmod10 below has used since step 1. What it exists for is `mean`, whose
// exact sum can leave int64 while the mean itself cannot, so the division has to
// happen at 128 bits.
//
// Truncation rather than flooring, and the caller gets r so it can choose otherwise.
// A Duration is a span with a true origin at zero, so the symmetry that matters is
// mean(-x) == -mean(x), which truncation has; rescaleTemporal floors instead because
// an INSTANT's epoch is arbitrary and flooring keeps the mapping monotone across it.
//
// ok is false only for d == 0.
func (a Int128) DivUint64(d uint64) (q Int128, r uint64, ok bool) {
	if d == 0 {
		return Zero, 0, false
	}
	neg := a.Hi < 0
	m := a
	if neg {
		m = a.Neg()
	}
	// m now holds the MAGNITUDE, read as an unsigned pair — correct even for Min,
	// whose Neg is itself: Min's bit pattern IS 2^127 unsigned, which is exactly
	// |Min|. Reading it as signed here would give the wrong sign and the right bits,
	// which is the plausible wrong answer this package exists to prevent.
	hi, lo := uint64(m.Hi), m.Lo

	// bits.Div64 panics for a zero divisor or a quotient that overflows, i.e. when
	// its high word is >= the divisor. Neither call can reach that: the first passes
	// the CONSTANT 0 as its high word and d >= 1, and the second passes a remainder
	// of a division by d, which is < d by definition. That is divmod10's argument
	// with 10 generalised.
	qhi, rem := bits.Div64(0, hi, d)
	qlo, rem := bits.Div64(rem, lo, d)

	q = Int128{Hi: int64(qhi), Lo: qlo}
	if neg {
		q = q.Neg()
	}
	return q, rem, true
}

// divmod10 divides the 128-bit value (hi,lo) by 10.
//
// bits.Div64 panics unless its high word is less than the divisor, so the division
// is done in two halves with the remainder carried between them.
func divmod10(hi, lo uint64) (qhi, qlo, r uint64) {
	qhi, r = bits.Div64(0, hi, 10)
	qlo, r = bits.Div64(r, lo, 10)
	return qhi, qlo, r
}

// Parse reads a base-10 Int128. Used by tests and by future CSV parsing.
func Parse(s string) (Int128, bool) {
	if s == "" {
		return Zero, false
	}
	neg := false
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		s = s[1:]
		if s == "" {
			return Zero, false
		}
	}
	v := Zero
	for i := range len(s) {
		d := s[i]
		if d < '0' || d > '9' {
			return Zero, false
		}
		// v = v*10 + d, via shifts to avoid needing a general multiply.
		v8 := v.shl(3)
		v2 := v.shl(1)
		v = v8.Add(v2).Add(FromUint64(uint64(d - '0')))
	}
	if neg {
		v = v.Neg()
	}
	return v, true
}

// shl shifts left by n < 64.
func (a Int128) shl(n uint) Int128 {
	if n == 0 {
		return a
	}
	hi := uint64(a.Hi)<<n | a.Lo>>(64-n)
	return Int128{Hi: int64(hi), Lo: a.Lo << n}
}

// AppendBigEndian appends the 16-byte big-endian two's-complement encoding, with
// the sign bit flipped so that the byte ordering matches the value ordering. This
// is the form the group-key encoder and any future spill file use.
func (a Int128) AppendBigEndian(dst []byte) []byte {
	hi := uint64(a.Hi) ^ 0x8000_0000_0000_0000
	var b [16]byte
	putUint64BE(b[0:8], hi)
	putUint64BE(b[8:16], a.Lo)
	return append(dst, b[:]...)
}

func putUint64BE(b []byte, v uint64) {
	_ = b[7]
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
}

// GoString makes %#v readable in test failures.
func (a Int128) GoString() string { return "i128.Int128(" + a.String() + ")" }
