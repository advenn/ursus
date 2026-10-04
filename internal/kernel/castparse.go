package kernel

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// parseFromString converts a String column into to, parsing every value AT THE
// TARGET'S OWN WIDTH.
//
// Non-strict is the default and turns a value it cannot convert into a NULL, which
// is why the type checker marks every non-strict cast nullable. Strict fails the
// query and names the offending row and text — the same contract the CSV reader
// honours, and for the same reason: "cannot parse" on a ten-million-row frame is
// useless.
//
// # Why at the target's width
//
// Every numeric target used to parse into an int64 or a float64 and narrow from
// there through narrow, whose round-trip test compared a value that had already
// rounded with itself. So "9007199254740993" became …992, a Uint64 above MaxInt64
// could not be parsed at all — ParseInt stops at MaxInt64 — and "0.1" to Float32
// was a null, because the float32 nearest 0.1 is not the float64 nearest it. And
// the hand-off to narrow passed strict=false whatever the caller asked, so "256"
// to Uint8 under a strict cast was a null rather than a refusal.
//
// strconv parses at any width and rounds once: ParseInt and ParseUint report a
// value past the width as out of range, and ParseFloat(s, 32) is the float32
// nearest the text, not the float32 nearest the float64 nearest it. So the parse IS
// the conversion, and there is nothing left to narrow.
func parseFromString(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	switch to.ID() {
	case dtype.TypeInt8:
		return parseColumn(name, to, strict, c, parseSigned[int8](8))
	case dtype.TypeInt16:
		return parseColumn(name, to, strict, c, parseSigned[int16](16))
	case dtype.TypeInt32:
		return parseColumn(name, to, strict, c, parseSigned[int32](32))
	case dtype.TypeInt64:
		return parseColumn(name, to, strict, c, parseSigned[int64](64))
	case dtype.TypeUint8:
		return parseColumn(name, to, strict, c, parseUnsigned[uint8](8))
	case dtype.TypeUint16:
		return parseColumn(name, to, strict, c, parseUnsigned[uint16](16))
	case dtype.TypeUint32:
		return parseColumn(name, to, strict, c, parseUnsigned[uint32](32))
	case dtype.TypeUint64:
		return parseColumn(name, to, strict, c, parseUnsigned[uint64](64))
	case dtype.TypeFloat32:
		return parseColumn(name, to, strict, c, parseFloat[float32](32))
	case dtype.TypeFloat64:
		return parseColumn(name, to, strict, c, parseFloat[float64](64))
	case dtype.TypeBool:
		return parseBools(name, strict, c)
	}
	if to.IsTemporal() {
		// Temporal storage is exactly int32 days or an int64 tick count.
		if to.Physical().ID() == dtype.TypeInt32 {
			return parseColumn(name, to, strict, c, func(s string) (int32, parseResult) {
				v, ok := dtype.ParseTemporal(to, s)
				switch {
				case !ok:
					return 0, malformed
				case v < math.MinInt32 || v > math.MaxInt32:
					return 0, outOfRange
				}
				return int32(v), parsed
			})
		}
		return parseColumn(name, to, strict, c, func(s string) (int64, parseResult) {
			v, ok := dtype.ParseTemporal(to, s)
			if !ok {
				return 0, malformed
			}
			return v, parsed
		})
	}
	return nil, uerr.New(uerr.KindUnsupported, "cast",
		"cast from %s to %s is not implemented yet", c.DType(), to)
}

// parseResult is what one parse found.
type parseResult uint8

const (
	parsed     parseResult = iota
	malformed              // not a number of this kind at all
	outOfRange             // a number, and one the target cannot hold
)

// parseColumn runs parse over every valid row: the value, or under a strict cast
// the refusal naming the row and the text, or a null.
func parseColumn[T data.Fixed](name string, to dtype.DataType, strict bool, c *data.Column,
	parse func(string) (T, parseResult)) (*data.Column, error) {

	acc := c.Strings()
	n := c.Len()
	valid := c.Validity()
	buf, dst := newValuesBuffer[T](n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		if !valid.Get(i) {
			ok.Append(false)
			continue
		}
		s := acc.Get(i)
		v, r := parse(s)
		if r != parsed {
			if strict {
				return nil, parseRefusal(s, i, to, r)
			}
			ok.Append(false)
			continue
		}
		dst[i] = v
		ok.Append(true)
	}
	return data.NewFixedBuffer(name, to, buf, n, ok.Finish()), nil
}

