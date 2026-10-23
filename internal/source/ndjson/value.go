// Package ndjson reads newline-delimited JSON (step 334): Polars' read_ndjson and
// scan_ndjson, a JSON object a line, a column a key.
package ndjson

import (
	"errors"
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/advenn/ursus/internal/uerr"
)

// kind is what a JSON value is.
type kind uint8

const (
	kNull kind = iota
	kBool
	kInt    // a number with no fraction or exponent
	kFloat  // a number with either
	kString //
	kArray
	kObject
)

// value is one parsed JSON value. A number keeps its text, so that an integer past
// Int64 reaches Int128 whole, and an object its keys in their order.
type value struct {
	kind kind
	b    bool
	text string  // a number's text, or a string's decoded value
	arr  []value // an array's elements
	keys []string
	vals []value // an object's values, keys[i]'s at vals[i]
}

// parser parses JSON out of one line: as a tree of values, for inference and for an
// error's text, or straight into a column's builder, which builds no tree.
type parser struct {
	s       []byte
	pos     int
	scratch []byte // a key with an escape, decoded
	ignore  bool   // a value its column cannot take is a null, Polars' ignore_errors
	given   bool   // the columns' types are a given schema's, not inferred
}

// parseObject parses line as one JSON object and nothing more, but space.
func parseObject(line []byte) (value, error) {
	p := parser{s: line}
	p.space()
	v, err := p.value()
	if err != nil {
		return value{}, err
	}
	p.space()
	if p.pos != len(p.s) {
		return value{}, p.fail("text after the line's object")
	}
	if v.kind != kObject {
		return value{}, uerr.New(uerr.KindValue, "read_ndjson",
			"an NDJSON line holds a JSON object, and this one holds %s", v.kind.what())
	}
	return v, nil
}

func (k kind) what() string {
	switch k {
	case kNull:
		return "null"
	case kBool:
		return "a Boolean"
	case kInt, kFloat:
		return "a number"
	case kString:
		return "a string"
	case kArray:
		return "an array"
	}
	return "an object"
}

func (p *parser) fail(what string) error {
	return uerr.New(uerr.KindValue, "read_ndjson", "malformed JSON at byte %d: %s", p.pos+1, what)
}

func (p *parser) space() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

// value parses one value, depth-first; it may recurse as deep as the line nests.
func (p *parser) value() (value, error) {
	if p.pos >= len(p.s) {
		return value{}, p.fail("a value was expected")
	}
	switch c := p.s[p.pos]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		s, err := p.str()
		return value{kind: kString, text: s}, err
	case c == 't':
		return p.word("true", value{kind: kBool, b: true})
	case c == 'f':
		return p.word("false", value{kind: kBool})
	case c == 'n':
		return p.word("null", value{kind: kNull})
	case numeric(c):
		return p.number()
	}
	return value{}, p.fail("an unexpected character " + strconv.QuoteRune(rune(p.s[p.pos])))
}

func (p *parser) word(w string, v value) (value, error) {
	if err := p.lit(w); err != nil {
		return value{}, err
	}
	return v, nil
}

// lit moves past w, a word of JSON's, at p.
func (p *parser) lit(w string) error {
	if len(p.s)-p.pos < len(w) || string(p.s[p.pos:p.pos+len(w)]) != w {
		return p.fail("an unknown word")
	}
	p.pos += len(w)
	return nil
}

func (p *parser) object() (value, error) {
	p.pos++ // {
	v := value{kind: kObject}
	p.space()
	if p.pos < len(p.s) && p.s[p.pos] == '}' {
		p.pos++
		return v, nil
	}
	for {
		p.space()
		if p.pos >= len(p.s) || p.s[p.pos] != '"' {
			return value{}, p.fail("an object's key was expected")
		}
		k, err := p.str()
		if err != nil {
			return value{}, err
		}
		p.space()
		if p.pos >= len(p.s) || p.s[p.pos] != ':' {
			return value{}, p.fail("':' was expected after a key")
		}
		p.pos++
		p.space()
		val, err := p.value()
		if err != nil {
			return value{}, err
		}
		v.keys, v.vals = append(v.keys, k), append(v.vals, val)
		p.space()
		if p.pos >= len(p.s) {
			return value{}, p.fail("the object is not closed")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return v, nil
		default:
			return value{}, p.fail("',' or '}' was expected")
		}
	}
}

