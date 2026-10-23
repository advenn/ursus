// Package selector picks a frame's columns by what they are, as Polars' selectors
// do: by type, by name, by place, and the unions, intersections, differences and
// complements of those (step 322). The set algebra is named, Union and Complement
// where Polars writes | and ~, so a selector's complement is never mistaken for an
// expression's negation.
//
//	lf.Select(selector.Numeric().Difference(selector.ByName("id")).Expr()) // every number but id
//	lf.WithColumns(selector.Float().Exclude("price").Expr().Round(2))      // each float but price, rounded
//
// A selector is resolved against the frame's schema when the query is planned, so
// one written selector serves any schema. Each answers in the schema's order but
// ByName and ByIndex, which answer in the order written, as Polars' do; a combined
// selector answers in the schema's order. A selector that selects nothing selects
// nothing, as Polars': no error, but where a name or a place is asked for that the
// frame lacks.
package selector

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/exprbridge"
)

// Selector is a choice of columns, resolved against a schema.
type Selector struct{ m expr.Matcher }

// Expr is the selector as an expression, a column each selected column, where an
// expression goes: Select, WithColumns, Agg, or a method of Expr's.
func (s Selector) Expr() ursus.Expr { return exprbridge.Wrap(&expr.Match{M: s.m}).(ursus.Expr) }

// String renders the selector, as Explain does.
func (s Selector) String() string { return s.m.String() }

// Union is the columns either selects, Polars' |.
func (s Selector) Union(o Selector) Selector { return set(expr.SetUnion, s, o) }

// Intersect is the columns both select, Polars' &.
func (s Selector) Intersect(o Selector) Selector { return set(expr.SetIntersect, s, o) }

// Difference is the columns s selects and o does not, Polars' -.
func (s Selector) Difference(o Selector) Selector { return set(expr.SetDifference, s, o) }

// SymmetricDifference is the columns one selects and the other does not, Polars' ^.
func (s Selector) SymmetricDifference(o Selector) Selector { return set(expr.SetXor, s, o) }

// Complement is every column s does not select, Polars' ~ of a selector.
func (s Selector) Complement() Selector { return Selector{&expr.NotMatcher{Inner: s.m}} }

// Exclude is the columns s selects but the named ones, Polars' exclude: a name it
// does not select is passed over.
func (s Selector) Exclude(names ...string) Selector {
	return Selector{&expr.ExcludeMatcher{Inner: s.m, Names: append([]string(nil), names...)}}
}

// Expand is the names s selects in schema, in the order the expression would make
// them, as Polars' expand_selector: an unknown name or place, or a pattern that does
// not compile, is an error.
func (s Selector) Expand(schema *ursus.Schema) ([]string, error) { return s.m.Match(schema) }

func set(op expr.SetOp, a, b Selector) Selector {
	return Selector{&expr.SetMatcher{Op: op, A: a.m, B: b.m}}
}

// field is the columns whose field holds pred, labelled for Explain.
func field(label string, pred func(dtype.Field) bool) Selector {
	return Selector{&expr.FieldMatcher{Label: label, Pred: pred}}
}

func byType(label string, pred func(dtype.DataType) bool) Selector {
	return field(label, func(f dtype.Field) bool { return pred(f.Type) })
}

func is(ids ...dtype.TypeID) func(dtype.DataType) bool {
	return func(t dtype.DataType) bool {
		for _, id := range ids {
			if t.ID() == id {
				return true
			}
		}
		return false
	}
}

// All is every column, and Empty none.
func All() Selector   { return Selector{&expr.AllMatcher{}} }
func Empty() Selector { return field("empty", func(dtype.Field) bool { return false }) }

// ByName is the named columns, in the order named; one the frame lacks is an error.
func ByName(names ...string) Selector {
	return Selector{&expr.NameMatcher{Names: append([]string(nil), names...)}}
}

// ByIndex is the columns at the places given, in that order, a negative place from
// the end; one past either end is an error. First and Last are the first and the
// last column.
func ByIndex(indices ...int) Selector {
	return Selector{&expr.IndexMatcher{Indices: append([]int(nil), indices...)}}
}

func First() Selector { return ByIndex(0) }
func Last() Selector  { return ByIndex(-1) }

