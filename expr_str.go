package ursus

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
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
	nodes = append(nodes, s.e.node())
	for _, a := range args {
		nodes = append(nodes, litNode(a))
	}
	return wrap(&expr.Call{Fn: fn, Args: nodes})
}

// each is a call of a per-row pattern or value (step 255): the receiver, then args.
func (s StrExpr) each(fn expr.CallFn, args ...expr.Node) Expr {
	return wrap(&expr.Call{Fn: fn, Args: append([]expr.Node{s.e.node()}, args...)})
}

// Contains reports whether each value contains pattern.
//
// pattern is a string, or an expression, a pattern a row: a regex compiled once for
// each distinct pattern in a batch, and an invalid one an error naming its row. A
// null pattern gives null. The same holds for StartsWith, EndsWith, StripPrefix,
// StripSuffix and Split, and for Replace's and ReplaceAll's value (step 255).
func (s StrExpr) Contains[P string | Expr](pattern P, literal bool) Expr {
	if p, ok := any(pattern).(Expr); ok {
		return s.each(expr.FnStrContainsExpr, p.node(), litNode(literal))
	}
	return s.call(expr.FnStrContains, any(pattern).(string), literal)
}

// StartsWith and EndsWith are always literal — a prefix match against a regex is
// not a well-defined thing to ask for.
func (s StrExpr) StartsWith[P string | Expr](prefix P) Expr {
	if p, ok := any(prefix).(Expr); ok {
		return s.each(expr.FnStrStartsWithExpr, p.node())
	}
	return s.call(expr.FnStrStartsWith, any(prefix).(string))
}

func (s StrExpr) EndsWith[P string | Expr](suffix P) Expr {
	if p, ok := any(suffix).(Expr); ok {
		return s.each(expr.FnStrEndsWithExpr, p.node())
	}
	return s.call(expr.FnStrEndsWith, any(suffix).(string))
}

// Find returns the byte offset of the first match, or NULL when there is none.
//
// Null rather than -1: a sentinel index would compare and do arithmetic like a real
// position, so `find(x) < 5` would be true for "not found".
func (s StrExpr) Find(pattern string, literal bool) Expr {
	return s.call(expr.FnStrFind, pattern, literal)
}

// CountMatches counts non-overlapping occurrences. An empty pattern, literal or
// regex, matches at every rune boundary: len_chars + 1 times.
func (s StrExpr) CountMatches(pattern string, literal bool) Expr {
	return s.call(expr.FnStrCountMatches, pattern, literal)
}

// Extract returns capture group n of the first match, or NULL if there is none.
// Group 0 is the whole match. Regex only: extracting a literal is just Find.
func (s StrExpr) Extract(pattern string, group int) Expr {
	return s.call(expr.FnStrExtract, pattern, int64(group))
}

// Replace substitutes the FIRST match; ReplaceAll substitutes every match.
//
// For a regex, the replacement expands $1, ${1} and ${name} in both, as Go's
// regexp.Expand does — so $1x names a group called "1x", and ${1}x is group 1 then
// an x. A literal pattern's replacement is literal, $ and all.
//
// value may be an expression, a value a row, which is inserted as it is written:
// $1 is not expanded, as Polars does not expand it. A null value gives null, where
// Polars keeps the string as it was. The pattern stays a constant, as Polars'
// does.
func (s StrExpr) Replace[V string | Expr](pattern string, value V, literal bool) Expr {
	if v, ok := any(value).(Expr); ok {
		return s.each(expr.FnStrReplaceExpr, litNode(pattern), v.node(), litNode(literal))
	}
	return s.call(expr.FnStrReplace, pattern, any(value).(string), literal)
}

func (s StrExpr) ReplaceAll[V string | Expr](pattern string, value V, literal bool) Expr {
	if v, ok := any(value).(Expr); ok {
		return s.each(expr.FnStrReplaceAllExpr, litNode(pattern), v.node(), litNode(literal))
	}
	return s.call(expr.FnStrReplaceAll, pattern, any(value).(string), literal)
}

func (s StrExpr) ToLower() Expr { return s.call(expr.FnStrToLower) }
func (s StrExpr) ToUpper() Expr { return s.call(expr.FnStrToUpper) }

