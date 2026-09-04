package kernel

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// String kernels.
//
// # No SIMD, and that is not an oversight
//
// Strings are outside the Numeric constraint entirely — they are offsets plus a
// character buffer, not a flat array of a fixed-width type — so none of the
// scalar/SIMD/differential machinery in dispatch_simd_amd64.go applies. There is
// no scalar twin to differ from, because there is only one implementation.
//
// What replaces that discipline here is a differential test against the standard
// library: every function below is checked against strings.Contains, strings.
// ToLower and friends over a corpus including empty strings, multi-byte UTF-8 and
// invalid UTF-8. That is the same argument the hand-rolled CSV scanner makes.
//
// # Copying is mandatory
//
// StringAccessor.Get is zero-copy and aliases the column's character buffer, so
// any result derived from it must be copied before it escapes. Go's string
// immutability does most of that for free; the cases to watch are slices of the
// accessor's return value, which alias the same bytes.

// StrCall applies a string function to a column.
//
// re is the compiled pattern for the regex forms, or nil for literal matching. It
// is compiled ONCE per expression node by the evaluator, never per batch and never
// per row — RE2 compilation is orders of magnitude more expensive than the match.
func StrCall(fn expr.CallFn, name string, out dtype.DataType,
	c *data.Column, args []any, re *regexp.Regexp) (*data.Column, error) {

	acc := c.Strings()
	n := c.Len()
	valid := c.Validity()

	// Predicates and counts write fixed-width output; the rest build strings.
	switch fn {
	case expr.FnStrContains, expr.FnStrStartsWith, expr.FnStrEndsWith:
		bits := bitmap.NewBuilder(n)
		pat, _ := argString(args, 0)
		for i := range n {
			if !valid.Get(i) {
				bits.Append(false)
				continue
			}
			s := acc.Get(i)
			switch fn {
			case expr.FnStrContains:
				if re != nil {
					bits.Append(re.MatchString(s))
				} else {
					bits.Append(strings.Contains(s, pat))
				}
			case expr.FnStrStartsWith:
				bits.Append(strings.HasPrefix(s, pat))
			default:
				bits.Append(strings.HasSuffix(s, pat))
			}
		}
		return data.NewBool(name, bits.Finish(), valid), nil

	case expr.FnStrLenBytes, expr.FnStrLenChars, expr.FnStrFind, expr.FnStrCountMatches:
		vals := make([]uint32, n)
		ok := bitmap.NewBuilder(n)
		pat, _ := argString(args, 0)
		for i := range n {
			if !valid.Get(i) {
				ok.Append(false)
				continue
			}
			s := acc.Get(i)
			present := true
			switch fn {
			case expr.FnStrLenBytes:
				vals[i] = uint32(len(s))
			case expr.FnStrLenChars:
				// The whole reason LenChars exists: len(s) counts bytes, and on any
				// non-ASCII input the two answers differ.
				vals[i] = uint32(utf8.RuneCountInString(s))
			case expr.FnStrFind:
				idx := -1
				if re != nil {
					if loc := re.FindStringIndex(s); loc != nil {
						idx = loc[0]
					}
				} else {
					idx = strings.Index(s, pat)
				}
				if idx < 0 {
					// No match is NULL, not -1: a sentinel index would compare and
					// arithmetic as a real position.
					present = false
				} else {
					vals[i] = uint32(idx)
				}
			default: // CountMatches
				if re != nil {
					vals[i] = uint32(len(re.FindAllStringIndex(s, -1)))
				} else if pat == "" {
					present = false
				} else {
					vals[i] = uint32(strings.Count(s, pat))
				}
			}
			ok.Append(present)
		}
		return data.NewFixed(name, out, vals, ok.Finish()), nil
	}

	// String-producing functions.
	res := make([]string, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		if !valid.Get(i) {
			ok.Append(false)
			continue
		}
		s := acc.Get(i)
		v, present := applyStr(fn, s, args, re)
		res[i] = v
		ok.Append(present)
	}
	return data.NewString(name, res, ok.Finish()), nil
}

