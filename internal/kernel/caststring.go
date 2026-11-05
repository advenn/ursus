package kernel

import (
	"math"
	"slices"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// formatToString renders a column as text, using the same formatter the frame
// renderer and the CSV writer use, so all three agree.
func formatToString(name string, c *data.Column) (*data.Column, error) {
	n := c.Len()
	out := make([]string, n)
	valid := c.Validity()
	dt := c.DType()

	if dt.IsTemporal() {
		ticks, err := readTicks(c)
		if err != nil {
			return nil, err
		}
		for i := range n {
			if valid.Get(i) {
				out[i] = dtype.FormatTemporal(dt, ticks[i])
			}
		}
		return data.NewString(name, out, valid), nil
	}
	if dt.IsBool() {
		bits := c.Bools()
		for i := range n {
			if valid.Get(i) {
				out[i] = strconv.FormatBool(bits.Get(i))
			}
		}
		return data.NewString(name, out, valid), nil
	}
	if dt.IsFloat() {
		f, err := toFloat64(c)
		if err != nil {
			return nil, err
		}
		bits := 64
		if dt == dtype.Float32 {
			bits = 32
		}
		var text []byte // one buffer for every value, a string's allocation each
		for i := range n {
			if valid.Get(i) {
				text = AppendFloatText(text[:0], f[i], bits)
				out[i] = string(text)
			}
		}
		return data.NewString(name, out, valid), nil
	}
	ticks, err := readAnyInt(c)
	if err != nil {
		return nil, err
	}
	for i := range n {
		if valid.Get(i) {
			out[i] = ticks[i]
		}
	}
	return data.NewString(name, out, valid), nil
}

// readAnyInt formats an integer column exactly.
//
// # Exactly, which it did not used to be
//
// It routed everything through toFloat64, so a Uint64 above 2^53 formatted as its
// rounded neighbour — and Count, Len, NUnique and NullCount all output Uint64.
// Int128 had no path at all and failed outright, which mattered because Int128 is
// what EVERY integer Sum produces, so Col("x").Sum().Cast(String) was the same
// ordinary query that broke Abs. Both were promises dtype.CanCast made and this
// kernel did not keep.
func readAnyInt(c *data.Column) ([]string, error) {
	n := c.Len()
	out := make([]string, n)

	switch c.DType().Physical().ID() {
	case dtype.TypeInt128:
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		// A Decimal is physically an Int128 of unscaled units, so it lands here and
		// would otherwise print 12.34 as "1234" — wrong by a factor of 10^scale, and
		// plausible enough that nobody would question it. This is the same guard
		// DataFrame.String() carries for the same reason.
		if dt := c.DType(); dt.ID() == dtype.TypeDecimal {
			for i := range n {
				out[i] = dtype.FormatDecimal(v[i].String(), dt.Scale())
			}
			return out, nil
		}
		for i := range n {
			out[i] = v[i].String()
		}
		return out, nil

	case dtype.TypeUint8:
		return formatUints[uint8](c, out)
	case dtype.TypeUint16:
		return formatUints[uint16](c, out)
	case dtype.TypeUint32:
		return formatUints[uint32](c, out)
	case dtype.TypeUint64:
		return formatUints[uint64](c, out)
	case dtype.TypeInt8:
		return formatInts[int8](c, out)
	case dtype.TypeInt16:
		return formatInts[int16](c, out)
	case dtype.TypeInt32:
		return formatInts[int32](c, out)
	case dtype.TypeInt64:
		return formatInts[int64](c, out)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cannot format a %s column as text", c.DType())
	}
}

func formatUints[T ~uint8 | ~uint16 | ~uint32 | ~uint64](c *data.Column, out []string) ([]string, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = strconv.FormatUint(uint64(v[i]), 10)
	}
	return out, nil
}

func formatInts[T ~int8 | ~int16 | ~int32 | ~int64](c *data.Column, out []string) ([]string, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = strconv.FormatInt(int64(v[i]), 10)
	}
	return out, nil
}

// FloatText is a float as Polars casts one to text (step 325; it was Go's shortest
// text, 2 and -0 and 1e-07): the shortest digits that read back as it at its width,
// plain from 1e-5 to below 1e16, a whole number with ".0", and otherwise in
// scientific notation with a signed exponent of no padding, "1e+16" and "1.5e-7";
// NaN, inf and -inf.
func FloatText(v float64, bits int) string { return string(AppendFloatText(nil, v, bits)) }

// AppendFloatText appends FloatText's text of v to dst, one allocation a value at
// most where FloatText adds one for its string.
func AppendFloatText(dst []byte, v float64, bits int) []byte {
	switch {
	case math.IsNaN(v):
		return append(dst, "NaN"...)
	case math.IsInf(v, 1):
		return append(dst, "inf"...)
	case math.IsInf(v, -1):
		return append(dst, "-inf"...)
	}
	start := len(dst)
	if a := math.Abs(v); a == 0 || a >= 1e-5 && a < 1e16 {
		dst = strconv.AppendFloat(dst, v, 'f', -1, bits)
		if slices.Index(dst[start:], '.') < 0 {
			dst = append(dst, ".0"...)
		}
		return dst
	}
	dst = strconv.AppendFloat(dst, v, 'e', -1, bits)
	// Go writes an exponent of two digits at least, "1e-07"; Polars of no padding.
	at := start + slices.Index(dst[start:], 'e') + 2 // past e and its sign
	for at < len(dst)-1 && dst[at] == '0' {
		dst = append(dst[:at], dst[at+1:]...)
	}
	return dst
}
