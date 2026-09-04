package plan

import (
	"strings"

	"ursus/dtype"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// Window computes windowed expressions and APPENDS them to its input's schema.
//
// # Why it is a node at all
//
// A user never writes one. `Select(Col("x").Mean().Over(Col("g")))` mentions no
// window operator, the way `GroupBy(k).Agg(...)` mentions an aggregation — so the
// node is synthesised during Resolve, spliced beneath the Project or WithColumns
// whose expressions contained windows, with each window subtree replaced by a
// reference to a `__winN` temporary.
//
// The alternative was to extract windows in the PHYSICAL planner, the way
// extractAggs pulls aggregates out of an Aggregate's expressions. That works and
// costs two things worth more than it saves: Explain would not show that a window
// is being computed, and the optimizer could not see it — which matters because a
// window is a predicate-pushdown barrier and a rule cannot refuse to cross a node
// it cannot observe.
//
// # It appends rather than replaces
//
// Output is the input schema plus one field per expression. That is what makes the
// Project above it work: it evaluates over the original columns and the temporaries
// side by side, so `Col("x").Sub(Col("x").Mean().Over(g))` needs no special case —
// the subtraction is ordinary arithmetic over two ordinary columns.
type Window struct {
	Input Node

	// Exprs are the windows to compute. Every one is an *expr.Window wrapped in an
	// *expr.Alias naming its temporary, which is what lets Schema resolve them
	// through the ordinary path.
	Exprs []expr.Node
}

func (w *Window) planNode()        {}
func (w *Window) Children() []Node { return []Node{w.Input} }

func (w *Window) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Window takes exactly one child")
	}
	c := *w
	c.Input = kids[0]
	return &c
}

func (w *Window) Schema() (*dtype.Schema, error) {
	in, err := w.Input.Schema()
	if err != nil {
		return nil, err
	}
	fields := make([]dtype.Field, 0, in.Len()+len(w.Exprs))
	fields = append(fields, in.FieldSlice()...)
	for _, e := range w.Exprs {
		f, err := expr.Resolve(e, in)
		if err != nil {
			return nil, uerr.Annotate(err, "over", "Window")
		}
		fields = append(fields, f)
	}
	return dtype.NewSchema(fields...)
}

// Label renders the windows themselves, not a count.
//
// Same reason Aggregate does: a golden plan file is how an optimizer regression is
// caught, and "2 windows" makes two structurally different queries indistinguishable
// in the snapshot.
func (w *Window) Label() string {
	var b strings.Builder
	b.WriteString("WINDOW [")
	b.WriteString(expr.StringAll(w.Exprs))
	b.WriteString("]")
	return b.String()
}