func (p *parser) array() (value, error) {
	p.pos++ // [
	v := value{kind: kArray, arr: []value{}}
	p.space()
	if p.pos < len(p.s) && p.s[p.pos] == ']' {
		p.pos++
		return v, nil
	}
	for {
		p.space()
		e, err := p.value()
		if err != nil {
			return value{}, err
		}
		v.arr = append(v.arr, e)
		p.space()
		if p.pos >= len(p.s) {
			return value{}, p.fail("the array is not closed")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return v, nil
		default:
			return value{}, p.fail("',' or ']' was expected")
		}
	}
}

// number parses a JSON number, keeping its text.
func (p *parser) number() (value, error) {
	start := p.pos
	k, err := p.scanNumber()
	if err != nil {
		return value{}, err
	}
	return value{kind: k, text: string(p.s[start:p.pos])}, nil
}

// scanNumber moves past the JSON number at p, which begins with '-' or a digit: an
// integer when it has neither a fraction nor an exponent, as Polars reads one. As
// JSON has it, and Polars, an integer part has no leading zero.
func (p *parser) scanNumber() (kind, error) {
	if p.s[p.pos] == '-' {
		p.pos++
	}
	start := p.pos
	if n := p.digits(); n == 0 {
		return 0, p.fail("a number's digits were expected")
	} else if n > 1 && p.s[start] == '0' {
		p.pos = start + 1
		return 0, p.fail("a number has a leading zero")
	}
	k := kInt
	if p.pos < len(p.s) && p.s[p.pos] == '.' {
		p.pos++
		if p.digits() == 0 {
			return 0, p.fail("a fraction's digits were expected")
		}
		k = kFloat
	}
	if p.pos < len(p.s) && (p.s[p.pos] == 'e' || p.s[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.s) && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
			p.pos++
		}
		if p.digits() == 0 {
			return 0, p.fail("an exponent's digits were expected")
		}
		k = kFloat
	}
	return k, nil
}

// digits moves past the digits at p, and is how many.
func (p *parser) digits() int {
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos]-'0' <= 9 {
		p.pos++
	}
	return p.pos - start
}

// numeric reports whether c begins a JSON number.
func numeric(c byte) bool { return c == '-' || c-'0' <= 9 }

// str parses a JSON string.
func (p *parser) str() (string, error) {
	b, err := p.appendStr(nil)
	return string(b), err
}

// plain is whether a byte is one a string holds as itself: not its closing quote, not
// an escape's backslash, and not a control character, which JSON refuses in one.
var plain = func() (t [256]bool) {
	for c := range t {
		t[c] = c >= 0x20 && c != '"' && c != '\\'
	}
	return t
}()

// appendStr appends the JSON string at p to dst, its escapes decoded: \u surrogate
// pairs as one rune, and a lone surrogate, or a byte that is not UTF-8, as U+FFFD.
func (p *parser) appendStr(dst []byte) ([]byte, error) {
	p.pos++ // "
	from := len(dst)
	for {
		start := p.pos
		for p.pos < len(p.s) && plain[p.s[p.pos]] {
			p.pos++
		}
		dst = append(dst, p.s[start:p.pos]...)
		if p.pos >= len(p.s) {
			return dst, p.fail("the string is not closed")
		}
		switch c := p.s[p.pos]; {
		case c == '"':
			p.pos++
			return validUTF8(dst, from), nil
		case c < 0x20:
			return dst, p.fail("a control character in a string")
		}
		p.pos++ // the backslash
		if p.pos >= len(p.s) {
			return dst, p.fail("the string is not closed")
		}
		e := p.s[p.pos]
		p.pos++
		switch e {
		case '"', '\\', '/':
			dst = append(dst, e)
		case 'b':
			dst = append(dst, '\b')
		case 'f':
			dst = append(dst, '\f')
		case 'n':
			dst = append(dst, '\n')
		case 'r':
			dst = append(dst, '\r')
		case 't':
			dst = append(dst, '\t')
		case 'u':
			r, ok := p.hex4()
			if !ok {
				return dst, p.fail("\\u wants four hex digits")
			}
			if utf16.IsSurrogate(r) {
				// A pair, 😀, is one rune; a lone half is U+FFFD.
				if p.pos+6 <= len(p.s) && p.s[p.pos] == '\\' && p.s[p.pos+1] == 'u' {
					save := p.pos
					p.pos += 2
					if r2, ok := p.hex4(); ok {
						if d := utf16.DecodeRune(r, r2); d != utf8.RuneError {
							dst = utf8.AppendRune(dst, d)
							continue
						}
					}
					p.pos = save
				}
				r = utf8.RuneError
			}
			dst = utf8.AppendRune(dst, r)
		default:
			return dst, p.fail("an unknown escape \\" + string(e))
		}
	}
}