// ByDType is the columns of the types given, exactly: a Datetime's unit and zone
// both.
func ByDType(types ...ursus.DataType) Selector {
	return Selector{&expr.DTypeMatcher{Types: append([]dtype.DataType(nil), types...)}}
}

// Numeric is the columns of numbers: integers, floats and Decimals.
func Numeric() Selector { return byType("numeric", dtype.DataType.IsNumeric) }

// Integer is the integer columns, SignedInteger the signed ones and UnsignedInteger
// the unsigned.
func Integer() Selector         { return byType("integer", dtype.DataType.IsInteger) }
func SignedInteger() Selector   { return byType("signed_integer", dtype.DataType.IsSignedInteger) }
func UnsignedInteger() Selector { return byType("unsigned_integer", dtype.DataType.IsUnsignedInteger) }

// Float is the float columns, and Decimal the Decimal ones.
func Float() Selector   { return byType("float", dtype.DataType.IsFloat) }
func Decimal() Selector { return byType("decimal", is(dtype.TypeDecimal)) }

// Temporal is the columns of dates, instants, durations and times of day; Date,
// Datetime, Duration and Time each one kind, a Datetime of any unit and zone.
func Temporal() Selector { return byType("temporal", dtype.DataType.IsTemporal) }
func Date() Selector     { return byType("date", is(dtype.TypeDate)) }
func Datetime() Selector { return byType("datetime", is(dtype.TypeDatetime)) }
func Duration() Selector { return byType("duration", is(dtype.TypeDuration)) }
func Time() Selector     { return byType("time", is(dtype.TypeTime)) }

// String is the text columns, Binary the binary ones, Boolean the Boolean and Enum
// the Enum.
func String() Selector  { return byType("string", is(dtype.TypeString)) }
func Binary() Selector  { return byType("binary", is(dtype.TypeBinary)) }
func Boolean() Selector { return byType("boolean", is(dtype.TypeBool)) }
func Enum() Selector    { return byType("enum", is(dtype.TypeEnum)) }

// List is the List columns, Struct the Struct ones, and Nested either.
func List() Selector   { return byType("list", is(dtype.TypeList)) }
func Struct() Selector { return byType("struct", is(dtype.TypeStruct)) }
func Nested() Selector { return byType("nested", dtype.DataType.IsNested) }

// StartsWith is the columns whose name starts with any of prefixes, EndsWith ends
// with any of suffixes, and Contains holds any of subs.
func StartsWith(prefixes ...string) Selector {
	return named("starts_with", prefixes, strings.HasPrefix)
}

func EndsWith(suffixes ...string) Selector { return named("ends_with", suffixes, strings.HasSuffix) }

func Contains(subs ...string) Selector { return named("contains", subs, strings.Contains) }

func named(label string, parts []string, test func(name, part string) bool) Selector {
	parts = append([]string(nil), parts...)
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = strconv.Quote(p)
	}
	return field(label+"("+strings.Join(q, ", ")+")", func(f dtype.Field) bool {
		for _, p := range parts {
			if test(f.Name, p) {
				return true
			}
		}
		return false
	})
}

// Matches is the columns whose name the regular expression matches, anywhere in it,
// as Polars' matches; anchor it with ^ and $ for the whole name.
func Matches(pattern string) Selector {
	m, err := expr.NewRegexMatcher(pattern)
	if err != nil {
		return Selector{&errMatcher{err: err}}
	}
	return Selector{m}
}

// Alpha is the columns whose name is letters alone, Digit digits alone and
// Alphanumeric either, any script's, as Polars' with ascii_only false.
func Alpha() Selector { return runes("alpha", unicode.IsLetter) }
func Digit() Selector { return runes("digit", unicode.IsDigit) }
func Alphanumeric() Selector {
	return runes("alphanumeric", func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

func runes(label string, ok func(rune) bool) Selector {
	return field(label, func(f dtype.Field) bool {
		if f.Name == "" {
			return false
		}
		for _, r := range f.Name {
			if !ok(r) {
				return false
			}
		}
		return true
	})
}

// errMatcher is a selector that failed to build, Matches' bad pattern: its error
// surfaces when the query is planned, as an expression's does.
type errMatcher struct{ err error }

func (m *errMatcher) Match(*dtype.Schema) ([]string, error) { return nil, m.err }
func (m *errMatcher) String() string                        { return "invalid selector" }