// parseRefusal says why s at row i did not convert under a strict cast. Text that
// is no number "cannot parse"; a number the target cannot hold is "not
// representable", with the target's range — two different mistakes, and the old
// message called the second the first.
func parseRefusal(s string, row int, to dtype.DataType, r parseResult) *uerr.Error {
	if r == malformed {
		return uerr.New(uerr.KindValue, "cast",
			"cannot parse %s as %s at row %d", strconv.Quote(s), to, row).
			Hint("use CastLossy to turn unparseable values into nulls instead")
	}
	if to.IsFloat() {
		return floatOverflow(strconv.Quote(s), row, to)
	}
	e := unrepresentable(strconv.Quote(s), row, to)
	if lo, hi, ok := integerRange(to); ok {
		e.Hint("%s holds %s to %s", to, lo, hi)
	}
	return e
}

// integerRange is the closed range of an integer target, as text.
func integerRange(to dtype.DataType) (lo, hi string, ok bool) {
	if !to.IsInteger() || to.ID() == dtype.TypeInt128 {
		return "", "", false
	}
	w := to.BitWidth()
	if to.IsSignedInteger() {
		return fmt.Sprint(-(int64(1) << (w - 1))), fmt.Sprint(int64(1)<<(w-1) - 1), true
	}
	return "0", fmt.Sprint(uint64(math.MaxUint64) >> (64 - w)), true
}

// parseSigned parses a base-10 integer at bits bits: an optional sign, then digits.
func parseSigned[T ~int8 | ~int16 | ~int32 | ~int64](bits int) func(string) (T, parseResult) {
	return func(s string) (T, parseResult) {
		v, err := strconv.ParseInt(s, 10, bits)
		switch {
		case errors.Is(err, strconv.ErrRange) && decimalInteger(s):
			return 0, outOfRange
		case err != nil:
			return 0, malformed
		}
		return T(v), parsed
	}
}

// parseUnsigned parses a base-10 integer at bits bits, with the same grammar as
// parseSigned — an optional sign, then digits — which ParseUint does not accept on
// its own: it refuses any sign. So "+5" is 5 and "-0" is 0, as they were when every
// integer went through ParseInt, and "-1" is a number out of range, not text that
// cannot be parsed.
func parseUnsigned[T ~uint8 | ~uint16 | ~uint32 | ~uint64](bits int) func(string) (T, parseResult) {
	return func(s string) (T, parseResult) {
		neg, rest := false, s
		if len(rest) > 0 && (rest[0] == '+' || rest[0] == '-') {
			neg, rest = rest[0] == '-', rest[1:]
		}
		v, err := strconv.ParseUint(rest, 10, bits)
		switch {
		case errors.Is(err, strconv.ErrRange) && decimalInteger(s):
			return 0, outOfRange
		case err != nil:
			return 0, malformed
		case neg && v != 0:
			return 0, outOfRange
		}
		return T(v), parsed
	}
}

// decimalInteger reports whether s is an optional sign and then digits. strconv
// reports a range error as soon as the digits overflow, before it reads the rest:
// "16777217.5" is "out of range" for an Int8, though it is no integer at all.
func decimalInteger(s string) bool {
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseFloat parses at bits bits, which is the one rounding. ParseFloat reports only
// an OVERFLOW as out of range — a value underflowing to a subnormal or to zero is
// rounding, as it is for every cast to a float — and accepts "inf" and "NaN".
func parseFloat[T ~float32 | ~float64](bits int) func(string) (T, parseResult) {
	return func(s string) (T, parseResult) {
		v, err := dtype.ParseFloat(s, bits)
		switch {
		case errors.Is(err, strconv.ErrRange):
			return 0, outOfRange
		case err != nil:
			return 0, malformed
		}
		return T(v), parsed
	}
}

// parseBools parses with strconv.ParseBool, which is a bitmap rather than a buffer
// of values, so it has a loop of its own.
func parseBools(name string, strict bool, c *data.Column) (*data.Column, error) {
	acc := c.Strings()
	n := c.Len()
	valid := c.Validity()
	vals := bitmap.NewBuilder(n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		if !valid.Get(i) {
			vals.Append(false)
			ok.Append(false)
			continue
		}
		s := acc.Get(i)
		v, err := strconv.ParseBool(s)
		if err != nil && strict {
			return nil, parseRefusal(s, i, dtype.Bool, malformed)
		}
		vals.Append(v)
		ok.Append(err == nil)
	}
	return data.NewBool(name, vals.Finish(), ok.Finish()), nil
}