// validUTF8 is dst with its bytes from on made UTF-8, each byte that is not U+FFFD, as
// a Go string's runes make it.
func validUTF8(dst []byte, from int) []byte {
	if utf8.Valid(dst[from:]) {
		return dst
	}
	bad := string(dst[from:])
	dst = dst[:from]
	for _, r := range bad {
		dst = utf8.AppendRune(dst, r)
	}
	return dst
}

// key is the object key at p, valid until the next key: a slice of the line when it
// has no escape and is UTF-8, as most keys are, else decoded into the scratch.
func (p *parser) key() ([]byte, error) {
	start := p.pos + 1
	end := start
	for end < len(p.s) && plain[p.s[end]] {
		end++
	}
	if end < len(p.s) && p.s[end] == '"' && utf8.Valid(p.s[start:end]) {
		p.pos = end + 1
		return p.s[start:end], nil
	}
	var err error
	p.scratch, err = p.appendStr(p.scratch[:0])
	return p.scratch, err
}

// skip moves past the value at p, checking it is JSON but building nothing of it: a
// key's value no column reads.
func (p *parser) skip() error {
	if p.pos >= len(p.s) {
		return p.fail("a value was expected")
	}
	switch c := p.s[p.pos]; {
	case c == '{':
		p.pos++
		p.space()
		if p.pos < len(p.s) && p.s[p.pos] == '}' {
			p.pos++
			return nil
		}
		for {
			p.space()
			if p.pos >= len(p.s) || p.s[p.pos] != '"' {
				return p.fail("an object's key was expected")
			}
			if err := p.skipStr(); err != nil {
				return err
			}
			p.space()
			if p.pos >= len(p.s) || p.s[p.pos] != ':' {
				return p.fail("':' was expected after a key")
			}
			p.pos++
			p.space()
			if err := p.skip(); err != nil {
				return err
			}
			p.space()
			if p.pos >= len(p.s) {
				return p.fail("the object is not closed")
			}
			switch p.s[p.pos] {
			case ',':
				p.pos++
			case '}':
				p.pos++
				return nil
			default:
				return p.fail("',' or '}' was expected")
			}
		}
	case c == '[':
		p.pos++
		p.space()
		if p.pos < len(p.s) && p.s[p.pos] == ']' {
			p.pos++
			return nil
		}
		for {
			p.space()
			if err := p.skip(); err != nil {
				return err
			}
			p.space()
			if p.pos >= len(p.s) {
				return p.fail("the array is not closed")
			}
			switch p.s[p.pos] {
			case ',':
				p.pos++
			case ']':
				p.pos++
				return nil
			default:
				return p.fail("',' or ']' was expected")
			}
		}
	case c == '"':
		return p.skipStr()
	case c == 't':
		return p.lit("true")
	case c == 'f':
		return p.lit("false")
	case c == 'n':
		return p.lit("null")
	case numeric(c):
		_, err := p.scanNumber()
		return err
	}
	return p.fail("an unexpected character " + strconv.QuoteRune(rune(p.s[p.pos])))
}

// skipStr moves past the JSON string at p, checking its escapes.
func (p *parser) skipStr() error {
	p.pos++ // "
	for {
		for p.pos < len(p.s) && plain[p.s[p.pos]] {
			p.pos++
		}
		if p.pos >= len(p.s) {
			return p.fail("the string is not closed")
		}
		switch c := p.s[p.pos]; {
		case c == '"':
			p.pos++
			return nil
		case c < 0x20:
			return p.fail("a control character in a string")
		}
		p.pos++ // the backslash
		if p.pos >= len(p.s) {
			return p.fail("the string is not closed")
		}
		e := p.s[p.pos]
		p.pos++
		switch e {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			if _, ok := p.hex4(); !ok {
				return p.fail("\\u wants four hex digits")
			}
		default:
			return p.fail("an unknown escape \\" + string(e))
		}
	}
}

// appendText appends the value at p as Polars writes one into a String column: text as
// itself; a number as Rust prints it, "1.5" for 1.50, "1000" for 1e3; true and false;
// and an array or an object as JSON, ", " between values and ": " after a key.
func (p *parser) appendText(dst []byte) ([]byte, error) {
	switch c := p.s[p.pos]; {
	case c == '"':
		return p.appendStr(dst)
	case numeric(c):
		return p.appendNumber(dst)
	}
	return p.appendNested(dst)
}

