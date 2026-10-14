package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Filter is a column's values where a predicate holds, Polars' Expr.filter (v0.6.5
// step 251, E2 of v0.6.5-scope.md).
//
// Its length is not the frame's, so it stands beneath an aggregate: a conditional
// aggregate, SQL's FILTER (WHERE ...). col("a").filter(col("b") > 0).sum() sums the
// a's whose b is positive, in a group-by, a window, a temporal group, and as a
// scalar in Select. The aggregate's sink feeds its accumulator only the rows the
// predicate is true on, so len, first and last count and pick among them. A filter
// anywhere else is refused (LooseFilter) until E4 gives it a place.
type Filter struct {
	Child Node
	Pred  Node
}

func (f *Filter) node()            {}
func (f *Filter) Children() []Node { return []Node{f.Child, f.Pred} }
func (f *Filter) String() string   { return f.Child.String() + ".filter(" + f.Pred.String() + ")" }

func (f *Filter) Field(in *dtype.Schema) (dtype.Field, error) {
	pf, err := f.Pred.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	if !pf.Type.IsBool() {
		return dtype.Field{}, uerr.New(uerr.KindType, "filter",
			"a filter's predicate must be Boolean, got %s", pf.Type).
			Hint("predicate: %s", f.Pred.String())
	}
	return f.Child.Field(in)
}

// LooseFilter is the first Filter in n that is not an aggregate's input, or nil. An
// aggregate's input may be a chain of filters, whose predicates are themselves
// checked.
func LooseFilter(n Node) *Filter {
	switch t := n.(type) {
	case *Filter:
		return t
	case *Agg:
		c := t.Child
		for {
			f, ok := c.(*Filter)
			if !ok {
				break
			}
			if l := LooseFilter(f.Pred); l != nil {
				return l
			}
			c = f.Child
		}
		return LooseFilter(c)
	}
	for _, c := range n.Children() {
		if l := LooseFilter(c); l != nil {
			return l
		}
	}
	return nil
}

// FilterChain is an aggregate's input stripped of its filters, and their
// predicates, outermost first; none when it has none.
func FilterChain(n Node) (Node, []Node) {
	var preds []Node
	for {
		f, ok := n.(*Filter)
		if !ok {
			return n, preds
		}
		preds = append(preds, f.Pred)
		n = f.Child
	}
}

// looseFilterErr refuses a filter that is not an aggregate's input.
func looseFilterErr(f *Filter) error {
	return uerr.New(uerr.KindUnsupported, "filter",
		"%s changes the column's length, and is only an aggregate's input yet", f.String()).
		Hint("as an aggregate's input it filters the rows the aggregate reads: %s.sum()", f.String()).
		Hint("to keep the frame's rows where the predicate holds, use the frame's Filter: lf.Filter(%s)",
			f.Pred.String())
}
