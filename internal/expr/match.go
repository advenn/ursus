package expr

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Matcher selects zero or more columns from a schema.
//
// Matchers are what make one written expression apply to many columns:
//
//	Col("weight", "height").Mean()      → two independent means
//	ColDType(Float64).Mul(1.1)          → every float column, count known only at plan time
//	All().Exclude("id")                 → everything but id
//
// Matching is by NAME and resolved against a schema, never against data.
type Matcher interface {
	// Match returns the selected column names, in schema order.
	Match(in *dtype.Schema) ([]string, error)
	// String renders the matcher for Explain and error messages.
	String() string
}

// --- explicit names ----------------------------------------------------------

// NameMatcher selects an explicit list of columns, in the order written.
//
// Unlike the other matchers, a name that is not present is an ERROR rather than
// an empty match: the user named a specific column, so its absence is a typo.
type NameMatcher struct{ Names []string }

func (m *NameMatcher) Match(in *dtype.Schema) ([]string, error) {
	for _, n := range m.Names {
		if !in.Has(n) {
			return nil, uerr.UnknownColumn("", n, in.Names())
		}
	}
	return append([]string(nil), m.Names...), nil
}

func (m *NameMatcher) String() string {
	q := make([]string, len(m.Names))
	for i, n := range m.Names {
		q[i] = strconv.Quote(n)
	}
	return "cols(" + strings.Join(q, ", ") + ")"
}

// --- regex -------------------------------------------------------------------

// RegexMatcher selects columns whose name matches a pattern.
//
// ursus requires the pattern to be passed to ColRegex explicitly rather than
// inferring "this string looks like a regex" from leading ^ and trailing $ the
// way Polars does. That inference makes a column literally named "^total$"
// unselectable and makes every plain Col() call pay a scan for anchors.
type RegexMatcher struct {
	Pattern string
	Re      *regexp.Regexp
}

// NewRegexMatcher compiles pattern, returning an error the caller parks in an
// *Err node.
func NewRegexMatcher(pattern string) (*RegexMatcher, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindValue, "",
			"invalid column pattern %q", pattern)
	}
	return &RegexMatcher{Pattern: pattern, Re: re}, nil
}

// Match returns every matching column. Matching nothing is not an error — it is
// the natural answer for a pattern over a schema that happens not to contain it,
// and erroring would make schema-driven code fragile.
func (m *RegexMatcher) Match(in *dtype.Schema) ([]string, error) {
	var out []string
	for _, f := range in.All() {
		if m.Re.MatchString(f.Name) {
			out = append(out, f.Name)
		}
	}
	return out, nil
}

func (m *RegexMatcher) String() string { return "cols(~" + strconv.Quote(m.Pattern) + ")" }

// --- by dtype ----------------------------------------------------------------

// DTypeMatcher selects columns by type. The number of columns it expands to is
// determined at plan time from the schema, so the same expression adapts as the
// schema changes.
type DTypeMatcher struct{ Types []dtype.DataType }

func (m *DTypeMatcher) Match(in *dtype.Schema) ([]string, error) {
	var out []string
	for _, f := range in.All() {
		for _, t := range m.Types {
			if f.Type == t {
				out = append(out, f.Name)
				break
			}
		}
	}
	return out, nil
}

func (m *DTypeMatcher) String() string {
	parts := make([]string, len(m.Types))
	for i, t := range m.Types {
		parts[i] = t.String()
	}
	return "cols(dtype: " + strings.Join(parts, ", ") + ")"
}

// --- predicate over fields ---------------------------------------------------

// FieldMatcher selects columns whose field satisfies a predicate.
//
// It is groundwork for the selector package's dtype families (numeric, temporal,
// string, ...), which want a predicate rather than an enumeration of concrete
// types. NOTHING CONSTRUCTS ONE TODAY — that package does not exist yet, and this
// doc used to claim in the present tense that it did.
type FieldMatcher struct {
	Label string
	Pred  func(dtype.Field) bool
}

func (m *FieldMatcher) Match(in *dtype.Schema) ([]string, error) {
	var out []string
	for _, f := range in.All() {
		if m.Pred(f) {
			out = append(out, f.Name)
		}
	}
	return out, nil
}

func (m *FieldMatcher) String() string { return "cols(" + m.Label + ")" }

// --- all ---------------------------------------------------------------------

// AllMatcher selects every column.
type AllMatcher struct{}

func (m *AllMatcher) Match(in *dtype.Schema) ([]string, error) { return in.Names(), nil }
func (m *AllMatcher) String() string                           { return "all()" }

// --- exclusion ---------------------------------------------------------------

// ExcludeMatcher subtracts columns from an inner matcher's result.
type ExcludeMatcher struct {
	Inner  Matcher
	Names  []string
	Re     *regexp.Regexp
	ReSrc  string
	DTypes []dtype.DataType
}

func (m *ExcludeMatcher) Match(in *dtype.Schema) ([]string, error) {
	base, err := m.Inner.Match(in)
	if err != nil {
		return nil, err
	}
	drop := make(map[string]struct{}, len(m.Names))
	for _, n := range m.Names {
		drop[n] = struct{}{}
	}
	out := base[:0:0]
	for _, n := range base {
		if _, skip := drop[n]; skip {
			continue
		}
		if m.Re != nil && m.Re.MatchString(n) {
			continue
		}
		if len(m.DTypes) > 0 {
			if f, ok := in.ByName(n); ok {
				excluded := false
				for _, t := range m.DTypes {
					if f.Type == t {
						excluded = true
						break
					}
				}
				if excluded {
					continue
				}
			}
		}
		out = append(out, n)
	}
	return out, nil
}

func (m *ExcludeMatcher) String() string {
	var b strings.Builder
	b.WriteString(m.Inner.String())
	b.WriteString(".exclude(")
	first := true
	for _, n := range m.Names {
		if !first {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(n))
		first = false
	}
	if m.ReSrc != "" {
		if !first {
			b.WriteString(", ")
		}
		b.WriteString("~" + strconv.Quote(m.ReSrc))
		first = false
	}
	for _, t := range m.DTypes {
		if !first {
			b.WriteString(", ")
		}
		b.WriteString(t.String())
		first = false
	}
	b.WriteString(")")
	return b.String()
}
