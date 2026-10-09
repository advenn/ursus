package kernel

import (
	"math"
	"math/bits"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// MathCall evaluates the parameterised maths family.
//
// Round, two helpers the window sugar uses, and the bit counts. The family exists because a parameter belongs in a
// Call's argument list rather than in a field on expr.Unary — see the note on
// FnMathRound for the three hand-written Unary rebuilds that would have dropped it.
func MathCall(fn expr.CallFn, name string, out dtype.DataType,
	c *data.Column, args []any) (*data.Column, error) {

	switch fn {
	case expr.FnMathRound:
		d, err := intArg(fn, args, 0)
		if err != nil {
			return nil, err
		}
		return round(name, out, c, d)
	case expr.FnMathCountOnes, expr.FnMathCountZeros, expr.FnMathLeadingOnes,
		expr.FnMathLeadingZeros, expr.FnMathTrailingOnes, expr.FnMathTrailingZeros:
		return bitCount(fn, name, c)
	case expr.FnMathAsFloat, expr.FnMathDiffWiden:
		// ResolveCall typed it, so the cast cannot refuse: an integer, a Decimal or
		// a Duration's ticks are all far inside Float64, and an unsigned value
		// inside the signed type one width up.
		if c.DType() == out {
			return c.Rename(name), nil
		}
		return Cast(name, out, true, c)
	default:
		return nil, uerr.Internalf("kernel: no maths kernel for %s", fn)
	}
}

// round rounds to d decimal places, HALF AWAY FROM ZERO.
//
// # The tie-break is a decision, not an accident
//
// Go offers both: math.Round is half-away-from-zero, math.RoundToEven is banker's
// rounding. ursus takes half-away-from-zero, which is what a spreadsheet user
// expects — Round(0.5) is 1, and Round(2.5) is 3 rather than 2. Polars offers it as
// `mode="half_away_from_zero"`; its DEFAULT is half-to-even, measured on 1.44, so a
// round ported from Polars' default can differ here at an exact tie.
//
// # It rounds TWICE, and the second rounding is the one that surprises people
//
// Round(x, d) is math.Round(x * 10^d) / 10^d, so the scaling multiply rounds
// before math.Round ever sees the value. Round(2.675, 2) is 2.68 — not because
// 2.675 is exact (it is really 2.67499999999999982…) but because 2.675 * 100
// rounds to exactly 267.5, which then rounds away from zero to 268.
//
// So the answer is not "the decimal you wrote, rounded"; it is "the float you
// actually have, scaled and rounded". Every library that rounds binary floats
// this way lands in the same place, and the tests assert the specific values
// rather than papering over them.
func round(name string, out dtype.DataType, c *data.Column, d int) (*data.Column, error) {
	// Rounding an integer to any number of decimals is the identity, whatever the
	// width — including Int128, and including the unsigned types.
	if !out.IsFloat() {
		return c.Rename(name).WithDType(out), nil
	}

	scale := math.Pow(10, float64(d))
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	dst := make([]float64, len(src))
	for i, v := range src {
		s := v * scale
		if math.IsInf(s, 0) || math.IsNaN(s) {
			// Either v was already NaN or ±Inf — both pass straight through — or the
			// scaling overflowed, which means d is larger than the value has
			// significant decimals and the value is already its own rounding.
			dst[i] = v
			continue
		}
		dst[i] = math.Round(s) / scale
	}
	// A Float32 is rounded as a Float64 and then once more to a float32: Round(0.1,
	// 1) of a Float32 is the float32 nearest 0.1, not a null.
	return floatResult(name, out, dst, c.Validity())
}

// intArg reads the i'th literal argument as an int.
//
// CallArgs has already established that every argument is a literal; this is the
// narrowing to a Go int, and it reports which function and which position rather
// than "expected int" so a mistake is diagnosable from the message alone.
func intArg(fn expr.CallFn, args []any, i int) (int, error) {
	if i >= len(args) {
		return 0, uerr.Internalf("kernel: %s needs argument %d, got %d", fn, i, len(args))
	}
	switch v := args[i].(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case int32:
		return int(v), nil
	default:
		return 0, uerr.New(uerr.KindType, "", "%s argument %d must be an integer, got %T",
			fn, i, args[i])
	}
}

// bitCount counts the bits of each integer in its type's own width, as Polars'
// bitwise_count_ones and its siblings do: the leading zeros of an Int8 1 are 7, and
// a negative number is its two's complement.
func bitCount(fn expr.CallFn, name string, c *data.Column) (*data.Column, error) {
	c, err := readable(c) // a column of nulls may have no payload
	if err != nil {
		return nil, err
	}
	u, w, err := integerBits(c)
	if err != nil {
		return nil, err
	}
	valid := c.Validity()
	out := make([]uint32, len(u))
	for i, x := range u {
		if valid.Get(i) {
			out[i] = uint32(countBits(fn, x, w))
		}
	}
	return data.NewFixed(name, dtype.Uint32, out, valid), nil
}

// countBits answers fn for x, the bits of a w-bit integer in the low w bits of a
// uint64, the rest zero.
func countBits(fn expr.CallFn, x uint64, w int) int {
	ones := ^x
	if w < 64 {
		ones &= 1<<w - 1
	}
	switch fn {
	case expr.FnMathCountOnes:
		return bits.OnesCount64(x)
	case expr.FnMathCountZeros:
		return w - bits.OnesCount64(x)
	case expr.FnMathLeadingZeros:
		return bits.LeadingZeros64(x) - (64 - w)
	case expr.FnMathLeadingOnes:
		return bits.LeadingZeros64(ones) - (64 - w)
	case expr.FnMathTrailingZeros:
		return min(bits.TrailingZeros64(x), w)
	default: // expr.FnMathTrailingOnes
		return min(bits.TrailingZeros64(ones), w)
	}
}

// integerBits reads an integer column of 8 to 64 bits as the low bits of uint64s,
// and its width.
func integerBits(c *data.Column) ([]uint64, int, error) {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return widenBits[int8](c, 8)
	case dtype.TypeInt16:
		return widenBits[int16](c, 16)
	case dtype.TypeInt32:
		return widenBits[int32](c, 32)
	case dtype.TypeInt64:
		return widenBits[int64](c, 64)
	case dtype.TypeUint8:
		return widenBits[uint8](c, 8)
	case dtype.TypeUint16:
		return widenBits[uint16](c, 16)
	case dtype.TypeUint32:
		return widenBits[uint32](c, 32)
	case dtype.TypeUint64:
		return widenBits[uint64](c, 64)
	}
	return nil, 0, uerr.Internalf("kernel: bit counts of %s", c.DType())
}

func widenBits[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64](
	c *data.Column, w int) ([]uint64, int, error) {

	v, err := data.Values[T](c)
	if err != nil {
		return nil, 0, err
	}
	mask := ^uint64(0)
	if w < 64 {
		mask = 1<<w - 1
	}
	out := make([]uint64, len(v))
	for i, x := range v {
		out[i] = uint64(x) & mask
	}
	return out, w, nil
}
