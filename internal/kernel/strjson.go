package kernel

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// JSON in strings (step 271, S of v0.6.6-scope.md): Polars' json_path_match and
// json_decode.

// jsonValue is a parsed JSON value, its object's members in their order.
type jsonValue struct {
	kind  byte // 'n' null, 'b' Boolean, '0' number, 's' string, '[' array, '{' object
	b     bool
	text  string // a number's literal or a string's value
	keys  []string
	items []*jsonValue
}

var errNotJSON = errors.New("not JSON")

// parseJSON is s as one JSON value. Of an object's repeated keys, json_path_match
// keeps the last value, in the first one's place, and json_decode the first, as
// Polars' two parsers do.
func parseJSON(s string, firstWins bool) (*jsonValue, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := readJSON(dec, firstWins)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errNotJSON
	}
	return v, nil
}

func readJSON(dec *json.Decoder, firstWins bool) (*jsonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case nil:
		return &jsonValue{kind: 'n'}, nil
	case bool:
		return &jsonValue{kind: 'b', b: t}, nil
	case json.Number:
		return &jsonValue{kind: '0', text: string(t)}, nil
	case string:
		return &jsonValue{kind: 's', text: t}, nil
	}
	open, _ := tok.(json.Delim)
	if open != '[' && open != '{' {
		return nil, errNotJSON
	}
	v := &jsonValue{kind: byte(open)}
	var at map[string]int
	for dec.More() {
		key := ""
		if open == '{' {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ = kt.(string)
		}
		item, err := readJSON(dec, firstWins)
		if err != nil {
			return nil, err
		}
		if open == '{' {
			if k, ok := v.member(key, at); ok {
				if !firstWins {
					v.items[k] = item
				}
				continue
			}
			v.keys = append(v.keys, key)
			if at == nil && len(v.keys) > 16 {
				at = make(map[string]int, 2*len(v.keys))
				for k, name := range v.keys {
					at[name] = k
				}
			} else if at != nil {
				at[key] = len(v.keys) - 1
			}
		}
		v.items = append(v.items, item)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return v, nil
}

// member is the index of an object's key, looked up in at where an object large
// enough has one.
func (v *jsonValue) member(key string, at map[string]int) (int, bool) {
	if at != nil {
		k, ok := at[key]
		return k, ok
	}
	for k, name := range v.keys {
		if name == key {
			return k, true
		}
	}
	return 0, false
}

// describe names a value's kind for a refusal.
func (v *jsonValue) describe() string {
	switch v.kind {
	case 'b':
		return "a Boolean"
	case '0':
		return "the number " + v.text
	case 's':
		return "a string"
	case '[':
		return "an array"
	}
	return "an object"
}

// matchPath is the value at steps in v, nil where there is none. ..key is the key
// here, or else its first place below, depth first in order, as Polars finds it.
func matchPath(v *jsonValue, steps []expr.JSONPathStep) *jsonValue {
	if len(steps) == 0 {
		return v
	}
	s := steps[0]
	switch {
	case s.Deep:
		if m := matchPath(v, append([]expr.JSONPathStep{{Key: s.Key}}, steps[1:]...)); m != nil {
			return m
		}
		if v.kind == '[' || v.kind == '{' {
			for _, item := range v.items {
				if m := matchPath(item, steps); m != nil {
					return m
				}
			}
		}
	case s.IsIndex:
		at := s.Index
		if at < 0 {
			at += len(v.items)
		}
		if v.kind == '[' && at >= 0 && at < len(v.items) {
			return matchPath(v.items[at], steps[1:])
		}
	case v.kind == '{':
		if k, ok := v.member(s.Key, nil); ok {
			return matchPath(v.items[k], steps[1:])
		}
	}
	return nil
}

// writeJSON writes v compactly, its keys in order, as Polars' json_path_match does:
// an integer as written, where it fits 64 bits, and any other number reprinted.
func writeJSON(b *strings.Builder, v *jsonValue) {
	switch v.kind {
	case 'n':
		b.WriteString("null")
	case 'b':
		b.WriteString(strconv.FormatBool(v.b))
	case '0':
		b.WriteString(pathNumber(v.text))
	case 's':
		quoteJSON(b, v.text)
	case '[', '{':
		b.WriteByte(v.kind)
		for k, item := range v.items {
			if k > 0 {
				b.WriteByte(',')
			}
			if v.kind == '{' {
				quoteJSON(b, v.keys[k])
				b.WriteByte(':')
			}
			writeJSON(b, item)
		}
		if v.kind == '[' {
			b.WriteByte(']')
		} else {
			b.WriteByte('}')
		}
	}
}

