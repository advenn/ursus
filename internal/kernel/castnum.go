package kernel

import (
	"math"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// boolToNumeric renders false as 0 and true as 1, which is what every other
// system does and the only choice that makes sum(flag) a count.
//
// It materialises Int64 and RE-ENTERS Cast rather than converting directly. Two
// reasons, and the second is the one a differential test found: bool -> T ought to
// be bool -> Int64 -> T by definition, and the float path it once took could not
// produce a 128-bit target at all — so `Col("flag").Cast(Int128)` failed with
// "cannot narrow to Int128" although CanCast promises it. Nothing is lost by going
// through Int64: 0 and 1 are exact in it and in every type reachable from it.
func boolToNumeric(name string, to dtype.DataType, c *data.Column) (*data.Column, error) {
	n := c.Len()
	bits := c.Bools()
	src := make([]int64, n)
	for i := range n {
		if bits.Get(i) {
			src[i] = 1
		}
	}
	ints := data.NewFixed(name, dtype.Int64, src, c.Validity())
	if to.ID() == dtype.TypeInt64 {
		return ints, nil
	}
	// strict=false: 0 and 1 are representable in every numeric type, so nothing can
	// be lossy and the flag cannot fire.
	return Cast(name, to, false, ints)
}

// numericToBool is `!= 0`, matching C, SQL and Polars. NaN is true — it is not
// zero — which is the one answer people find surprising and the one that follows
// from the rule.
func numericToBool(name string, c *data.Column) (*data.Column, error) {
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	bits := bitmap.NewBuilder(len(src))
	for _, v := range src {
		bits.Append(v != 0)
	}
	return data.NewBool(name, bits.Finish(), c.Validity()), nil
}

func toFloat64(c *data.Column) ([]float64, error) {
	// A Decimal's physical type is Int128, so without this arm the switch below reads
	// its UNSCALED integer — 12.34 as 1234. Every float consumer comes through here:
	// the Float64 cast, mean, var, std, median, quantile, product, cum_prod. So this
	// is the one place the scale can be applied, and the one place it can be forgotten.
	if c.DType().ID() == dtype.TypeDecimal {
		return decimalFloats(c)
	}
	if c.DType().Physical().ID() == dtype.TypeFloat64 {
		return data.Values[float64](c) // the column's own: a caller must not write it
	}
	n := c.Len()
	out := make([]float64, n)
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return conv[int8](c, out)
	case dtype.TypeInt16:
		return conv[int16](c, out)
	case dtype.TypeInt32:
		return conv[int32](c, out)
	case dtype.TypeInt64:
		return conv[int64](c, out)
	case dtype.TypeUint8:
		return conv[uint8](c, out)
	case dtype.TypeUint16:
		return conv[uint16](c, out)
	case dtype.TypeUint32:
		return conv[uint32](c, out)
	case dtype.TypeUint64:
		return conv[uint64](c, out)
	case dtype.TypeFloat32:
		return conv[float32](c, out)
	case dtype.TypeInt128:
		// The nearest float64, in one rounding since step 73 — above 2^53 it cannot
		// be exact, and this path feeds sqrt, log and exp, none of which has an exact
		// answer anyway. A Cast to a float does not come here: castToFloat converts
		// each value from its own type. What this arm enables is Col("x").Sum().Sqrt(),
		// an ordinary query, since every integer Sum outputs Int128.
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		for i := range v {
			out[i] = v[i].Float64()
		}
		return out, nil
	default:
		return nil, uerr.Internalf("kernel: cannot widen %s", c.DType())
	}
}

func conv[T data.Primitive](c *data.Column, out []float64) ([]float64, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = float64(v[i])
	}
	return out, nil
}

// floatsToInt converts floats to an integer type: exact, or refused under strict and
// null otherwise. A float converts exactly when it is an integer the target holds,
// so a fraction, NaN, an infinity and a value out of range are all refused.
func floatsToInt(name string, to dtype.DataType, src []float64,
	valid bitmap.View, strict bool) (*data.Column, error) {

	switch to.Physical().ID() {
	case dtype.TypeInt8:
		return floatToInt[int8](name, to, src, valid, strict, -0x1p7, 0x1p7)
	case dtype.TypeInt16:
		return floatToInt[int16](name, to, src, valid, strict, -0x1p15, 0x1p15)
	case dtype.TypeInt32:
		return floatToInt[int32](name, to, src, valid, strict, -0x1p31, 0x1p31)
	case dtype.TypeInt64:
		return floatToInt[int64](name, to, src, valid, strict, -0x1p63, 0x1p63)
	case dtype.TypeUint8:
		return floatToInt[uint8](name, to, src, valid, strict, 0, 0x1p8)
	case dtype.TypeUint16:
		return floatToInt[uint16](name, to, src, valid, strict, 0, 0x1p16)
	case dtype.TypeUint32:
		return floatToInt[uint32](name, to, src, valid, strict, 0, 0x1p32)
	case dtype.TypeUint64:
		return floatToInt[uint64](name, to, src, valid, strict, 0, 0x1p64)
	default:
		return nil, uerr.Internalf("kernel: cannot convert a float to %s", to)
	}
}

