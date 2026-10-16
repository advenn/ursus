package kernel

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
	"golang.org/x/text/unicode/norm"
)

// More of the string namespace (step 270, S of v0.6.6-scope.md): matching many
// patterns at once, splitting into a Struct, title case and Unicode normal forms.

// manyMatcher finds literal patterns in a string, as Polars' Aho–Corasick does, by
// its three semantics:
//
//   - standard, Polars' default: the match that ends first, and of two ending
//     together the one that starts first, then on from its end;
//   - leftmost: the match that starts first, and of two starting together the
//     pattern listed first, then on from its end;
//   - overlapping: every match, ordered by where it ends, then where it starts.
//
// It compares each pattern at each place, which is quick for the few patterns these
// functions take. fold compares ASCII letters without their case, as Polars'
// ascii_case_insensitive does; other letters keep theirs.
type manyMatcher struct {
	pats []string
	fold bool
}

func (m *manyMatcher) at(s string, i int, p string) bool {
	if i+len(p) > len(s) {
		return false
	}
	if !m.fold {
		return s[i:i+len(p)] == p
	}
	for k := range len(p) {
		a, b := s[i+k], p[k]
		if a != b && lowerASCII(a) != lowerASCII(b) {
			return false
		}
	}
	return true
}

func lowerASCII(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

// each calls emit with each match's start, end and pattern, in order.
func (m *manyMatcher) each(s string, leftmost, overlapping bool, emit func(st, en, p int) bool) {
	if overlapping {
		for en := 0; en <= len(s); en++ {
			for st := 0; st <= en; st++ {
				for p, pat := range m.pats {
					if en-st == len(pat) && m.at(s, st, pat) && !emit(st, en, p) {
						return
					}
				}
			}
		}
		return
	}
	pos := 0
	for pos <= len(s) {
		st, en, p := -1, -1, -1
		if leftmost {
		start:
			for i := pos; i <= len(s); i++ {
				for k, pat := range m.pats {
					if m.at(s, i, pat) {
						st, en, p = i, i+len(pat), k
						break start
					}
				}
			}
		} else {
		end:
			for e := pos; e <= len(s); e++ {
				for k, pat := range m.pats {
					if i := e - len(pat); i >= pos && m.at(s, i, pat) && (st < 0 || i < st) {
						st, en, p = i, e, k
					}
				}
				if p >= 0 {
					break end
				}
			}
		}
		if p < 0 || !emit(st, en, p) {
			return
		}
		pos = en
		if en == st {
			// An empty match: the next is past the character here, which stays.
			if pos == len(s) {
				return
			}
			_, size := utf8.DecodeRuneInString(s[pos:])
			pos += size
		}
	}
}

// strings of a literal list, a List literal's elements.
func stringsOf(v any) ([]string, bool) {
	lits, ok := v.([]*expr.Lit)
	if !ok {
		return nil, false
	}
	out := make([]string, len(lits))
	for i, l := range lits {
		s, ok := l.Value.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// strMany is contains_any, replace_many, extract_many and find_many. args are the
// patterns, then for replace_many the replacements, then ascii_case_insensitive,
// overlapping and leftmost as each takes them.
func strMany(fn expr.CallFn, name string, c *data.Column, args []any) (*data.Column, error) {
	pats, ok := stringsOf(args[0])
	if !ok {
		return nil, uerr.Internalf("kernel: %s without its patterns", fn)
	}
	rest := args[1:]
	var reps []string
	if fn == expr.FnStrReplaceMany {
		if reps, ok = stringsOf(args[1]); !ok {
			return nil, uerr.Internalf("kernel: %s without its replacements", fn)
		}
		rest = args[2:]
	}
	fold, _ := argBool(rest, 0)
	overlapping, _ := argBool(rest, 1)
	leftmost, _ := argBool(rest, 2)
	m := &manyMatcher{pats: pats, fold: fold}
	acc := c.Strings()
	n := c.Len()
	valid := c.Validity()
	switch fn {
	case expr.FnStrContainsAny:
		bits := bitmap.NewBuilder(n)
		for i := range n {
			found := false
			if valid.Get(i) {
				m.each(acc.Get(i), false, false, func(int, int, int) bool { found = true; return false })
			}
			bits.Append(found)
		}
		return data.NewBool(name, bits.Finish(), valid), nil
	case expr.FnStrReplaceMany:
		res := make([]string, n)
		for i := range n {
			if !valid.Get(i) {
				continue
			}
			s := acc.Get(i)
			var b strings.Builder
			last := 0
			m.each(s, leftmost, false, func(st, en, p int) bool {
				b.WriteString(s[last:st])
				if len(reps) == 1 {
					b.WriteString(reps[0])
				} else {
					b.WriteString(reps[p])
				}
				last = en
				return true
			})
			b.WriteString(s[last:])
			res[i] = b.String()
		}
		return data.NewString(name, res, valid), nil
	}
	// extract_many and find_many: a list a row.
	var words []string
	var at []uint32
	offs := make([]int32, 1, n+1)
	for i := range n {
		if valid.Get(i) {
			s := acc.Get(i)
			m.each(s, leftmost, overlapping, func(st, en, _ int) bool {
				if fn == expr.FnStrExtractMany {
					words = append(words, strings.Clone(s[st:en]))
				} else {
					at = append(at, uint32(st))
				}
				return true
			})
		}
		offs = append(offs, int32(len(words)+len(at)))
	}
	if fn == expr.FnStrExtractMany {
		return data.NewList(name, offs, data.NewString(name, words, bitmap.AllSet(len(words))), valid), nil
	}
	return data.NewList(name, offs, data.NewFixed(name, dtype.Uint32, at, bitmap.AllSet(len(at))), valid), nil
}

// strStruct is a string function whose answer is a Struct of out's fields:
// extract_groups, each capture group of the first match, all null where none
// matches; split_exact, the first n+1 parts, the rest dropped; and splitn, n parts,
// the last holding the rest. A field is null where there is no part for it, and a
// null string's fields are null, the Struct not, as Polars makes it.
func strStruct(fn expr.CallFn, name string, out dtype.DataType, c *data.Column, args []any,
	re *regexp.Regexp) (*data.Column, error) {

	fields := out.Fields()
	k := len(fields)
	acc := c.Strings()
	n := c.Len()
	valid := c.Validity()
	vals := make([][]string, k)
	oks := make([]*bitmap.Builder, k)
	for j := range k {
		vals[j] = make([]string, n)
		oks[j] = bitmap.NewBuilder(n)
	}
	set := func(j, i int, v string, ok bool) {
		if ok {
			vals[j][i] = strings.Clone(v)
		}
		oks[j].Append(ok)
	}
	by, _ := argString(args, 0)
	inclusive, _ := argBool(args, 2)
	for i := range n {
		if !valid.Get(i) {
			for j := range k {
				set(j, i, "", false)
			}
			continue
		}
		s := acc.Get(i)
		var parts []string
		switch fn {
		case expr.FnStrExtractGroups:
			m := re.FindStringSubmatchIndex(s)
			for j := range k {
				if m != nil && m[2*j+2] >= 0 {
					set(j, i, s[m[2*j+2]:m[2*j+3]], true)
				} else {
					set(j, i, "", false)
				}
			}
			continue
		case expr.FnStrSplitExact:
			// One part more than the fields, to drop what follows them. Polars splits
			// an empty string inclusively into no parts.
			switch {
			case inclusive && s == "":
			case inclusive:
				parts = strings.SplitAfterN(s, by, k+1)
			default:
				parts = strings.SplitN(s, by, k+1)
			}
		default: // splitn
			parts = strings.SplitN(s, by, k)
		}
		for j := range k {
			if j < len(parts) {
				set(j, i, parts[j], true)
			} else {
				set(j, i, "", false)
			}
		}
	}
	cols := make([]*data.Column, k)
	for j, f := range fields {
		cols[j] = data.NewString(f.Name, vals[j], oks[j].Finish())
	}
	return data.NewStruct(name, cols, bitmap.AllSet(n)), nil
}

// titlecase is s with each letter that follows a character not a letter upper case
// and every other letter lower case, as Polars' to_titlecase makes it: "3rd" is
// "3Rd".
func titlecase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevLetter := false
	for _, r := range s {
		letter := unicode.IsLetter(r)
		switch {
		case letter && !prevLetter:
			b.WriteRune(unicode.ToUpper(r))
		case letter:
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
		prevLetter = letter
	}
	return b.String()
}

// normalForm is a Unicode normal form by its name.
func normalForm(name string) (norm.Form, bool) {
	switch name {
	case "NFC":
		return norm.NFC, true
	case "NFD":
		return norm.NFD, true
	case "NFKC":
		return norm.NFKC, true
	case "NFKD":
		return norm.NFKD, true
	}
	return norm.NFC, false
}