// LenBytes counts bytes; LenChars counts runes. They differ on any non-ASCII
// input, and which one is wanted is not guessable — so there is no `Len`.
func (s StrExpr) LenBytes() Expr { return s.call(expr.FnStrLenBytes) }
func (s StrExpr) LenChars() Expr { return s.call(expr.FnStrLenChars) }

// Slice takes length runes from offset, which may be negative to count from the
// end. Runes rather than bytes, so it can never split a character in half. The
// window is clipped as Polars clips it: a start before the first rune cuts the
// length by as much, so (-7, 2) of "012345" is "0" (step 291).
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

func (s StrExpr) StripPrefix[P string | Expr](prefix P) Expr {
	if p, ok := any(prefix).(Expr); ok {
		return s.each(expr.FnStrStripPrefixExpr, p.node())
	}
	return s.call(expr.FnStrStripPrefix, any(prefix).(string))
}

func (s StrExpr) StripSuffix[P string | Expr](suffix P) Expr {
	if p, ok := any(suffix).(Expr); ok {
		return s.each(expr.FnStrStripSuffixExpr, p.node())
	}
	return s.call(expr.FnStrStripSuffix, any(suffix).(string))
}

// Reverse reverses by RUNE, not by byte.
func (s StrExpr) Reverse() Expr { return s.call(expr.FnStrReverse) }

// Split splits each value on a literal separator, producing a List(String).
//
//	lf.WithColumns(ursus.Col("tags").Str().Split(",").Alias("tag")).
//	    Explode("tag")
//
// That two-stage form is the canonical use, and it is two stages because Explode
// changes the frame's height, which an expression may not.
//
// # Three cases that are not the same
//
//	"a,b"   ->  ["a", "b"]
//	""      ->  [""]      one empty element, matching strings.Split
//	NULL    ->  NULL      a null list, not an empty one
//
// The separator is LITERAL. A regex split would be a different function, and
// splitting on "." meaning "any character" is the mistake it would invite.
func (s StrExpr) Split[P string | Expr](by P) Expr {
	if p, ok := any(by).(Expr); ok {
		return s.each(expr.FnStrSplitExpr, p.node())
	}
	return s.call(expr.FnStrSplit, any(by).(string))
}

// SplitN is the first n parts of each value split on by, a Struct of fields field_0
// to field_{n-1}, the last holding the rest, separators and all: Polars' splitn. A
// field is null where there are fewer parts, and a null value's fields are null.
// Before step 270 it was a List; Split(by) is that, and SplitExact drops the rest.
func (s StrExpr) SplitN(by string, n int) Expr {
	return s.call(expr.FnStrSplitN, by, int64(n))
}

// SplitExact is the first n+1 parts of each value split on by, a Struct of fields
// field_0 to field_n, what follows them dropped: Polars' split_exact. With inclusive
// each part keeps the separator after it.
func (s StrExpr) SplitExact(by string, n int, inclusive bool) Expr {
	if n < 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "str.split_exact", "split_exact takes %d", n)})
	}
	return s.call(expr.FnStrSplitExact, by, int64(n), inclusive)
}

// SplitInclusive is Split whose parts keep the separator after them, Polars'
// split(inclusive=True): "a,b" is ["a,", "b"], and an empty value no part.
func (s StrExpr) SplitInclusive(by string) Expr { return s.call(expr.FnStrSplitInclusive, by) }

// ContainsAny reports whether each value contains one of patterns, literally:
// Polars' contains_any. With asciiCaseInsensitive an ASCII letter matches its other
// case; other letters keep theirs, as Polars' do.
func (s StrExpr) ContainsAny(patterns []string, asciiCaseInsensitive bool) Expr {
	return s.call(expr.FnStrContainsAny, patterns, asciiCaseInsensitive)
}

// ReplaceMany replaces each of patterns with the replacement at its place, or with
// one replacement for all, literally: Polars' replace_many. Matches do not overlap.
// By default the match that ends first is taken, and of two ending together the one
// that starts first, as Polars' default is; leftmost takes the match that starts
// first, and of two starting together the pattern listed first.
func (s StrExpr) ReplaceMany(patterns, replaceWith []string, asciiCaseInsensitive, leftmost bool) Expr {
	if len(replaceWith) != 1 && len(replaceWith) != len(patterns) {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "str.replace_many",
			"replace_many has %d patterns and %d replacements", len(patterns), len(replaceWith)).
			Hint("one replacement for every pattern, or one for all")})
	}
	return s.call(expr.FnStrReplaceMany, patterns, replaceWith, asciiCaseInsensitive, false, leftmost)
}