func applyStr(fn expr.CallFn, s string, args []any, re *regexp.Regexp) (string, bool) {
	switch fn {
	case expr.FnStrToLower:
		return strings.ToLower(s), true
	case expr.FnStrToUpper:
		return strings.ToUpper(s), true

	case expr.FnStrReverse:
		// By RUNE, not by byte: reversing bytes would corrupt every multi-byte
		// character in the string.
		r := []rune(s)
		for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
			r[i], r[j] = r[j], r[i]
		}
		return string(r), true

	case expr.FnStrReplace, expr.FnStrReplaceAll:
		pat, _ := argString(args, 0)
		rep, _ := argString(args, 1)
		count := 1
		if fn == expr.FnStrReplaceAll {
			count = -1
		}
		if re != nil {
			if count == -1 {
				return re.ReplaceAllString(s, rep), true
			}
			done := false
			return re.ReplaceAllStringFunc(s, func(m string) string {
				if done {
					return m
				}
				done = true
				return rep
			}), true
		}
		return strings.Replace(s, pat, rep, count), true

	case expr.FnStrExtract:
		if re == nil {
			return "", false
		}
		group := 0
		if g, ok := argInt(args, 1); ok {
			group = int(g)
		}
		m := re.FindStringSubmatch(s)
		if m == nil || group >= len(m) || group < 0 {
			return "", false
		}
		return m[group], true

	case expr.FnStrStripChars:
		cut, ok := argString(args, 0)
		if !ok || cut == "" {
			return strings.TrimSpace(s), true
		}
		return strings.Trim(s, cut), true

	case expr.FnStrStripPrefix:
		p, _ := argString(args, 0)
		return strings.TrimPrefix(s, p), true
	case expr.FnStrStripSuffix:
		p, _ := argString(args, 0)
		return strings.TrimSuffix(s, p), true

	case expr.FnStrSlice:
		// Offsets are in RUNES and may be negative, counting from the end — the
		// convention every dataframe library uses, and the one that does not split
		// a multi-byte character in half.
		r := []rune(s)
		off, _ := argInt(args, 0)
		length, hasLen := argInt(args, 1)
		start := int(off)
		if start < 0 {
			start += len(r)
		}
		start = max(0, min(start, len(r)))
		end := len(r)
		if hasLen {
			if length < 0 {
				return "", true
			}
			end = min(start+int(length), len(r))
		}
		return string(r[start:end]), true

	default:
		return "", false
	}
}

// CompilePattern builds the regex for a call, or returns nil for literal matching.
//
// Only the functions that accept a pattern get one, and only when the call asked
// for regex. Extract is the exception: it is regex-only, because "extract the
// first literal occurrence" is just Find.
func CompilePattern(fn expr.CallFn, args []any) (*regexp.Regexp, error) {
	needsRegex := false
	switch fn {
	case expr.FnStrExtract:
		needsRegex = true
	case expr.FnStrContains, expr.FnStrFind, expr.FnStrCountMatches,
		expr.FnStrReplace, expr.FnStrReplaceAll:
		// The `literal` flag is the LAST argument for these, and literal is the
		// default: RE2 has no JIT, so the common case must not pay for it.
		if lit, ok := argBool(args, len(args)-1); ok && !lit {
			needsRegex = true
		}
	}
	if !needsRegex {
		return nil, nil
	}
	pat, ok := argString(args, 0)
	if !ok {
		return nil, uerr.New(uerr.KindValue, "str", "%s needs a pattern", fn)
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindValue, "str",
			"invalid regular expression %q", pat).
			Hint("pass literal=true to match the pattern as plain text")
	}
	return re, nil
}

func argString(args []any, i int) (string, bool) {
	if i < 0 || i >= len(args) {
		return "", false
	}
	s, ok := args[i].(string)
	return s, ok
}

func argInt(args []any, i int) (int64, bool) {
	if i < 0 || i >= len(args) {
		return 0, false
	}
	switch v := args[i].(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

func argBool(args []any, i int) (bool, bool) {
	if i < 0 || i >= len(args) {
		return false, false
	}
	b, ok := args[i].(bool)
	return b, ok
}