// floatToInt is floatsToInt for one width, whose range is [lo, hiExcl): powers of
// two, and so exact as float64s.
//
// # The range is checked before the conversion, not after
//
// Go leaves a float-to-integer conversion whose value is out of range to the
// platform. amd64 gives the "indefinite integer", 0x8000…, which never survives the
// round trip below, so checking afterwards happened to work there. arm64 SATURATES:
// int64(2^63) is MaxInt64, whose float64 is 2^63 again, so the round trip passed and
// 2^63 cast to Int64 came back as MaxInt64 — and 2^64 to Uint64 as MaxUint64. NaN
// fails both comparisons.
func floatToInt[T ~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32 | ~uint64](
	name string, to dtype.DataType, src []float64, valid bitmap.View, strict bool,
	lo, hiExcl float64) (*data.Column, error) {

	n := len(src)
	buf, dst := newValuesBuffer[T](n)
	ok := bitmap.NewBuilder(n)
	lossy := false
	for i, v := range src {
		var t T
		if v >= lo && v < hiExcl {
			t = T(v)
		}
		if !(v >= lo && v < hiExcl) || float64(t) != v {
			lossy = true
			if strict && valid.Get(i) {
				return nil, unrepresentable(strconv.FormatFloat(v, 'g', -1, 64), i, to)
			}
			ok.Append(false)
			continue
		}
		dst[i] = t
		ok.Append(true)
	}
	if lossy {
		valid = bitmap.And(valid, ok.Finish())
	}
	return data.NewFixedBuffer(name, to, buf, n, valid), nil
}

// castInt converts between integer types — every width, signed or not, and Int128 —
// without a float anywhere.
//
// Every integer fits an Int128 exactly, so widening to one loses nothing, and
// narrowI128 then range-checks against the TARGET's own bounds. Before this, only
// Int128 went through here and the other pairs took the float64 path below, on the
// argument that any value large enough to round would be caught by narrow's range
// check. It was not: narrow checks the value after it has been rounded, against
// itself. Int64 and Uint64 above 2^53 came back rounded, MaxInt64 cast to Uint64
// came back as 2^63 — one MORE than the input, which fits a Uint64 and so passed —
// and MaxInt64 cast from a Uint64 was refused. intcast_test.go sweeps every pair.
func castInt(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	src, err := widenToInt128(c)
	if err != nil {
		return nil, err
	}
	return narrowI128(name, to, c.DType(), src, c.Validity(), strict)
}

// narrowI128 converts Int128 values to the integer type to, refusing (strict) or
// nulling (lossy) any value outside its range. from is the source type, for the
// refusal's hint.
func narrowI128(name string, to, from dtype.DataType, src []i128.Int128,
	valid bitmap.View, strict bool) (*data.Column, error) {

	switch to.Physical().ID() {
	case dtype.TypeInt8:
		return narrowSigned[int8](name, to, from, src, valid, strict, math.MinInt8, math.MaxInt8)
	case dtype.TypeInt16:
		return narrowSigned[int16](name, to, from, src, valid, strict, math.MinInt16, math.MaxInt16)
	case dtype.TypeInt32:
		return narrowSigned[int32](name, to, from, src, valid, strict, math.MinInt32, math.MaxInt32)
	case dtype.TypeInt64:
		return narrowSigned[int64](name, to, from, src, valid, strict, math.MinInt64, math.MaxInt64)
	case dtype.TypeUint8:
		return narrowUnsigned[uint8](name, to, from, src, valid, strict, math.MaxUint8)
	case dtype.TypeUint16:
		return narrowUnsigned[uint16](name, to, from, src, valid, strict, math.MaxUint16)
	case dtype.TypeUint32:
		return narrowUnsigned[uint32](name, to, from, src, valid, strict, math.MaxUint32)
	case dtype.TypeUint64:
		return narrowUnsigned[uint64](name, to, from, src, valid, strict, math.MaxUint64)
	case dtype.TypeInt128:
		buf, dst := newValuesBuffer[i128.Int128](len(src))
		copy(dst, src)
		return data.NewFixedBuffer(name, to, buf, len(src), valid), nil
	default:
		return nil, uerr.Internalf("kernel: cannot narrow Int128 to %s", to)
	}
}