// ExtractMany is each value's matches of patterns, a List(String), and FindMany
// where they start, in bytes, a List(UInt32): Polars' extract_many and find_many.
// overlapping finds every match, ordered by where it ends; it takes the default
// matching, not leftmost, as Polars' does.
func (s StrExpr) ExtractMany(patterns []string, asciiCaseInsensitive, overlapping, leftmost bool) Expr {
	return s.many(expr.FnStrExtractMany, patterns, asciiCaseInsensitive, overlapping, leftmost)
}

func (s StrExpr) FindMany(patterns []string, asciiCaseInsensitive, overlapping, leftmost bool) Expr {
	return s.many(expr.FnStrFindMany, patterns, asciiCaseInsensitive, overlapping, leftmost)
}

func (s StrExpr) many(fn expr.CallFn, patterns []string, fold, overlapping, leftmost bool) Expr {
	if overlapping && leftmost {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, fn.String(),
			"%s finds overlapping matches by the default matching, not leftmost", fn)})
	}
	return s.call(fn, patterns, fold, overlapping, leftmost)
}

// ExtractGroups is the first match of a regex's capture groups, a Struct of String
// fields, each named after its group or numbered where it has none, all null where
// nothing matches: Polars' extract_groups.
func (s StrExpr) ExtractGroups(pattern string) Expr { return s.call(expr.FnStrExtractGroups, pattern) }

// ToTitlecase upper-cases each letter that follows a character that is not a letter,
// and lower-cases the rest: Polars' to_titlecase, "3rd place" "3Rd Place".
func (s StrExpr) ToTitlecase() Expr { return s.call(expr.FnStrToTitlecase) }

// Encoding is a transfer encoding for Encode and Decode.
type Encoding string

const (
	Hex    Encoding = "hex"
	Base64 Encoding = "base64"
)

// Encode is each value's bytes as lowercase hex or padded base64: Polars' encode
// (step 271).
func (s StrExpr) Encode(enc Encoding) Expr {
	if enc != Hex && enc != Base64 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "str.encode", "%q is not an encoding", enc).Hint("Hex or Base64")})
	}
	return s.call(expr.FnStrEncode, string(enc))
}

// Decode is each value's hex or base64 as Binary: Polars' decode. Text that is not of
// the encoding is refused with strict, naming its row, and null without it; base64
// must be padded, as Polars has it.
func (s StrExpr) Decode(enc Encoding, strict bool) Expr {
	if enc != Hex && enc != Base64 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "str.decode", "%q is not an encoding", enc).Hint("Hex or Base64")})
	}
	return s.call(expr.FnStrDecode, string(enc), strict)
}

// ToIntegerWith reads each value as an integer in base, a constant or an expression,
// 2 to 36, as dt: Polars' to_integer(base, dtype, strict). A sign is read and nothing
// else but digits: no space, 0x or underscore. A value that does not read, or does not
// fit dt, is refused with strict and null without it.
func (s StrExpr) ToIntegerWith[B int | Expr](base B, dt DataType, strict bool) Expr {
	if !dt.IsInteger() {
		return wrap(&expr.Err{E: uerr.New(uerr.KindType, "str.to_integer", "to_integer reads integers, not %s", dt)})
	}
	var b expr.Node
	if x, ok := any(base).(Expr); ok {
		b = x.node()
	} else {
		b = litNode(int64(any(base).(int)))
	}
	fn, wide := expr.FnStrParseInt, Int64
	if dt.IsUnsignedInteger() {
		fn, wide = expr.FnStrParseUint, Uint64
	}
	e := wrap(&expr.Call{Fn: fn, Args: []expr.Node{s.e.node(), b, litNode(strict)}})
	switch {
	case dt == wide:
		return e
	case strict:
		return e.Cast(dt)
	}
	return e.CastLossy(dt)
}

// ReplaceN replaces the first n matches of pattern, all where n is negative: Polars'
// replace(n=). Polars takes n > 1 for a literal pattern only; here a regex takes it
// too, each replacement expanding $1 as Replace's does.
func (s StrExpr) ReplaceN(pattern, value string, literal bool, n int) Expr {
	return s.call(expr.FnStrReplace, pattern, value, int64(n), literal)
}

