package ndjson

import (
	"strconv"

	"github.com/advenn/ursus/dtype"
)

// scanFloat moves past the JSON number at p, which begins with '-' or a digit, as
// scanNumber does, and is its value, the float64 nearest it as strconv.ParseFloat
// finds it: a number past a float's range ±Inf, as Polars reads one. The digits are
// read once, as they are passed, and the float found from them as strconv finds it, by
// Clinger's exact case or the Eisel-Lemire algorithm; strconv itself reads what
// neither settles, more than 19 digits that matter or a halfway case.
func (p *parser) scanFloat() (float64, error) {
	start := p.pos
	neg := p.s[p.pos] == '-'
	if neg {
		p.pos++
	}
	var man uint64
	nd, exp := 0, 0 // man's digits, and the power of ten it is scaled by
	exact := true   // no digit that matters is left out of man
	digit := func(c byte, frac bool) {
		switch {
		case man == 0 && c == '0': // a leading zero
			if frac {
				exp--
			}
		case nd < 19:
			man = man*10 + uint64(c-'0')
			nd++
			if frac {
				exp--
			}
		default:
			exact = exact && c == '0'
			if !frac {
				exp++
			}
		}
	}
	whole := p.pos
	for p.pos < len(p.s) && p.s[p.pos]-'0' <= 9 {
		digit(p.s[p.pos], false)
		p.pos++
	}
	switch n := p.pos - whole; {
	case n == 0:
		return 0, p.fail("a number's digits were expected")
	case n > 1 && p.s[whole] == '0':
		p.pos = whole + 1
		return 0, p.fail("a number has a leading zero")
	}
	if p.pos < len(p.s) && p.s[p.pos] == '.' {
		p.pos++
		frac := p.pos
		for p.pos < len(p.s) && p.s[p.pos]-'0' <= 9 {
			digit(p.s[p.pos], true)
			p.pos++
		}
		if p.pos == frac {
			return 0, p.fail("a fraction's digits were expected")
		}
	}
	if p.pos < len(p.s) && (p.s[p.pos] == 'e' || p.s[p.pos] == 'E') {
		p.pos++
		eneg := false
		if p.pos < len(p.s) && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
			eneg = p.s[p.pos] == '-'
			p.pos++
		}
		e, from := 0, p.pos
		for p.pos < len(p.s) && p.s[p.pos]-'0' <= 9 {
			if e < 1_000_000 { // past it the answer is 0 or ±Inf all the same
				e = e*10 + int(p.s[p.pos]-'0')
			}
			p.pos++
		}
		if p.pos == from {
			return 0, p.fail("an exponent's digits were expected")
		}
		if eneg {
			e = -e
		}
		exp += e
	}
	if exact {
		if f, ok := dtype.NearestFloat(man, exp, neg); ok {
			return f, nil
		}
	}
	// A number past a float's range is ParseFloat's ±Inf with an error, which is
	// how Polars reads one; text it accepts is the only text here.
	f, _ := strconv.ParseFloat(str(p.s[start:p.pos]), 64)
	return f, nil
}
