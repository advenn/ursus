package expr

import (
	"github.com/advenn/ursus/dtype"
)

// Selectors' set algebra (step 322): Polars' selectors combined by |, &, -, ^ and ~,
// each answering in the schema's order, as Polars orders a combined selector.

// SetOp is how a SetMatcher combines its two matchers' columns.
type SetOp uint8

const (
	SetUnion      SetOp = iota // in either
	SetIntersect               // in both
	SetDifference              // in the first and not the second
	SetXor                     // in one and not the other
)

var setOpSymbols = [...]string{SetUnion: " | ", SetIntersect: " & ", SetDifference: " - ", SetXor: " ^ "}

// SetMatcher is two matchers' columns combined by a set operation.
type SetMatcher struct {
	Op   SetOp
	A, B Matcher
}

func (m *SetMatcher) Match(in *dtype.Schema) ([]string, error) {
	a, err := m.A.Match(in)
	if err != nil {
		return nil, err
	}
	b, err := m.B.Match(in)
	if err != nil {
		return nil, err
	}
	inA, inB := nameSet(a), nameSet(b)
	var out []string
	for _, n := range in.Names() {
		x, y := inA[n], inB[n]
		var keep bool
		switch m.Op {
		case SetUnion:
			keep = x || y
		case SetIntersect:
			keep = x && y
		case SetDifference:
			keep = x && !y
		case SetXor:
			keep = x != y
		}
		if keep {
			out = append(out, n)
		}
	}
	return out, nil
}

func (m *SetMatcher) String() string {
	return "(" + m.A.String() + setOpSymbols[m.Op] + m.B.String() + ")"
}

// NotMatcher is every column its inner matcher does not select.
type NotMatcher struct{ Inner Matcher }

func (m *NotMatcher) Match(in *dtype.Schema) ([]string, error) {
	sel, err := m.Inner.Match(in)
	if err != nil {
		return nil, err
	}
	drop := nameSet(sel)
	var out []string
	for _, n := range in.Names() {
		if !drop[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

func (m *NotMatcher) String() string { return "~" + m.Inner.String() }

func nameSet(names []string) map[string]bool {
	s := make(map[string]bool, len(names))
	for _, n := range names {
		s[n] = true
	}
	return s
}
