package kernel

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Full case mapping (step 178), as Polars' to_uppercase and to_lowercase apply it.
//
// Go's strings.ToUpper maps a rune to a rune, so "ß" stayed "ß" where Polars, Rust
// and Python answer "SS", and ligatures and Greek with a subscript iota stayed one
// character. The lower case has two such mappings: İ, whose lower case is an i and a
// combining dot, and Σ, which is ς at the end of a word and σ elsewhere.
//
// Each walks the string once more only when it holds such a character; otherwise it
// is strings.ToUpper or strings.ToLower as before.

// fullUpper is s in upper case, every upperExpansions character expanded.
func fullUpper(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { _, ok := upperExpansions[r]; return ok }) {
		return strings.ToUpper(s)
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch x, ok := upperExpansions[r]; {
		case r == utf8.RuneError && size == 1:
			b.WriteByte(s[i]) // not UTF-8: kept as it is
		case ok:
			b.WriteString(x)
		default:
			b.WriteRune(unicode.ToUpper(r))
		}
		i += size
	}
	return b.String()
}

// fullLower is s in lower case, with İ as i and a combining dot above, and Σ as ς
// where it ends a word: Unicode's Final_Sigma, a cased letter before it and none
// after, either side looking past the case-ignorable characters between.
func fullLower(s string) string {
	if !strings.ContainsAny(s, "İΣ") {
		return strings.ToLower(s)
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteByte(s[i])
		case r == 'İ':
			b.WriteString("i̇")
		case r == 'Σ' && casedBefore(s[:i]) && !casedAfter(s[i+size:]):
			b.WriteRune('ς')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
		i += size
	}
	return b.String()
}

// casedBefore reports whether s ends with a cased letter, past any case-ignorable
// characters after it.
func casedBefore(s string) bool {
	for s != "" {
		r, size := utf8.DecodeLastRuneInString(s)
		if !caseIgnorable(r) {
			return cased(r)
		}
		s = s[:len(s)-size]
	}
	return false
}

// casedAfter reports whether s begins with a cased letter, past any case-ignorable
// characters before it.
func casedAfter(s string) bool {
	for s != "" {
		r, size := utf8.DecodeRuneInString(s)
		if !caseIgnorable(r) {
			return cased(r)
		}
		s = s[size:]
	}
	return false
}

// cased is Unicode's Cased property: a letter with a case, or one of the few others
// that are lower or upper case.
func cased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

// caseIgnorable is Unicode's Case_Ignorable property: marks, format characters,
// modifier letters and symbols, and the apostrophes, periods and colons a word may
// hold.
func caseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '·', '·', '՟', '״', '‘', '’', '․',
		'‧', '︓', '﹒', '﹕', '＇', '．', '：':
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}