// JsonPathMatch is each value's JSON at path, as text: Polars' json_path_match. The
// path is $ then .key, ['key'], [n] (from the end where negative) and ..key, the
// first key of that name at any depth. A string is unquoted and any other value its
// compact JSON, its keys in their order; JSON null, a path that matches nothing and
// text that is not JSON are null.
func (s StrExpr) JsonPathMatch(path string) Expr { return s.call(expr.FnStrJsonPathMatch, path) }

// JsonDecode reads each value's JSON as dt, of Booleans, numbers, Strings, and Lists
// and Structs of them: Polars' json_decode with its dtype, which ursus does not infer.
// A Struct's missing fields are null and its extra ones skipped; a float read as an
// integer is truncated, as Polars casts it. Text that is not JSON, or JSON of another
// shape than dt, is refused, naming its row.
func (s StrExpr) JsonDecode(dt DataType) Expr {
	return wrap(&expr.Call{Fn: expr.FnStrJsonDecode, Args: []expr.Node{s.e.node(), &expr.Lit{DT: dt}}})
}

// UnicodeForm is a Unicode normal form for Normalize.
type UnicodeForm string

const (
	NFC  UnicodeForm = "NFC"
	NFD  UnicodeForm = "NFD"
	NFKC UnicodeForm = "NFKC"
	NFKD UnicodeForm = "NFKD"
)

// Normalize puts each value in a Unicode normal form: Polars' normalize, NFC its
// default.
func (s StrExpr) Normalize(form UnicodeForm) Expr {
	switch form {
	case NFC, NFD, NFKC, NFKD:
		return s.call(expr.FnStrNormalize, string(form))
	}
	return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "str.normalize", "%q is not a Unicode normal form", form).
		Hint("NFC, NFD, NFKC or NFKD")})
}

// ExtractAll returns every non-overlapping match of a regex as a List(String).
//
// Regex only, for the reason Extract is: extracting every literal occurrence of a
// pattern returns that many copies of the pattern.
func (s StrExpr) ExtractAll(pattern string) Expr {
	return s.call(expr.FnStrExtractAll, pattern)
}

// PadStart and PadEnd pad each value to width, counted in RUNES, using the first
// rune of fill. A value already that wide is returned unchanged — padding never
// truncates.
func (s StrExpr) PadStart(width int, fill string) Expr {
	return s.call(expr.FnStrPadStart, int64(width), fill)
}

func (s StrExpr) PadEnd(width int, fill string) Expr {
	return s.call(expr.FnStrPadEnd, int64(width), fill)
}

// ZFill left-pads with zeros to width, keeping any leading sign in front of them:
// -5 at width 4 is "-005", not "0-05". That is what makes it different from
// PadStart with a '0', and it is the reason it is a separate function.
func (s StrExpr) ZFill(width int) Expr {
	return s.call(expr.FnStrZFill, int64(width))
}

// StripCharsStart and StripCharsEnd are the one-sided forms of StripChars: they
// remove any leading (or trailing) character in the cutset. An empty cutset means
// Unicode whitespace, as it does for StripChars.
func (s StrExpr) StripCharsStart(chars string) Expr {
	return s.call(expr.FnStrStripCharsStart, chars)
}

func (s StrExpr) StripCharsEnd(chars string) Expr {
	return s.call(expr.FnStrStripCharsEnd, chars)
}

// EscapeRegex quotes every regex metacharacter, so the result matches itself
// literally when used as a pattern.
func (s StrExpr) EscapeRegex() Expr {
	return s.call(expr.FnStrEscapeRegex)
}

// ToInteger parses an integer, an ordinary cast: a value that does not parse is
// null, as is an integer past Int64's range. It parses exactly at 64 bits:
// "9007199254740993" is itself, not the float64 beside it.
func (s StrExpr) ToInteger() Expr { return s.e.CastLossy(dtype.Int64) }

// ToDate parses a date as Polars' str.to_date() with no format reads one (step 356):
// by Polars' patterns, 2024-01-02, 2024/1/2, 02.01.2024 and 02/01/2024, the year
// first or the day, never the month; a value that does not parse is null. Each value
// is read by its own pattern, where Polars infers one from the first. Strptime takes
// a format, and strictness.
func (s StrExpr) ToDate() Expr { return s.Strptime(dtype.Date, "", false) }

