package kernel

// Decimal casts.
//
// A Decimal(p, s) is stored as its UNSCALED integer u, meaning u / 10^s, with
// |u| < 10^p. Every conversion here is EXACT OR REFUSED, the rule Cast(Float64 ->
// Int64) has always followed: a value that survives unchanged converts, and one
// that would lose a digit is refused by Cast and becomes null under CastLossy.
//
//	Decimal -> integer   the fraction must be zero, and the integer must fit
//	integer -> Decimal   |v| must be below 10^(p-s)
//	float   -> Decimal   the float must come back unchanged, so 0.1 -> 0.10 is
//	                     exact and 0.123 -> Decimal(10,2) is refused; this is
//	                     precisely "Round(v, s) == v"
//	Decimal -> Decimal   rescaling up always keeps the digits; down, only if the
//	                     dropped ones are zero; and the result must fit p
//	Decimal -> Float64   approximate by definition, as it is from Int64: the
//	                     nearest double, correctly rounded
//
// Rounding is something a caller writes, not something a cast does behind their
// back: Col("x").Round(2).Cast(Decimal(10, 2)) gives the digits Polars and DuckDB
// give, 2.675 -> 2.68, because Round's arithmetic is the candidate here.

import (
	"math"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// pow10 is 10^0 .. 10^38 as Int128. It is built by multiplication rather than
// written out, so no digit can be mistyped; castdec_test.go checks it against
// math/big.
var pow10 = func() (t [dtype.MaxDecimalPrecision + 1]i128.Int128) {
	t[0] = i128.One
	for k := 1; k < len(t); k++ {
		t[k] = mul10(t[k-1])
	}
	return t
}()

// mul10 returns v*10, as 8v + 2v, wrapping. Every caller has bounded v first.
func mul10(v i128.Int128) i128.Int128 {
	x2 := v.Add(v)
	x4 := x2.Add(x2)
	return x4.Add(x4).Add(x2)
}

// scaleUp returns v * 10^k. The caller has checked that the product fits.
func scaleUp(v i128.Int128, k int) i128.Int128 {
	for range k {
		v = mul10(v)
	}
	return v
}

// scaleDown returns v / 10^k, truncated, and whether nothing was truncated. 10^19
// is the largest power of ten a uint64 divisor holds, so a larger k divides twice;
// truncation composes, and so does exactness.
func scaleDown(v i128.Int128, k int) (i128.Int128, bool) {
	exact := true
	for k > 0 {
		step := min(k, 19)
		q, r, _ := v.DivUint64(pow10[step].Lo)
		v, exact = q, exact && r == 0
		k -= step
	}
	return v, exact
}

// withinDigits reports whether |v| < 10^digits.
func withinDigits(v i128.Int128, digits int) bool {
	b := pow10[digits]
	return v.Cmp(b) < 0 && v.Cmp(b.Neg()) > 0
}

// decimalToFloat is u / 10^scale as the NEAREST float64.
//
// The fast path is exact arithmetic followed by one correctly rounded division:
// below 2^53 the integer converts exactly, 10^0..10^22 are exact doubles, and IEEE
// division rounds once. Everything else goes through strconv.ParseFloat of the
// decimal's own digits, which is correctly rounded for any input. So the answer is
// always the double nearest the decimal — the same one ParseFloat(Cast(String))
// gives, which is what castdec_test.go asserts.
func decimalToFloat(u i128.Int128, scale uint8) float64 {
	if scale <= 22 {
		if x, ok := u.Int64(); ok && x >= -(1<<53) && x <= 1<<53 {
			return float64(x) / math.Pow10(int(scale))
		}
	}
	// Cannot fail: the digits are well formed and |u / 10^s| < 2^127 is finite.
	f, _ := strconv.ParseFloat(dtype.FormatDecimal(u.String(), scale), 64)
	return f
}

// decimalFloats reads a Decimal column as float64, with the scale applied.
func decimalFloats(c *data.Column) ([]float64, error) {
	src, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	s := c.DType().Scale()
	out := make([]float64, len(src))
	for i, v := range src {
		out[i] = decimalToFloat(v, s)
	}
	return out, nil
}

// decimalRange renders a Decimal type's bounds, for a refusal's hint.
func decimalRange(dt dtype.DataType) string {
	hi := dtype.FormatDecimal(pow10[dt.Precision()].Sub(i128.One).String(), dt.Scale())
	return dt.String() + " holds values from -" + hi + " to " + hi
}

// unrepresentable is a strict cast's refusal of one value.
func unrepresentable(v string, row int, to dtype.DataType) *uerr.Error {
	return uerr.New(uerr.KindValue, "cast",
		"value %s at row %d is not representable as %s", v, row, to).
		Hint("use a non-strict cast to turn unrepresentable values into nulls")
}

func castDecimal(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	from := c.DType()
	switch {
	case to.ID() == dtype.TypeDecimal && !dtype.ValidDecimal(to.Precision(), to.Scale()):
		return nil, uerr.New(uerr.KindType, "cast",
			"cannot cast %s to %s", from, to).
			Hint("%s is not a type: a Decimal has 1 to %d digits of precision, "+
				"and a scale no larger than its precision", to, dtype.MaxDecimalPrecision)
	case from.ID() == dtype.TypeDecimal && to.ID() == dtype.TypeDecimal:
		return rescaleDecimal(name, to, strict, c)
	case from.ID() == dtype.TypeDecimal && to.IsInteger():
		return decimalToInt(name, to, strict, c)
	case from.ID() == dtype.TypeDecimal && to.IsFloat():
		f, err := decimalFloats(c)
		if err != nil {
			return nil, err
		}
		// Float64 takes the nearest double; Float32 is range- and round-trip-checked
		// by narrow, as a Float64 -> Float32 cast is.
		return fromFloat64(name, to, f, c.Validity(), strict)
	case from.IsInteger() && to.ID() == dtype.TypeDecimal:
		return intToDecimal(name, to, strict, c)
	case from.IsFloat() && to.ID() == dtype.TypeDecimal:
		return floatToDecimal(name, to, strict, c)
	}
	return nil, uerr.New(uerr.KindUnsupported, "cast",
		"cannot cast %s to %s", from, to).
		Hint("a Decimal converts to and from the integer and float types, to " +
			"another Decimal, and to String")
}

// toDecimal runs conv over every row, building a column of type to. A value conv
// cannot represent is refused under strict — refuse says how — and is null
// otherwise.
func toDecimal(name string, to dtype.DataType, strict bool, valid bitmap.View, n int,
	conv func(i int) (i128.Int128, bool), refuse func(i int) error) (*data.Column, error) {

	buf, dst := newValuesBuffer[i128.Int128](n)
	ok := bitmap.NewBuilder(n)
	lossy := false
	for i := range n {
		v, fits := conv(i)
		if !fits {
			lossy = true
			if strict && valid.Get(i) {
				return nil, refuse(i)
			}
			ok.Append(false)
			continue
		}
		dst[i] = v
		ok.Append(true)
	}
	if lossy {
		valid = bitmap.And(valid, ok.Finish())
	}
	return data.NewFixedBuffer(name, to, buf, n, valid), nil
}

func intToDecimal(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	src, err := widenToInt128(c)
	if err != nil {
		return nil, err
	}
	// |v| < 10^(p-s) is the whole check, and it needs no multiplication: once it
	// holds, v * 10^s < 10^p fits by construction.
	whole := int(to.Precision()) - int(to.Scale())
	s := int(to.Scale())
	return toDecimal(name, to, strict, c.Validity(), len(src),
		func(i int) (i128.Int128, bool) {
			if !withinDigits(src[i], whole) {
				return i128.Zero, false
			}
			return scaleUp(src[i], s), true
		},
		func(i int) error {
			return unrepresentable(src[i].String(), i, to).Hint("%s", decimalRange(to))
		})
}

func floatToDecimal(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	p, s := int(to.Precision()), to.Scale()
	scale := math.Pow10(int(s))
	f32 := c.DType().ID() == dtype.TypeFloat32
	return toDecimal(name, to, strict, c.Validity(), len(src),
		func(i int) (i128.Int128, bool) {
			f := src[i]
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return i128.Zero, false
			}
			// The candidate is Round's own arithmetic, so that "Round(v, s) == v" is
			// exactly the condition for success. It is accepted only if it converts
			// back to the value it came from — compared in the SOURCE's precision, or
			// a Float32 0.1, which is 0.100000001 as a float64, could never be 0.10.
			u, fits := i128.FromFloat64(math.Round(f * scale))
			if !fits || !withinDigits(u, p) {
				return i128.Zero, false
			}
			back := decimalToFloat(u, s)
			if f32 {
				return u, float32(back) == float32(f)
			}
			return u, back == f
		},
		func(i int) error {
			e := unrepresentable(strconv.FormatFloat(src[i], 'g', -1, 64), i, to)
			if math.IsNaN(src[i]) || math.IsInf(src[i], 0) {
				return e
			}
			return e.
				Hint("a cast keeps every digit or refuses; to round to %d places on "+
					"purpose, apply .Round(%d) first", s, s).
				Hint("%s", decimalRange(to))
		})
}

