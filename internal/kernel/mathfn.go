package kernel

import (
	"math"
	"math/bits"
	"strconv"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
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
	case expr.FnMathRound, expr.FnMathTruncate:
		d, err := intArg(fn, args, 0)
		if err != nil {
			return nil, err
		}
		op := math.Round
		switch {
		case fn == expr.FnMathTruncate:
			op = math.Trunc
		case len(args) > 1:
			switch m, _ := args[1].(string); m {
			case "half_to_even":
				op = math.RoundToEven
			case "half_away_from_zero":
			default:
				return nil, uerr.Internalf("kernel: round has no mode %q", m)
			}
		}
		return round(name, out, c, d, op)
	case expr.FnMathRoundSigFigs:
		d, err := intArg(fn, args, 0)
		if err != nil {
			return nil, err
		}
		return roundSigFigs(name, out, c, d)
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

// round rounds to d decimal places by op: math.Round, HALF AWAY FROM ZERO, ursus's
// default; math.RoundToEven, half to even, Polars' default; or math.Trunc, toward
// zero, which is Truncate. It is Polars' algorithm (step 283), so the answers are
// Polars' to the bit.
//
// # The default tie-break is a decision, not an accident
//
// ursus's default is half-away-from-zero, which is what a spreadsheet user expects —
// Round(0.5) is 1, and Round(2.5) is 3 rather than 2. Polars offers it as
// `mode="half_away_from_zero"`; its DEFAULT is half-to-even, which RoundHalfToEven
// selects here.
//
// # It rounds TWICE, and the second rounding is the one that surprises people
//
// Round(x, d) is op(x * 10^d) / 10^d, so the scaling multiply rounds before op ever
// sees the value. Round(2.675, 2) is 2.68 — not because 2.675 is exact (it is really
// 2.67499999999999982…) but because 2.675 * 100 rounds to exactly 267.5, which then
// rounds away from zero to 268, and to even to 268 too.
//
// So the answer is not "the decimal you wrote, rounded"; it is "the float you
// actually have, scaled and rounded". Every library that rounds binary floats
// this way lands in the same place, and the tests assert the specific values
// rather than papering over them.
//
// # The scale, as Polars takes it
//
//   - 10^d is powi's, by repeated squaring, exact to 10^22;
//   - d of 300 to 325, where 10^d is past a float64, scales by 2^d and 5^d apart;
//   - d of 326 or more leaves every value as it is, as does 47 or more for a
//     Float32, which is rounded in float64 and then once more to a float32;
//   - a value whose answer is not finite — the scaling overflowed, so d is past the
//     value's own decimals — is its own rounding.
func round(name string, out dtype.DataType, c *data.Column, d int,
	op func(float64) float64) (*data.Column, error) {
	// Rounding an integer to any number of decimals is the identity, whatever the
	// width — including Int128, and including the unsigned types.
	if !out.IsFloat() {
		return c.Rename(name).WithDType(out), nil
	}
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	f32 := out.ID() == dtype.TypeFloat32
	dst := make([]float64, len(src))
	scaled := func(v, r float64) float64 {
		if f32 {
			r = float64(float32(r))
		}
		if math.IsInf(r, 0) || math.IsNaN(r) {
			return v
		}
		return r
	}
	switch {
	case d == 0:
		for i, v := range src {
			dst[i] = op(v)
		}
	case d >= 326 || f32 && d >= 47:
		copy(dst, src)
	case d >= 300:
		mul2 := math.Ldexp(1, d)
		inv2 := 1 / mul2 // exact: a power of two
		mul5 := powi(5, d)
		for i, v := range src {
			dst[i] = scaled(v, op(v*mul2*mul5)/mul5*inv2)
		}
	default:
		m := powi(10, d)
		for i, v := range src {
			dst[i] = scaled(v, op(v*m)/m)
		}
	}
	return floatResult(name, out, dst, c.Validity())
}

// powi is x^n by repeated squaring, then its reciprocal for a negative n: Rust's
// powi, compiler-builtins' __powidf2, whose roundings Polars' scales have. Each
// product rounds, so 10^n is exact only to 10^22.
func powi(x float64, n int) float64 {
	recip := n < 0
	if recip {
		n = -n
	}
	r := 1.0
	for {
		if n&1 != 0 {
			r *= x
		}
		n >>= 1
		if n == 0 {
			break
		}
		x *= x
	}
	if recip {
		return 1 / r
	}
	return r
}

// roundSigFigs rounds to digits significant figures, half away from zero, in the
// operand's own type: Polars' round_sig_figs, by its algorithm (step 283). Each value
// is taken to a float64, scaled by 2^e and 5^e apart, rounded and scaled back; a
// value whose answer is not finite is kept. An integer comes back as Rust's `as`
// brings it, saturating, so an Int8 127 to two figures is 127 and NaN is 0.
func roundSigFigs(name string, out dtype.DataType, c *data.Column, digits int) (*data.Column, error) {
	c, err := readable(c)
	if err != nil {
		return nil, err
	}
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	v := make([]float64, len(src))
	for i, x := range src {
		v[i] = sigFigs(x, digits)
	}
	valid := c.Validity()
	switch out.Physical().ID() {
	case dtype.TypeFloat32, dtype.TypeFloat64:
		return floatResult(name, out, v, valid)
	case dtype.TypeInt8:
		return saturated[int8](name, out, v, valid, math.MinInt8, math.MaxInt8), nil
	case dtype.TypeInt16:
		return saturated[int16](name, out, v, valid, math.MinInt16, math.MaxInt16), nil
	case dtype.TypeInt32:
		return saturated[int32](name, out, v, valid, math.MinInt32, math.MaxInt32), nil
	case dtype.TypeInt64:
		return saturated[int64](name, out, v, valid, math.MinInt64, math.MaxInt64), nil
	case dtype.TypeUint8:
		return saturated[uint8](name, out, v, valid, 0, math.MaxUint8), nil
	case dtype.TypeUint16:
		return saturated[uint16](name, out, v, valid, 0, math.MaxUint16), nil
	case dtype.TypeUint32:
		return saturated[uint32](name, out, v, valid, 0, math.MaxUint32), nil
	case dtype.TypeUint64:
		return saturated[uint64](name, out, v, valid, 0, math.MaxUint64), nil
	case dtype.TypeInt128:
		buf, dst := newValuesBuffer[i128.Int128](len(v))
		for i, x := range v {
			switch {
			case math.IsNaN(x):
				dst[i] = i128.Zero
			case x >= 0x1p127:
				dst[i] = i128.Max
			case x <= -0x1p127:
				dst[i] = i128.Min
			default:
				dst[i], _ = i128.FromFloat64(x)
			}
		}
		return data.NewFixedBuffer(name, out, buf, len(v), valid), nil
	}
	return nil, uerr.Internalf("kernel: round_sig_figs has no kernel for %s", out)
}

// sigFigs is one value to digits significant figures, as Polars computes it.
func sigFigs(v float64, digits int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	e := digits - 1 - floorLog10(math.Abs(v))
	pow5 := powi(5, e)
	r := math.Ldexp(math.Round(math.Ldexp(v, e)*pow5)/pow5, -e)
	if math.IsInf(r, 0) || math.IsNaN(r) {
		return v
	}
	return r
}

// floorLog10 is floor(log10(a)) for a finite a > 0, an exact power of ten its own
// exponent, as a correctly rounded log10 gives it. It is Log2's, scaled: Go's Log10
// is Log's, which on amd64 misreads a subnormal, so Log10(1.5e-315) is -307.95. Log2
// takes the exponent apart by Frexp first. Scaled, it falls short of 45 powers of
// ten, 10^23 and 10^46 among them, where a correctly rounded log10 is the exponent
// itself, so the floor is checked against the float64 nearest the next power of ten.
// At many figures, one floor too low lands an ulp from Polars' answer.
func floorLog10(a float64) int {
	n := int(math.Floor(math.Log2(a) * (math.Ln2 / math.Ln10)))
	if k := n + 1; k >= -323 && k <= 308 && a >= powersOfTen()[k+323] {
		n = k
	}
	return n
}

// powersOfTen is the float64 nearest each 10^k, k from -323 to 308, as the
// parser rounds the text "1ek".
var powersOfTen = sync.OnceValue(func() []float64 {
	t := make([]float64, 308+323+1)
	for k := range t {
		t[k], _ = strconv.ParseFloat("1e"+strconv.Itoa(k-323), 64)
	}
	return t
})

// saturated is each value in an integer type as Rust's `as` takes it: toward zero,
// the type's ends past them, and NaN 0.
func saturated[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64](name string,
	out dtype.DataType, v []float64, valid bitmap.View, lo, hi T) *data.Column {
	buf, dst := newValuesBuffer[T](len(v))
	flo, fhi := float64(lo), float64(hi)
	for i, x := range v {
		switch {
		case math.IsNaN(x):
			dst[i] = 0
		case x <= flo:
			dst[i] = lo
		case x >= fhi:
			dst[i] = hi
		default:
			dst[i] = T(x)
		}
	}
	return data.NewFixedBuffer(name, out, buf, len(v), valid)
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