// appendNumber appends the number at p as Rust prints it, which is how Polars writes
// one as text: an integer in its digits, "0" for -0, one past 64 bits as written; a
// float in its shortest digits and no exponent, "1.5" for 1.50, "1000" for 1e3,
// "0.0000001" for 1E-7.
func (p *parser) appendNumber(dst []byte) ([]byte, error) {
	start := p.pos
	k, err := p.scanNumber()
	if err != nil {
		return dst, err
	}
	text := p.s[start:p.pos]
	if k == kInt {
		if n, ok := parseInt64(text); ok {
			return strconv.AppendInt(dst, n, 10), nil
		}
		if u, err := strconv.ParseUint(str(text), 10, 64); err == nil {
			return strconv.AppendUint(dst, u, 10), nil
		}
		return append(dst, text...), nil
	}
	f, _ := strconv.ParseFloat(str(text), 64) // past a float's range, ±Inf
	switch {
	case math.IsInf(f, 1):
		return append(dst, "inf"...), nil
	case math.IsInf(f, -1):
		return append(dst, "-inf"...), nil
	}
	return strconv.AppendFloat(dst, f, 'f', -1, 64), nil
}

// appendNested appends the value at p, inside an array or an object Polars writes as
// text, as Polars writes it: a string quoted, its quote, newline and carriage return
// escaped and nothing else, not even a backslash; an object's key quoted and not
// escaped at all; a number as appendNumber writes it.
func (p *parser) appendNested(dst []byte) ([]byte, error) {
	if p.pos >= len(p.s) {
		return dst, p.fail("a value was expected")
	}
	var err error
	switch c := p.s[p.pos]; {
	case c == '{' || c == '[':
		end := byte('}')
		if c == '[' {
			end = ']'
		}
		p.pos++
		dst = append(dst, c)
		p.space()
		if p.pos < len(p.s) && p.s[p.pos] == end {
			p.pos++
			return append(dst, end), nil
		}
		for first := true; ; first = false {
			p.space()
			if !first {
				dst = append(dst, ", "...)
			}
			if c == '{' {
				if p.pos >= len(p.s) || p.s[p.pos] != '"' {
					return dst, p.fail("an object's key was expected")
				}
				dst = append(dst, '"')
				if dst, err = p.appendStr(dst); err != nil {
					return dst, err
				}
				dst = append(dst, `": `...)
				p.space()
				if p.pos >= len(p.s) || p.s[p.pos] != ':' {
					return dst, p.fail("':' was expected after a key")
				}
				p.pos++
				p.space()
			}
			if dst, err = p.appendNested(dst); err != nil {
				return dst, err
			}
			p.space()
			if p.pos >= len(p.s) {
				if c == '{' {
					return dst, p.fail("the object is not closed")
				}
				return dst, p.fail("the array is not closed")
			}
			switch p.s[p.pos] {
			case ',':
				p.pos++
			case end:
				p.pos++
				return append(dst, end), nil
			default:
				if c == '{' {
					return dst, p.fail("',' or '}' was expected")
				}
				return dst, p.fail("',' or ']' was expected")
			}
		}
	case c == '"':
		if p.scratch, err = p.appendStr(p.scratch[:0]); err != nil {
			return dst, err
		}
		dst = append(dst, '"')
		for _, b := range p.scratch {
			switch b {
			case '"':
				dst = append(dst, '\\', b)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			default:
				dst = append(dst, b)
			}
		}
		return append(dst, '"'), nil
	case numeric(c):
		return p.appendNumber(dst)
	case c == 't':
		return append(dst, "true"...), p.lit("true")
	case c == 'f':
		return append(dst, "false"...), p.lit("false")
	case c == 'n':
		return append(dst, "null"...), p.lit("null")
	}
	return dst, p.fail("an unexpected character " + strconv.QuoteRune(rune(p.s[p.pos])))
}

// errNull is refuse's answer when a value its column cannot take is a null: the value
// is skipped, and its builder takes a null.
var errNull = errors.New("ndjson: a value read as a null")

// refuse is the error for the value at p, which a column of type t does not take: the
// value parsed whole, for its text, or the error it is malformed with; or errNull, the
// value skipped, when the parser ignores such errors.
func (p *parser) refuse(t string) error {
	if p.ignore {
		if err := p.skip(); err != nil {
			return err
		}
		return errNull
	}
	v, err := p.value()
	if err != nil {
		return err
	}
	return mismatch(v, t, !p.given)
}

func (p *parser) hex4() (rune, bool) {
	if p.pos+4 > len(p.s) {
		return 0, false
	}
	var r rune
	for _, c := range p.s[p.pos : p.pos+4] {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			r |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	p.pos += 4
	return r, true
}