func rescaleDecimal(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	src, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	from := c.DType()
	s1, s2, p2 := int(from.Scale()), int(to.Scale()), int(to.Precision())
	conv := func(i int) (i128.Int128, bool) {
		if s2 >= s1 {
			// Adding digits after the point keeps every one; it only needs room.
			// p2 - (s2 - s1) >= 0, because s2 <= p2.
			k := s2 - s1
			if !withinDigits(src[i], p2-k) {
				return i128.Zero, false
			}
			return scaleUp(src[i], k), true
		}
		q, exact := scaleDown(src[i], s1-s2)
		return q, exact && withinDigits(q, p2)
	}
	return toDecimal(name, to, strict, c.Validity(), len(src), conv,
		func(i int) error {
			return unrepresentable(dtype.FormatDecimal(src[i].String(), from.Scale()), i, to).
				Hint("a cast keeps every digit or refuses; it does not round").
				Hint("%s", decimalRange(to))
		})
}

func decimalToInt(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	src, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	from := c.DType()
	valid := c.Validity()
	n := len(src)

	// Divide out the scale first, refusing a fraction; narrowI128 then checks the
	// integer against the target's range, exactly as it does for any integer cast.
	whole := make([]i128.Int128, n)
	ok := bitmap.NewBuilder(n)
	lossy := false
	for i, v := range src {
		q, exact := scaleDown(v, int(from.Scale()))
		if !exact {
			lossy = true
			if strict && valid.Get(i) {
				return nil, unrepresentable(dtype.FormatDecimal(v.String(), from.Scale()), i, to).
					Hint("an integer has no fraction; to drop it on purpose, go through " +
						"a float: .Cast(ursus.Float64).Round(0).Cast(...)")
			}
			ok.Append(false)
			continue
		}
		whole[i] = q
		ok.Append(true)
	}
	if lossy {
		valid = bitmap.And(valid, ok.Finish())
	}
	return narrowI128(name, to, from, whole, valid, strict)
}