// quoteJSON writes s as a JSON string, escaping only what JSON must: a quote, a
// backslash and a control character.
func quoteJSON(b *strings.Builder, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\f':
			b.WriteString(`\f`)
		case c < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// isIntegerLiteral reports whether a JSON number is written as an integer.
func isIntegerLiteral(lit string) bool { return !strings.ContainsAny(lit, ".eE") }

// pathNumber is a JSON number as json_path_match prints it: an integer that fits an
// Int64 or a UInt64 as written, and any other number, -0 among them, as a float in
// its shortest form.
func pathNumber(lit string) string {
	if isIntegerLiteral(lit) && lit != "-0" {
		if _, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return lit
		}
		if _, err := strconv.ParseUint(lit, 10, 64); err == nil {
			return lit
		}
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return lit
	}
	return shortFloat(f)
}

// shortFloat is f's shortest digits laid out as Rust's ryu lays them out, but with
// an exponent's + sign, as Polars prints them: 1000.0 and 0.00001 in full, and from
// 1e+16 up and 1e-6 down in exponent form.
func shortFloat(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	mant, exp, _ := strings.Cut(strconv.FormatFloat(math.Abs(f), 'e', -1, 64), "e")
	digits := strings.Replace(mant, ".", "", 1)
	e, _ := strconv.Atoi(exp)
	kk, n := e+1, len(digits) // 10^(kk-1) <= |f| < 10^kk
	var b strings.Builder
	if f < 0 {
		b.WriteByte('-')
	}
	switch {
	case n <= kk && kk <= 16:
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", kk-n))
		b.WriteString(".0")
	case 0 < kk && kk <= 16:
		b.WriteString(digits[:kk])
		b.WriteByte('.')
		b.WriteString(digits[kk:])
	case -5 < kk && kk <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -kk))
		b.WriteString(digits)
	default:
		b.WriteString(digits[:1])
		if n > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		b.WriteByte('e')
		if kk > 1 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.Itoa(kk - 1))
	}
	return b.String()
}

// jsonPathMatch is each value's match of a JSONPath, as text: a string unquoted, any
// other value its compact JSON. JSON null, a path that matches nothing and text that
// is not JSON are null, as Polars makes them.
func jsonPathMatch(name string, c *data.Column, path string) (*data.Column, error) {
	steps, err := expr.ParseJSONPath(path)
	if err != nil {
		return nil, err
	}
	acc := c.Strings()
	n := c.Len()
	res := make([]string, n)
	ok := bitmap.NewBuilder(n)
	var b strings.Builder
	for i := range n {
		var m *jsonValue
		if c.IsValid(i) {
			if v, err := parseJSON(acc.Get(i), false); err == nil {
				m = matchPath(v, steps)
			}
		}
		switch {
		case m == nil || m.kind == 'n':
		case m.kind == 's':
			res[i] = m.text
		default:
			b.Reset()
			writeJSON(&b, m)
			res[i] = b.String()
		}
		ok.Append(m != nil && m.kind != 'n')
	}
	return data.NewString(name, res, ok.Finish()), nil
}

// jsonDecode is each value's JSON as out, a type of Booleans, numbers, Strings, Lists
// and Structs: Polars' json_decode with its dtype. Text that is not JSON is refused,
// naming its row.
func jsonDecode(name string, out dtype.DataType, c *data.Column) (*data.Column, error) {
	acc := c.Strings()
	vals := make([]*jsonValue, c.Len())
	for i := range vals {
		if !c.IsValid(i) {
			continue
		}
		v, err := parseJSON(acc.Get(i), true)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindValue, "str.json_decode", "row %d is not JSON", i)
		}
		vals[i] = v
	}
	return jsonColumn(name, out, vals)
}