// ToTime parses a time of day, a Time of nanoseconds, as Polars' str.to_time (step
// 325): by format, as Strptime reads one, or, with none, by Polars' patterns, "10:30",
// "3:04:05" and "10:30:15.123456789" alike (step 356), each value by its own where
// Polars infers one format from the first. Strict, a value that does not parse is an
// error; otherwise it is null.
func (s StrExpr) ToTime(format string, strict bool) Expr {
	return s.Strptime(dtype.Time(dtype.Nano), format, strict)
}

// ToDecimal parses a number into a Decimal(38, scale), as Polars' str.to_decimal
// (step 325): rounded half away from zero to the scale, an exponent read, 1e3 a
// thousand; text that is no number, or past 38 digits, is null.
func (s StrExpr) ToDecimal(scale int) Expr {
	if scale < 0 || scale > dtype.MaxDecimalPrecision {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "str.to_decimal",
			"a Decimal's scale is 0 to %d, not %d", dtype.MaxDecimalPrecision, scale)})
	}
	return s.e.CastLossy(dtype.Decimal(dtype.MaxDecimalPrecision, uint8(scale)))
}

// ToDatetime parses an instant as Polars' str.to_datetime() with no format reads one
// (step 356): by Polars' patterns, a date as ToDate's and a time of day after a T or
// a space, or its midnight. An offset, Z, +09:00 or +0900, makes it the instant; one
// without is read on tz's wall clock, as Polars reads it, or kept naive where tz is
// empty. It was a cast, which took such a value as UTC's. A value that does not parse,
// or a wall clock tz skips or reads twice, is null; Strptime takes a format, and
// strictness.
func (s StrExpr) ToDatetime(unit dtype.TimeUnit, tz string) Expr {
	return s.Strptime(dtype.Datetime(unit, tz), "", false)
}

// Strptime parses each string with a strftime-style format into dt, a Date,
// Datetime or Time — Polars' str.strptime.
//
//	Col("when").Str().Strptime(ursus.Date, "%d/%m/%Y", true)
//	Col("ts").Str().Strptime(ursus.Datetime(ursus.Micro, "UTC"), "%Y-%m-%d %H:%M:%S%.f %z", true)
//
// The directives are strftime's: %Y %m %d %H %M %S, %.f for a fraction, %z for an
// offset, %b and %B for month names, %j, %p, and the rest (internal/strftime lists
// them). A format that cannot produce dt is refused while the query is planned: a
// Date needs a date, a Time has none, and only a Datetime holds an offset.
//
// Strict, a value that does not match — or names a day that does not exist, such as
// 30 February — is an error naming it. Otherwise it becomes null, as ToDate's do.
//
// An empty format is Polars' format=None: each value is read by Polars' patterns, as
// ToDate, ToDatetime and ToTime read it (step 356).
//
// A Datetime takes its instant from the offset when the format reads one;
// otherwise the wall clock is read in dt's zone, or kept as it is when dt is naive.
// Digits finer than the unit are dropped.
func (s StrExpr) Strptime(dt dtype.DataType, format string, strict bool) Expr {
	switch dt.ID() {
	case dtype.TypeDate:
		return s.call(expr.FnStrToDateFmt, format, strict)
	case dtype.TypeDatetime:
		return s.call(expr.FnStrToDatetimeFmt, format, strict, int64(dt.TimeUnit()), dt.TimeZone())
	case dtype.TypeTime:
		return s.call(expr.FnStrToTimeFmt, format, strict, int64(dt.TimeUnit()))
	}
	return wrap(&expr.Err{E: uerr.New(uerr.KindType, "str.strptime",
		"strptime parses to a Date, Datetime or Time, not %s", dt)})
}

// Join joins a column's strings into one, with sep between each two and nulls
// skipped: Polars' str.join. It is an aggregate, so GroupBy().Agg gives one row for
// the frame, and GroupBy(keys).Agg one per group:
//
//	df.GroupBy(Col("team")).Agg(Col("name").Str().Join(", "))
//
// It is Implode, then List().Join, and its order is the rows': a group-by's rows
// arrive in the input's order within each group.
func (s StrExpr) Join(sep string) Expr { return s.e.Implode().List().Join(sep) }
