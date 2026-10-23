package kernel

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// struct.json_encode (step 231): each struct as a JSON object, its fields in order.
//
// # Each value as JSON has it, and as ursus writes it
//
//   - a null is null, a Bool true or false, an integer its digits;
//   - a float is the shortest decimal that reads back to it, with ".0" on a whole
//     number and an exponent past 10¹⁶ or under 10⁻⁵, as Polars writes 1.0, 1e+20
//     and 1e-7; NaN and the infinities, which JSON has no number for, are null;
//   - a String is quoted, with a quote, a backslash and the control characters
//     escaped and every other character as it is; a byte that is not UTF-8 is
//     U+FFFD;
//   - a Decimal, a Date, a Time, a Datetime, a Duration and an Enum are strings of
//     the text a Cast to String gives them, the one the CSV writer writes;
//   - a List is an array, a Struct an object.
//
// A null struct is a null String, where Polars writes the text "null": a null in,
// null out, as everywhere else.

// JSONEncode encodes each row of a Struct column.
func JSONEncode(name string, c *data.Column) (*data.Column, error) {
	j, err := newJSONCol(c)
	if err != nil {
		return nil, err
	}
	n := c.Len()
	vals := make([]string, n)
	var buf []byte
	for i := range n {
		if !c.IsValid(i) {
			continue
		}
		buf = j.append(buf[:0], i)
		vals[i] = string(buf)
	}
	return data.NewString(name, vals, c.Validity()), nil
}

type jsonKind uint8

const (
	jsonNull   jsonKind = iota
	jsonRaw             // its String cast, as it is: a Bool, an integer
	jsonQuoted          // its String cast, quoted: a Decimal, a temporal, an Enum
	jsonString
	jsonFloat
	jsonList
	jsonStruct
)

// jsonCol is a column ready to encode: its kind, and what each kind reads.
type jsonCol struct {
	c      *data.Column
	kind   jsonKind
	text   []string // jsonRaw and jsonQuoted: each row's String cast
	floats []float64
	bits   int // a float's width, 32 or 64
	elem   *jsonCol
	fields []*jsonCol
	keys   []string // each field's name, quoted
}

func newJSONCol(c *data.Column) (*jsonCol, error) {
	j := &jsonCol{c: c}
	dt := c.DType()
	switch {
	case dt.IsNull():
		j.kind = jsonNull
	case dt.ID() == dtype.TypeStruct:
		j.kind = jsonStruct
		for _, f := range c.Fields() {
			fj, err := newJSONCol(f)
			if err != nil {
				return nil, err
			}
			j.fields = append(j.fields, fj)
			j.keys = append(j.keys, string(appendJSONString(nil, f.Name())))
		}
	case dt.IsListLike():
		j.kind = jsonList
		elem, err := newJSONCol(c.Lists().Child())
		if err != nil {
			return nil, err
		}
		j.elem = elem
	case dt.HasStringStorage() && dt.ID() != dtype.TypeBinary:
		j.kind = jsonString
	case dt.IsFloat():
		j.kind, j.bits = jsonFloat, 64
		if dt.ID() == dtype.TypeFloat32 {
			j.bits = 32
		}
		f, err := toFloat64(c)
		if err != nil {
			return nil, err
		}
		j.floats = f
	case dt.IsBool() || (dt.IsInteger() && dt.ID() != dtype.TypeEnum):
		j.kind = jsonRaw
	case dt.ID() == dtype.TypeDecimal || dt.IsTemporal() || dt.ID() == dtype.TypeEnum:
		j.kind = jsonQuoted
	default:
		return nil, uerr.Internalf("kernel: struct.json_encode of %s", dt)
	}
	if j.kind == jsonRaw || j.kind == jsonQuoted {
		s, err := Cast(c.Name(), dtype.String, false, c)
		if err != nil {
			return nil, err
		}
		acc := s.Strings()
		j.text = make([]string, s.Len())
		for i := range j.text {
			if s.IsValid(i) {
				j.text[i] = acc.Get(i)
			}
		}
	}
	return j, nil
}

// append writes row i, which the caller has found valid or whose nulls this
// writes as null.
func (j *jsonCol) append(buf []byte, i int) []byte {
	if j.kind == jsonNull || !j.c.IsValid(i) {
		return append(buf, "null"...)
	}
	switch j.kind {
	case jsonRaw:
		return append(buf, j.text[i]...)
	case jsonQuoted:
		return appendJSONString(buf, j.text[i])
	case jsonString:
		return appendJSONString(buf, j.c.Strings().Get(i))
	case jsonFloat:
		return appendJSONFloat(buf, j.floats[i], j.bits)
	case jsonList:
		start, end, _ := j.c.Lists().Get(i)
		buf = append(buf, '[')
		for e := start; e < end; e++ {
			if e > start {
				buf = append(buf, ',')
			}
			buf = j.elem.append(buf, int(e))
		}
		return append(buf, ']')
	}
	buf = append(buf, '{')
	for k, f := range j.fields {
		if k > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, j.keys[k]...)
		buf = append(buf, ':')
		buf = f.append(buf, i)
	}
	return append(buf, '}')
}

// appendJSONFloat writes f as a JSON number, or null for a NaN or an infinity.
func appendJSONFloat(buf []byte, f float64, bits int) []byte {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return append(buf, "null"...)
	}
	e := strconv.FormatFloat(f, 'e', -1, bits)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if exp < -5 || exp >= 16 {
		// Polars' spelling: the exponent's sign, and no zeros before its digits.
		mant, rest, _ := strings.Cut(e, "e")
		sign := rest[:1]
		digits := strings.TrimLeft(rest[1:], "0")
		return append(append(append(append(buf, mant...), 'e'), sign...), digits...)
	}
	s := strconv.FormatFloat(f, 'f', -1, bits)
	buf = append(buf, s...)
	if !strings.ContainsAny(s, ".e") {
		buf = append(buf, ".0"...)
	}
	return buf
}

// appendJSONString writes s as a JSON string.
func appendJSONString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"' || r == '\\':
			buf = append(buf, '\\', byte(r))
		case r == '\n':
			buf = append(buf, '\\', 'n')
		case r == '\r':
			buf = append(buf, '\\', 'r')
		case r == '\t':
			buf = append(buf, '\\', 't')
		case r == '\b':
			buf = append(buf, '\\', 'b')
		case r == '\f':
			buf = append(buf, '\\', 'f')
		case r < 0x20:
			buf = append(buf, `\u00`...)
			buf = append(buf, "0123456789abcdef"[r>>4], "0123456789abcdef"[r&0xf])
		case r == utf8.RuneError && size == 1:
			buf = append(buf, "�"...)
		default:
			buf = append(buf, s[i:i+size]...)
		}
		i += size
	}
	return append(buf, '"')
}
