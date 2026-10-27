package dtype

import (
	"math/bits"
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
// [(e|E) [+-] digits], of at most 19 significant digits: they are read into a whole
// number, and NearestFloat finds the float, as strconv would. ok is false for anything
// else, and for a float NearestFloat does not settle, which ParseFloat reads as it did.
//
// Step 373 parsed a decimal in one pass where its value was Clinger's exact case, a
// whole number below 2^53 over a power of ten of at most 22. A float written in full,
// its 17 digits, is not: it was read three times, by this, the grammar and strconv,
// and is now once, its fraction eight digits at a time while the whole number has room
// for them (step 389). A whole part's eight at a time cost a short float more than
// it saved a long one.
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
		i++
		for sig <= 11 && i+8 <= len(s) {
			w := le64(s[i:])
			if !eightDigits(w) {
				break
			}
			mant, sig = addEight(mant, sig, w)
			i, exp, digits = i+8, exp-8, digits+8
		}
		for ; i < len(s) && isDigit(s[i]); i++ {
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
	if i != len(s) {
		return 0, false
	}
	if mant < 1<<53 && exp >= -22 && exp <= 22 { // NearestFloat's first case, inline
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
	return NearestFloat(mant, exp, neg)
}

// le64 is s's first eight bytes as a little-endian word, the first the lowest.
func le64(s string) uint64 {
	_ = s[7]
	return uint64(s[0]) | uint64(s[1])<<8 | uint64(s[2])<<16 | uint64(s[3])<<24 |
		uint64(s[4])<<32 | uint64(s[5])<<40 | uint64(s[6])<<48 | uint64(s[7])<<56
}

// eightDigits reports whether each byte of w is an ASCII digit: its high nibble 3, and
// 3 still after adding 6 (Lemire's fast_float).
func eightDigits(w uint64) bool {
	return w&0xF0F0F0F0F0F0F0F0|(w+0x0606060606060606)&0xF0F0F0F0F0F0F0F0>>4 == 0x3333333333333333
}

// addEight appends w's eight digits to man, of nd significant digits: their value found
// in three multiplies (fast_float's), and nd counting from man's first digit that is
// not 0, as the loop a digit at a time counts it.
func addEight(man uint64, nd int, w uint64) (uint64, int) {
	d := w - 0x3030303030303030
	v := d*10 + d>>8
	v = ((v&0x000000FF000000FF)*0x000F424000000064 + (v>>16&0x000000FF000000FF)*0x0000271000000001) >> 32
	if man == 0 {
		return v, 8 - bits.TrailingZeros64(d)>>3
	}
	return man*100_000_000 + v, nd + 8
}

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
