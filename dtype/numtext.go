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
// Polars, pandas and DuckDB read both as text. strconv still does the rounding.
func ParseFloat(s string, bits int) (float64, error) {
	if !floatSyntax(s) {
		return 0, &strconv.NumError{Func: "ParseFloat", Num: s, Err: strconv.ErrSyntax}
	}
	return strconv.ParseFloat(s, bits)
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
