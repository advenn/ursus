// Package expr is ursus's expression IR.
//
// # What an expression is
//
// An expression is a lazy description of a column transformation. `Col("a").Add(1)`
// builds a tree; it computes nothing. The tree is placed in a *context* — Select,
// WithColumns, Filter, GroupBy.Agg — and the context decides what the expression
// means and what shape its output must be.
//
// # The one method that matters
//
// Field resolves an expression's output name, dtype and nullability against an
// input schema, WITHOUT TOUCHING DATA. Everything the lazy engine promises rests
// on it:
//
//   - LazyFrame.CollectSchema tells you the result schema before reading a byte.
//   - A typo or type error is reported at plan time, not after a 40-minute scan.
//   - Projection pushdown can ask "which columns does this expression need".
//   - The evaluator has a contract to check itself against: for every node n and
//     batch b, Eval(n, b).DType() must equal n.Field(b.Schema()).Type.
//
// The design documents named this method and never defined it. It is the reason
// the walking skeleton exists.
//
// # Sealed
//
// Node is sealed by the unexported node() method. Every implementation lives in
// this package, so a type switch over Node is exhaustive by construction and the
// compiler tells you when a new node type needs handling somewhere.
package expr

import (
	"errors"

	"github.com/advenn/ursus/dtype"
)

// ErrUnexpanded is returned when a tree still containing a *Match reaches Field.
// It always means a caller skipped Expand, i.e. it is an ursus bug rather than a
// user error.
var ErrUnexpanded = errors.New("expression contains an unexpanded multi-column selection")

// Node is one node of the expression IR.
//
// A Node is IMMUTABLE. Rewrites allocate new nodes; nothing ever mutates a node
// or a slice reachable from one. That is what makes it safe for two LazyFrames to
// share a subtree, which in turn is what makes `base.Filter(x)` and
// `base.Filter(y)` independent.
type Node interface {
	// Field resolves this node's single output field against an input schema.
	// It never reads data.
	//
	// Defined only for RESOLVED trees: a tree containing a *Match must be run
	// through Expand first, and returns ErrUnexpanded otherwise.
	Field(in *dtype.Schema) (dtype.Field, error)

	// Children returns the direct sub-expressions, in evaluation order.
	//
	// A nested SCOPE body is deliberately not a child. When List().Eval() arrives,
	// its body is evaluated against a different schema (the list element type),
	// so a generic walker must not rewrite across it or it would resolve
	// Element() against the outer frame.
	Children() []Node

	// String renders the node canonically. Load-bearing: it appears in Explain
	// output, in golden test files, and in error messages.
	String() string

	node()
}

// --- traversal ---------------------------------------------------------------

// Walk calls fn for every node in the tree, parents before children (pre-order).
// Returning false from fn stops the traversal of that subtree.
func Walk(n Node, fn func(Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for _, c := range n.Children() {
		Walk(c, fn)
	}
}

// RootNames returns the distinct column names the expression reads, in first-seen
// order.
//
// This is the input to projection pushdown: the union of RootNames over every
// expression above a scan is exactly the set of columns that scan must produce.
// It is deliberately conservative — an unexpanded *Match contributes nothing
// here, which is why Expand must run first.
func RootNames(n Node) []string {
	var out []string
	seen := map[string]struct{}{}
	Walk(n, func(x Node) bool {
		if c, ok := x.(*Col); ok {
			if _, dup := seen[c.Name]; !dup {
				seen[c.Name] = struct{}{}
				out = append(out, c.Name)
			}
		}
		return true
	})
	return out
}

// OutputName returns the name the expression's result column will take.
//
// The rule is LEFTMOST COLUMN WINS: `Col("a").Add(Col("b"))` is named "a",
// matching Polars. An explicit Alias anywhere above overrides it. An expression
// with no column reference at all is named "literal".
//
// Naming has to be defined structurally like this because Select produces a
// schema, and a schema needs names before any data exists.
// Naming nodes are only consulted at the ROOT of the tree. They do not descend
// through operators: `Col("a").Alias("x").Add(1)` is named "a", not "x", because
// the alias applies to the operand rather than to the sum.
func OutputName(n Node) string {
	switch t := n.(type) {
	case *Alias:
		return t.Name
	case *Rename:
		// Renames compose, so a prefix over a suffix over a column works.
		return t.Fn(OutputName(t.Child))
	case *Window:
		// Named after what is being computed, not what it is partitioned by. The
		// generic fallback below takes the first name from RootNames, and Children
		// puts the child first precisely so the two agree — but stating it is what
		// stops a later reordering of Children from silently renaming every window.
		return OutputName(t.Child)
	case *WinFn:
		return OutputName(t.Child)
	case *Cond:
		// Named after the VALUE, not the test. Leftmost-column-wins would otherwise
		// pick the condition's column, so
		// `When(Col("a").Gt(0)).Then(Col("b")).Otherwise(Col("c"))` would be called
		// "a" — a column that does not appear in the result at all. Polars names it
		// after the first Then, and so does this.
		return OutputName(t.Then)
	}
	for _, c := range RootNames(n) {
		return c
	}
	return "literal"
}

// FirstErr returns the first deferred error carried anywhere in the tree.
//
// Expression construction cannot return an error without destroying chaining
// (`Col("a").Gt(5)` has nowhere to put one), so a failure such as a bad regex is
// parked in an *Err node and surfaces here — the expression-level half of the
// sticky-error model that LazyFrame implements at the frame level.
func FirstErr(n Node) error {
	var found error
	Walk(n, func(x Node) bool {
		if found != nil {
			return false
		}
		if e, ok := x.(*Err); ok {
			found = e.E
			return false
		}
		return true
	})
	return found
}

// HasUDF reports whether the tree contains a user-defined function.
//
// The optimizer consults this before DUPLICATING an expression. Substituting a
// column's definition into a predicate is how predicate pushdown rewrites a filter
// into its child's namespace, and for ordinary expressions that trade is fine: the
// definition is cheap and the pushdown saves downstream work.
//
// For a UDF it is a pure loss, and arithmetically so. Pushing `Filter(w > 1)` below
// the WithColumns that defines w runs the function on every input row IN THE FILTER,
// and the WithColumns still has to run it on every survivor to produce the column —
// n + survivors calls where not pushing costs n. There is no input for which the
// rewrite wins, and the function may be arbitrarily expensive.
func HasUDF(n Node) bool {
	found := false
	Walk(n, func(x Node) bool {
		if _, ok := x.(*UDF); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

// HasMatch reports whether the tree contains an unexpanded multi-column selector.
func HasMatch(n Node) bool {
	found := false
	Walk(n, func(x Node) bool {
		if _, ok := x.(*Match); ok {
			found = true
			return false
		}
		return true
	})
	return found
}