// jsonColumn is vals as a column of dt, as Polars converts them: a Struct's missing
// fields are null and its extra ones skipped; a List of a value not an array is that
// one value; a Boolean is 1 or 0 as a number; a float is truncated as an integer, and
// a number past dt's range is null; and a number or a Boolean as a String is its
// text. Any other pairing, an object as a List or a string as a number, is refused.
// nil and JSON null are null.
func jsonColumn(name string, dt dtype.DataType, vals []*jsonValue) (*data.Column, error) {
	n := len(vals)
	valid := bitmap.NewBuilder(n)
	refuse := func(v *jsonValue) error {
		return uerr.New(uerr.KindValue, "str.json_decode", "json_decode cannot read %s as %s", v.describe(), dt)
	}
	present := func(v *jsonValue) bool { return v != nil && v.kind != 'n' }
	switch {
	case dt.ID() == dtype.TypeList:
		var elems []*jsonValue
		offs := make([]int32, 1, n+1)
		for _, v := range vals {
			switch {
			case !present(v):
			case v.kind == '[':
				elems = append(elems, v.items...)
			case v.kind == '{':
				return nil, refuse(v)
			default:
				elems = append(elems, v)
			}
			offs = append(offs, int32(len(elems)))
			valid.Append(present(v))
		}
		child, err := jsonColumn(name, dt.Inner(), elems)
		if err != nil {
			return nil, err
		}
		return data.NewList(name, offs, child, valid.Finish()), nil
	case dt.ID() == dtype.TypeStruct:
		fields := dt.Fields()
		parts := make([][]*jsonValue, len(fields))
		for k := range fields {
			parts[k] = make([]*jsonValue, n)
		}
		for i, v := range vals {
			if present(v) {
				if v.kind != '{' {
					return nil, refuse(v)
				}
				for k, f := range fields {
					if at, ok := v.member(f.Name, nil); ok {
						parts[k][i] = v.items[at]
					}
				}
			}
			valid.Append(present(v))
		}
		cols := make([]*data.Column, len(fields))
		for k, f := range fields {
			col, err := jsonColumn(f.Name, f.Type, parts[k])
			if err != nil {
				return nil, err
			}
			cols[k] = col
		}
		return data.NewStruct(name, cols, valid.Finish()), nil
	case dt.ID() == dtype.TypeBool:
		bits := bitmap.NewBuilder(n)
		for _, v := range vals {
			if present(v) && v.kind != 'b' {
				return nil, refuse(v)
			}
			bits.Append(present(v) && v.b)
			valid.Append(present(v))
		}
		return data.NewBool(name, bits.Finish(), valid.Finish()), nil
	case dt.ID() == dtype.TypeString:
		res := make([]string, n)
		for i, v := range vals {
			if present(v) {
				switch v.kind {
				case 's':
					res[i] = v.text
				case 'b':
					res[i] = strconv.FormatBool(v.b)
				case '0':
					res[i] = textNumber(v.text)
				default:
					return nil, refuse(v)
				}
			}
			valid.Append(present(v))
		}
		return data.NewString(name, res, valid.Finish()), nil
	case dt.IsFloat():
		res := make([]float64, n)
		for i, v := range vals {
			if present(v) {
				switch v.kind {
				case 'b':
					res[i] = b2f(v.b)
				case '0':
					// Past a float's range is an infinity, the error saying only that.
					res[i], _ = strconv.ParseFloat(v.text, 64)
				default:
					return nil, refuse(v)
				}
			}
			valid.Append(present(v))
		}
		if dt == dtype.Float32 {
			// A double past a float's range is an infinity, as Polars narrows it.
			narrow := make([]float32, n)
			for i, f := range res {
				narrow[i] = float32(f)
			}
			return data.NewFixed(name, dtype.Float32, narrow, valid.Finish()), nil
		}
		return data.NewFixed(name, dtype.Float64, res, valid.Finish()), nil
	case dt.IsUnsignedInteger():
		res := make([]uint64, n)
		for i, v := range vals {
			ok := false
			if present(v) {
				var err error
				if res[i], ok, err = jsonUint(v); err != nil {
					return nil, refuse(v)
				}
			}
			valid.Append(ok)
		}
		return castLossy(name, dt, data.NewFixed(name, dtype.Uint64, res, valid.Finish()))
	case dt.IsInteger():
		res := make([]int64, n)
		for i, v := range vals {
			ok := false
			if present(v) {
				var err error
				if res[i], ok, err = jsonInt(v); err != nil {
					return nil, refuse(v)
				}
			}
			valid.Append(ok)
		}
		return castLossy(name, dt, data.NewFixed(name, dtype.Int64, res, valid.Finish()))
	case dt.IsNull():
		return NullColumn(name, dt, n)
	}
	return nil, uerr.Internalf("kernel: json_decode to %s", dt)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// jsonInt is a Boolean or a number as an Int64, a float truncated, and whether it
// fits one.
func jsonInt(v *jsonValue) (int64, bool, error) {
	switch {
	case v.kind == 'b':
		return int64(b2f(v.b)), true, nil
	case v.kind != '0':
		return 0, false, errNotJSON
	case isIntegerLiteral(v.text):
		x, err := strconv.ParseInt(v.text, 10, 64)
		return x, err == nil, nil
	}
	f, _ := strconv.ParseFloat(v.text, 64)
	if f >= -(1<<63) && f < 1<<63 {
		return int64(f), true, nil
	}
	return 0, false, nil
}

// jsonUint is jsonInt as a UInt64: a negative number, but a fraction truncated to
// zero, does not fit.
func jsonUint(v *jsonValue) (uint64, bool, error) {
	switch {
	case v.kind == 'b':
		return uint64(b2f(v.b)), true, nil
	case v.kind != '0':
		return 0, false, errNotJSON
	case isIntegerLiteral(v.text):
		x, err := strconv.ParseUint(v.text, 10, 64)
		return x, err == nil, nil
	}
	f, _ := strconv.ParseFloat(v.text, 64)
	if f > -1 && f < 1<<64 {
		return uint64(f), true, nil
	}
	return 0, false, nil
}

// textNumber is a JSON number as json_decode makes it a String: an integer as
// written, -0 as 0, and a float in its shortest form without an exponent.
func textNumber(lit string) string {
	if isIntegerLiteral(lit) {
		if lit == "-0" {
			return "0"
		}
		return lit
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return lit
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// castLossy is c as dt, a value dt cannot hold null.
func castLossy(name string, dt dtype.DataType, c *data.Column) (*data.Column, error) {
	if c.DType() == dt {
		return c, nil
	}
	return Cast(name, dt, false, c)
}
