package dtype

import (
	"strconv"
	"strings"
)

// ParseFloat parses s as a decimal floating-point number, rounded once to bits bits.
// It is the one float grammar: the CSV reader's inference and its parser, and
// Cast(String) to a float, all ask it, so text one of them reads as a number the
// others do too.
//
// The grammar is the decimal one every dataframe and database reads:
//
//	[+-] (digits [. [digits]] | . digits) [(e|E) [+-] digits]
//	[+-] (inf | infinity | nan)            case-insensitive
//
// It is strconv.ParseFloat's, less what is Go's own: an underscore between digits,
// and a hexadecimal float. Those made "1_000" read as 1000 and "0x1p3" as 8, where
// Polars, pandas and DuckDB read both as text. strconv still does the rounding, but
// for a plain decimal exactFloat parses in one pass, to strconv's answer.
func ParseFloat(s string, bits int) (float64, error) {
	if bits == 64 {
		if f, ok := exactFloat(s); ok {
			return f, nil
		}
	}
	if !floatSyntax(s) {
		return 0, &strconv.NumError{Func: "ParseFloat", Num: s, Err: strconv.ErrSyntax}
	}
	return strconv.ParseFloat(s, bits)
}

// exactFloat parses s in one pass where it is a plain decimal, [+-] digits [. digits]
// [(e|E) [+-] digits], of at most 19 significant digits whose value is a whole
// number below 2^53 times or over a power of ten of at most 22: both are exact
// float64s, so one multiply or divide rounds once, to the nearest, and the answer is
// strconv's, which takes this path itself for these (Clinger's). ok is false for
// anything else, which ParseFloat reads as it did (step 373): a CSV's float column was
// scanned twice, once for the grammar and once by strconv.
func exactFloat(s string) (float64, bool) {
	i, neg := 0, false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	var mant uint64
	digits, sig, exp := 0, 0, 0
	for ; i < len(s) && isDigit(s[i]); i++ {
		digits++
		if mant != 0 || s[i] != '0' {
			mant, sig = mant*10+uint64(s[i]-'0'), sig+1
		}
	}
	if i < len(s) && s[i] == '.' {
		for i++; i < len(s) && isDigit(s[i]); i++ {
			digits++
			if mant != 0 || s[i] != '0' {
				mant, sig = mant*10+uint64(s[i]-'0'), sig+1
			}
			exp--
		}
	}
	if digits == 0 || sig > 19 {
		return 0, false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		eneg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			eneg = s[i] == '-'
			i++
		}
		e, ed := 0, 0
		for ; i < len(s) && isDigit(s[i]) && ed < 4; i, ed = i+1, ed+1 {
			e = e*10 + int(s[i]-'0')
		}
		if ed == 0 {
			return 0, false
		}
		if eneg {
			e = -e
		}
		exp += e
	}
	if i != len(s) || mant>>53 != 0 || exp < -22 || exp > 22 {
		return 0, false
	}
	f := float64(mant)
	if exp < 0 {
		f /= pow10[-exp]
	} else {
		f *= pow10[exp]
	}
	if neg {
		f = -f
	}
	return f, true
}

// pow10 is the powers of ten a float64 holds exactly.
var pow10 = [...]float64{1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10,
	1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22}

func floatSyntax(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	switch w := s[i:]; {
	case strings.EqualFold(w, "inf"), strings.EqualFold(w, "infinity"), strings.EqualFold(w, "nan"):
		return true
	}
	digits := 0
	for i < len(s) && isDigit(s[i]) {
		i, digits = i+1, digits+1
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isDigit(s[i]) {
			i, digits = i+1, digits+1
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		exp := 0
		for i < len(s) && isDigit(s[i]) {
			i, exp = i+1, exp+1
		}
		if exp == 0 {
			return false
		}
	}
	return i == len(s)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