func narrowSigned[T int8 | int16 | int32 | int64](name string, to, from dtype.DataType,
	src []i128.Int128, valid bitmap.View, strict bool, lo, hi int64) (*data.Column, error) {

	return narrowInto(name, to, from, src, valid, strict, func(v i128.Int128) (T, bool) {
		x, ok := v.Int64()
		return T(x), ok && x >= lo && x <= hi
	})
}

func narrowUnsigned[T uint8 | uint16 | uint32 | uint64](name string, to, from dtype.DataType,
	src []i128.Int128, valid bitmap.View, strict bool, hi uint64) (*data.Column, error) {

	return narrowInto(name, to, from, src, valid, strict, func(v i128.Int128) (T, bool) {
		x, ok := v.Uint64()
		return T(x), ok && x <= hi
	})
}

func narrowInto[T data.Primitive](name string, to, from dtype.DataType, src []i128.Int128,
	valid bitmap.View, strict bool, conv func(i128.Int128) (T, bool)) (*data.Column, error) {

	n := len(src)
	buf, dst := newValuesBuffer[T](n)
	ok := bitmap.NewBuilder(n)
	lossy := false
	for i, v := range src {
		t, fits := conv(v)
		if !fits {
			lossy = true
			if strict && valid.Get(i) {
				e := uerr.New(uerr.KindValue, "cast",
					"value %s at row %d is not representable as %s", v, i, to).
					Hint("use a non-strict cast to turn unrepresentable values into nulls")
				if from.ID() == dtype.TypeInt128 {
					e = e.Hint("an integer sum is an Int128 so that it cannot overflow; " +
						"keep it as one, or cast to Float64 for an approximate value")
				}
				return nil, e
			}
			ok.Append(false)
			continue
		}
		dst[i] = t
		ok.Append(true)
	}
	if lossy {
		valid = bitmap.And(valid, ok.Finish())
	}
	return data.NewFixedBuffer(name, to, buf, n, valid), nil
}

// floatToInt128 converts a float to Int128: exact or refused, like every other
// float -> integer cast. i128.FromFloat64 truncates, and this used to take its
// answer, so a strict Cast(Int128) turned 3.7 into 3 where Cast(Int64) refuses it —
// two rules for one question, one of which broke Cast's promise to fail on an
// unrepresentable value. Int128 to a float is castToFloat's.
func floatToInt128(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	n := c.Len()
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	buf, dst := newValuesBuffer[i128.Int128](n)
	ok := bitmap.NewBuilder(n)
	valid := c.Validity()
	for i, f := range src {
		v, fits := i128.FromFloat64(f)
		if !fits || f != math.Trunc(f) {
			if strict && valid.Get(i) {
				return nil, uerr.New(uerr.KindValue, "cast",
					"value %v at row %d is not representable as Int128", f, i).
					Hint("use a non-strict cast to turn unrepresentable values into nulls").
					Hint("to drop the fraction on purpose, apply .Floor() or .Ceil() first")
			}
			ok.Append(false)
			continue
		}
		dst[i] = v
		ok.Append(true)
	}
	return data.NewFixedBuffer(name, to, buf, n, bitmap.And(valid, ok.Finish())), nil
}

func widenToInt64(c *data.Column) ([]int64, error) {
	n := c.Len()
	out := make([]int64, n)
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return convInt[int8](c, out)
	case dtype.TypeInt16:
		return convInt[int16](c, out)
	case dtype.TypeInt32:
		return convInt[int32](c, out)
	case dtype.TypeInt64:
		return data.Values[int64](c)
	default:
		return nil, uerr.Internalf("kernel: %s is not a signed integer", c.DType())
	}
}

func convInt[T data.Primitive](c *data.Column, out []int64) ([]int64, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = int64(v[i])
	}
	return out, nil
}

func widenToUint64(c *data.Column) ([]uint64, error) {
	n := c.Len()
	out := make([]uint64, n)
	switch c.DType().Physical().ID() {
	case dtype.TypeUint8:
		return convUint[uint8](c, out)
	case dtype.TypeUint16:
		return convUint[uint16](c, out)
	case dtype.TypeUint32:
		return convUint[uint32](c, out)
	case dtype.TypeUint64:
		return data.Values[uint64](c)
	default:
		return nil, uerr.Internalf("kernel: %s is not an unsigned integer", c.DType())
	}
}

func convUint[T data.Primitive](c *data.Column, out []uint64) ([]uint64, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = uint64(v[i])
	}
	return out, nil
}
