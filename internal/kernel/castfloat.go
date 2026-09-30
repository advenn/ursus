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

// castToFloat is every cast to Float32 or Float64 from an integer, a temporal tick
// count, an Int128 or the other float. A Decimal's is castDecimal's.
//
// # A cast to a float rounds, once
//
// Rounding to the nearest value of the target's width is what a float is, so it is
// never a loss to refuse: 0.1 cast to Float32 is the float32 nearest 0.1, an Int64
// above 2^24 is the float32 nearest it. The one value a float cannot take is a
// finite one past its range, whose nearest float is an infinity — a strict cast
// refuses it, a lossy one makes it null — and only a Float64 can be one: every
// integer and Int128 is below 2^128 - 2^103, where a Float32 starts to overflow.
//
// These all went through a float64 and then narrow's round-trip test, which is the
// right test for a cast FROM a float and the wrong one for a cast TO one. So 0.1 was
// refused, every Int64 above 2^24 was refused, and what did convert rounded twice —
// to a float64 and again to a float32, which is a different number whenever the
// first rounding lands on a float32 midpoint: 2^53+2^29+1 came back as 2^53, where
// the nearest float32 is 2^53+2^30. Each value now converts from its own type,
// which Go does in one rounding.
func castToFloat(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	n := c.Len()
	valid := c.Validity()
	if c.DType().Physical().ID() == dtype.TypeFloat64 && to.ID() == dtype.TypeFloat32 {
		src, err := data.Values[float64](c)
		if err != nil {
			return nil, err
		}
		return f64ToF32(name, to, src, valid, strict)
	}
	switch to.ID() {
	case dtype.TypeFloat32:
		buf, dst := newValuesBuffer[float32](n)
		if err := convertInto(c, dst); err != nil {
			return nil, err
		}
		return data.NewFixedBuffer(name, to, buf, n, valid), nil
	case dtype.TypeFloat64:
		buf, dst := newValuesBuffer[float64](n)
		if err := convertInto(c, dst); err != nil {
			return nil, err
		}
		return data.NewFixedBuffer(name, to, buf, n, valid), nil
	}
	return nil, uerr.Internalf("kernel: castToFloat to %s", to)
}

// convertInto converts every value of c into dst, each from its own type.
func convertInto[F float32 | float64](c *data.Column, dst []F) error {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return convertEach[int8](c, dst)
	case dtype.TypeInt16:
		return convertEach[int16](c, dst)
	case dtype.TypeInt32:
		return convertEach[int32](c, dst)
	case dtype.TypeInt64:
		return convertEach[int64](c, dst)
	case dtype.TypeUint8:
		return convertEach[uint8](c, dst)
	case dtype.TypeUint16:
		return convertEach[uint16](c, dst)
	case dtype.TypeUint32:
		return convertEach[uint32](c, dst)
	case dtype.TypeUint64:
		return convertEach[uint64](c, dst)
	case dtype.TypeFloat32:
		return convertEach[float32](c, dst)
	case dtype.TypeFloat64:
		return convertEach[float64](c, dst)
	case dtype.TypeInt128:
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return err
		}
		switch d := any(dst).(type) {
		case []float32:
			for i, x := range v {
				d[i] = x.Float32()
			}
		case []float64:
			for i, x := range v {
				d[i] = x.Float64()
			}
		}
		return nil
	}
	return uerr.Internalf("kernel: cannot convert %s to a float", c.DType())
}

func convertEach[T data.Primitive, F float32 | float64](c *data.Column, dst []F) error {
	v, err := data.Values[T](c)
	if err != nil {
		return err
	}
	for i, x := range v {
		dst[i] = F(x)
	}
	return nil
}

// f64ToF32 rounds each value to the nearest float32. A finite value that rounds to
// an infinity — at or past 2^128 - 2^103, halfway from MaxFloat32 to 2^128 — is the
// one that does not fit: refused under strict, null otherwise. A value between
// MaxFloat32 and that threshold rounds down to MaxFloat32, and is kept.
func f64ToF32(name string, to dtype.DataType, src []float64, valid bitmap.View, strict bool) (*data.Column, error) {
	n := len(src)
	buf, dst := newValuesBuffer[float32](n)
	var over []int
	for i, v := range src {
		f := float32(v)
		if math.IsInf(float64(f), 0) && !math.IsInf(v, 0) && valid.Get(i) {
			if strict {
				return nil, floatOverflow(strconv.FormatFloat(v, 'g', -1, 64), i, to)
			}
			over = append(over, i)
			continue
		}
		dst[i] = f
	}
	if len(over) > 0 {
		keep := bitmap.NewBuilder(n)
		for i, j := 0, 0; i < n; i++ {
			if j < len(over) && over[j] == i {
				keep.Append(false)
				j++
				continue
			}
			keep.Append(valid.Get(i))
		}
		valid = keep.Finish()
	}
	return data.NewFixedBuffer(name, to, buf, n, valid), nil
}

// floatResult stores the float64 result of a computation at out's width: a Float64
// as it is, a Float32 rounded once to the nearest float32.
//
// # A computation is not a cast
//
// A Float32 sqrt is computed in float64 and stored in float32, and that store ROUNDS
// — which is what a float32 result is, and what Polars and PyArrow return. It went
// through narrow's round-trip test, which asks whether a CAST lost anything: every
// inexact result failed it and became a null, so sqrt(2) and ln(2) were null, and
// ErrInternal in a non-nullable column. Nothing here nulls or refuses. An overflow
// is the +Inf that float32 arithmetic gives, as unaryFloat's and every binary op's
// already are.
//
// For sqrt, rounding through float64 is exactly the correctly rounded float32: 53
// bits is more than twice 24 plus two. For the others it is within one ulp of it.
func floatResult(name string, out dtype.DataType, v []float64, valid bitmap.View) (*data.Column, error) {
	n := len(v)
	switch out.ID() {
	case dtype.TypeFloat64:
		buf, dst := newValuesBuffer[float64](n)
		copy(dst, v)
		return data.NewFixedBuffer(name, out, buf, n, valid), nil
	case dtype.TypeFloat32:
		buf, dst := newValuesBuffer[float32](n)
		for i, x := range v {
			dst[i] = float32(x)
		}
		return data.NewFixedBuffer(name, out, buf, n, valid), nil
	}
	return nil, uerr.Internalf("kernel: a float computation cannot produce %s", out)
}

// floatOverflow refuses v at row under a strict cast to a float: the one value a
// float cannot take, a finite one past its range whose nearest float is an
// infinity. Everything else rounds.
func floatOverflow(v string, row int, to dtype.DataType) *uerr.Error {
	return unrepresentable(v, row, to).
		Hint("a cast to %s rounds to the nearest value, and refuses only a finite one "+
			"past its range, which would round to an infinity", to)
}
