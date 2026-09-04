package ursus

import (
	"ursus/dtype"
	"ursus/internal/expr"
)

// StrExpr is the string namespace: `Col("name").Str().ToLower()`.
//
// A small wrapper struct whose methods return Expr, so chaining continues
// naturally through it. This is the shape ursus-api.md §4.5 specifies for every
// namespace, and `.dt` follows it identically.
//
// # The literal flag
//
// Contains, Find, CountMatches, Replace and ReplaceAll take `literal bool`, and
// literal is what you want unless you know otherwise. Go's regexp is RE2: no
// backtracking, so no catastrophic blowup, but also no JIT — a plain substring
// test through it costs far more than strings.Contains. Passing literal=true keeps
// the common case on the fast path, which is why the flag is explicit rather than
// inferred from whether the pattern looks like a regex.
type StrExpr struct{ e Expr }

// Str opens the string namespace.
func (e Expr) Str() StrExpr { return StrExpr{e} }

func (s StrExpr) call(fn expr.CallFn, args ...any) Expr {
	nodes := make([]expr.Node, 0, len(args)+1)
	nodes = append(nodes, s.e.n)
	for _, a := range args {
		nodes = append(nodes, litNode(a))
	}
	return wrap(&expr.Call{Fn: fn, Args: nodes})
}

// Contains reports whether each value contains pattern.
func (s StrExpr) Contains(pattern string, literal bool) Expr {
	return s.call(expr.FnStrContains, pattern, literal)
}

// StartsWith and EndsWith are always literal — a prefix match against a regex is
// not a well-defined thing to ask for.
func (s StrExpr) StartsWith(prefix string) Expr {
	return s.call(expr.FnStrStartsWith, prefix)
}

func (s StrExpr) EndsWith(suffix string) Expr {
	return s.call(expr.FnStrEndsWith, suffix)
}

// Find returns the byte offset of the first match, or NULL when there is none.
//
// Null rather than -1: a sentinel index would compare and do arithmetic like a real
// position, so `find(x) < 5` would be true for "not found".
func (s StrExpr) Find(pattern string, literal bool) Expr {
	return s.call(expr.FnStrFind, pattern, literal)
}

// CountMatches counts non-overlapping occurrences.
func (s StrExpr) CountMatches(pattern string, literal bool) Expr {
	return s.call(expr.FnStrCountMatches, pattern, literal)
}

// Extract returns capture group n of the first match, or NULL if there is none.
// Group 0 is the whole match. Regex only: extracting a literal is just Find.
func (s StrExpr) Extract(pattern string, group int) Expr {
	return s.call(expr.FnStrExtract, pattern, int64(group))
}

// Replace substitutes the FIRST match; ReplaceAll substitutes every match.
func (s StrExpr) Replace(pattern, value string, literal bool) Expr {
	return s.call(expr.FnStrReplace, pattern, value, literal)
}

func (s StrExpr) ReplaceAll(pattern, value string, literal bool) Expr {
	return s.call(expr.FnStrReplaceAll, pattern, value, literal)
}

func (s StrExpr) ToLower() Expr { return s.call(expr.FnStrToLower) }
func (s StrExpr) ToUpper() Expr { return s.call(expr.FnStrToUpper) }

// LenBytes counts bytes; LenChars counts runes. They differ on any non-ASCII
// input, and which one is wanted is not guessable — so there is no `Len`.
func (s StrExpr) LenBytes() Expr { return s.call(expr.FnStrLenBytes) }
func (s StrExpr) LenChars() Expr { return s.call(expr.FnStrLenChars) }

// Slice takes length runes from offset, which may be negative to count from the
// end. Runes rather than bytes, so it can never split a character in half.
func (s StrExpr) Slice(offset, length int) Expr {
	return s.call(expr.FnStrSlice, int64(offset), int64(length))
}

// Head and Tail are Slice's common cases.
func (s StrExpr) Head(n int) Expr { return s.Slice(0, n) }
func (s StrExpr) Tail(n int) Expr { return s.Slice(-n, n) }

// StripChars trims any of the given characters from both ends. An empty set trims
// whitespace, matching Polars.
func (s StrExpr) StripChars(chars string) Expr {
	return s.call(expr.FnStrStripChars, chars)
}

func (s StrExpr) StripPrefix(prefix string) Expr {
	return s.call(expr.FnStrStripPrefix, prefix)
}

func (s StrExpr) StripSuffix(suffix string) Expr {
	return s.call(expr.FnStrStripSuffix, suffix)
}

// Reverse reverses by RUNE, not by byte.
func (s StrExpr) Reverse() Expr { return s.call(expr.FnStrReverse) }

// ToInteger, ToDate and ToDatetime parse. They are ordinary casts, which is why
// they are spelled as casts rather than as new kernels — and unparseable values
// become NULL rather than failing the query.
func (s StrExpr) ToInteger() Expr { return s.e.CastLossy(dtype.Int64) }
func (s StrExpr) ToDate() Expr    { return s.e.CastLossy(dtype.Date) }

func (s StrExpr) ToDatetime(unit dtype.TimeUnit, tz string) Expr {
	return s.e.CastLossy(dtype.Datetime(unit, tz))
}
