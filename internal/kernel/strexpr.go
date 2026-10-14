package kernel

import (
	"regexp"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// String functions whose pattern is a column (step 255, E5 of v0.6.5-scope.md):
// contains, starts_with, ends_with, strip_prefix, strip_suffix and split with a
// pattern a row, and replace and replace_all with a value a row. Polars takes the
// same, and refuses a per-row pattern for replace.
//
// Each operand is a column of the batch's height or a literal one row long, laid
// out as the constant form's arguments are after the receiver, so each row answers
// as the constant form would with that row's arguments. A null operand gives null.

// strPatterns compiles each distinct pattern once a batch, and names a bad one.
type strPatterns struct {
	seen map[string]*regexp.Regexp
}

func (p *strPatterns) get(pat string, row int) (*regexp.Regexp, error) {
	if re, ok := p.seen[pat]; ok {
		return re, nil
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindValue, "str",
			"invalid regular expression %q in row %d", pat, row).
			Hint("pass literal=true to match the patterns as plain text")
	}
	// A column of distinct patterns would hold every one: past a few hundred the
	// cache starts over.
	if len(p.seen) >= 256 {
		clear(p.seen)
	}
	p.seen[pat] = re
	return re, nil
}

// strEach is a string function of a per-row pattern or value, its answer of type
// out.
func strEach(fn expr.CallFn, name string, out dtype.DataType, ops []*data.Column,
	n int) (*data.Column, error) {

	// An operand of type Null, a literal null, is null in every row.
	for _, c := range ops {
		if c.DType().IsNull() {
			return NullColumn(name, out, n)
		}
	}
	s := ops[0]
	str := func(c *data.Column, i int) (string, bool) {
		r := rowOf(c, i)
		if !c.IsValid(r) {
			return "", false
		}
		return c.Strings().Get(r), true
	}
	flag := func(c *data.Column) bool { return c.Bools().Get(0) }
	pats := &strPatterns{seen: map[string]*regexp.Regexp{}}

	switch fn {
	case expr.FnStrContainsExpr, expr.FnStrStartsWithExpr, expr.FnStrEndsWithExpr:
		literal := fn != expr.FnStrContainsExpr || flag(ops[2])
		bits := bitmap.NewBuilder(n)
		valid := bitmap.NewBuilder(n)
		for i := range n {
			v, vok := str(s, i)
			p, pok := str(ops[1], i)
			valid.Append(vok && pok)
			if !vok || !pok {
				bits.Append(false)
				continue
			}
			switch {
			case fn == expr.FnStrStartsWithExpr:
				bits.Append(strings.HasPrefix(v, p))
			case fn == expr.FnStrEndsWithExpr:
				bits.Append(strings.HasSuffix(v, p))
			case literal:
				bits.Append(strings.Contains(v, p))
			default:
				re, err := pats.get(p, i)
				if err != nil {
					return nil, err
				}
				bits.Append(re.MatchString(v))
			}
		}
		return data.NewBool(name, bits.Finish(), valid.Finish()), nil

	case expr.FnStrStripPrefixExpr, expr.FnStrStripSuffixExpr:
		res := make([]string, n)
		valid := bitmap.NewBuilder(n)
		for i := range n {
			v, vok := str(s, i)
			p, pok := str(ops[1], i)
			valid.Append(vok && pok)
			switch {
			case !vok || !pok:
			case fn == expr.FnStrStripPrefixExpr:
				res[i] = strings.TrimPrefix(v, p)
			default:
				res[i] = strings.TrimSuffix(v, p)
			}
		}
		return data.NewString(name, res, valid.Finish()), nil

	case expr.FnStrSplitExpr:
		// As strToList: a null row is a null list, and the elements are never null.
		var parts []string
		offs := make([]int32, 1, n+1)
		valid := bitmap.NewBuilder(n)
		for i := range n {
			v, vok := str(s, i)
			p, pok := str(ops[1], i)
			if vok && pok {
				parts = append(parts, strings.Split(v, p)...)
			}
			offs = append(offs, int32(len(parts)))
			valid.Append(vok && pok)
		}
		child := data.NewString(name, parts, bitmap.AllSet(len(parts)))
		return data.NewList(name, offs, child, valid.Finish()), nil

	case expr.FnStrReplaceExpr, expr.FnStrReplaceAllExpr:
		// ops: the receiver, the pattern (a literal), the value a row, literal.
		pat, ok := str(ops[1], 0)
		if !ok || ops[1].Len() != 1 {
			return nil, uerr.Internalf("kernel: %s without a constant pattern", fn)
		}
		var re *regexp.Regexp
		if !flag(ops[3]) {
			var err error
			if re, err = pats.get(pat, 0); err != nil {
				return nil, err
			}
		}
		all := fn == expr.FnStrReplaceAllExpr
		res := make([]string, n)
		valid := bitmap.NewBuilder(n)
		for i := range n {
			v, vok := str(s, i)
			with, wok := str(ops[2], i)
			valid.Append(vok && wok)
			if !vok || !wok {
				continue
			}
			res[i] = replaceWith(v, pat, re, with, all)
		}
		return data.NewString(name, res, valid.Finish()), nil
	}
	return nil, uerr.Internalf("kernel: no string kernel for %s", fn)
}

// replaceWith is v with the first match of the pattern, or every one, replaced by
// with as it is written: a value a row is inserted without expanding $1, as Polars
// inserts it.
func replaceWith(v, pat string, re *regexp.Regexp, with string, all bool) string {
	switch {
	case re == nil && all:
		return strings.ReplaceAll(v, pat, with)
	case re == nil:
		return strings.Replace(v, pat, with, 1)
	case all:
		return re.ReplaceAllLiteralString(v, with)
	}
	m := re.FindStringIndex(v)
	if m == nil {
		return v
	}
	return v[:m[0]] + with + v[m[1]:]
}
