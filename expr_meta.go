package ursus

import (
	"fmt"
	"strings"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// The meta namespace (step 326): Polars' Expr.meta, questions about an expression
// itself, asked of no data.

// MetaExpr is an expression's meta functions, as Polars' meta namespace has them.
type MetaExpr struct{ e Expr }

// Meta is the expression's meta functions.
func (e Expr) Meta() MetaExpr { return MetaExpr{e} }

// RootNames is the columns the expression reads by name, a name each time it is read,
// in the order Polars' root_names lists them: an operation's inputs left to right, a
// conditional's branches before its condition. A selector, Col of several names, a
// pattern or All, names none, as Polars' does.
func (m MetaExpr) RootNames() []string {
	var out []string
	var walk func(n expr.Node)
	walk = func(n expr.Node) {
		switch t := n.(type) {
		case *expr.Col:
			out = append(out, t.Name)
			return
		case *expr.Cond:
			walk(t.Then)
			walk(t.Else)
			walk(t.Pred)
			return
		}
		for _, k := range n.Children() {
			walk(k)
		}
	}
	walk(m.e.node())
	return out
}

// OutputName is the name the expression's column takes, as Polars' output_name: an
// alias's or a name function's, else its first column's, a literal's "literal". An
// expression that may make several columns has no one name, and is an error.
func (m MetaExpr) OutputName() (string, error) {
	n := m.e.node()
	if bad, ok := n.(*expr.Err); ok {
		return "", bad.E
	}
	if m.HasMultipleOutputs() {
		return "", uerr.New(uerr.KindValue, "meta",
			"unable to find root column name for expr %s when calling 'output_name'", n)
	}
	return expr.OutputName(n), nil
}

// HasMultipleOutputs reports whether the expression may make several columns: a
// selector in it, Col of several names, a pattern, a type, All or Exclude, as Polars'
// has_multiple_outputs.
func (m MetaExpr) HasMultipleOutputs() bool {
	found := false
	expr.Walk(m.e.node(), func(x expr.Node) bool {
		if _, ok := x.(*expr.Match); ok {
			found = true
		}
		return !found
	})
	return found
}

// IsColumn reports whether the expression is one column read by name, unnamed, as
// Polars' is_column.
func (m MetaExpr) IsColumn() bool {
	_, ok := m.e.node().(*expr.Col)
	return ok
}

// IsColumnSelection reports whether the expression only selects columns, a column
// by name or a selector, as Polars' is_column_selection; with allowAliasing, under a
// name too.
func (m MetaExpr) IsColumnSelection(allowAliasing bool) bool {
	n := m.e.node()
	if allowAliasing {
		n = unnamed(n)
	}
	switch n.(type) {
	case *expr.Col, *expr.Match:
		return true
	}
	return false
}

// IsLiteral reports whether the expression is a literal, as Polars' is_literal; with
// allowAliasing, under a name too.
func (m MetaExpr) IsLiteral(allowAliasing bool) bool {
	n := m.e.node()
	if allowAliasing {
		n = unnamed(n)
	}
	_, ok := n.(*expr.Lit)
	return ok
}

// IsRegexProjection reports whether the expression selects columns by a regular
// expression, ColRegex("^a.*$"), as Polars' is_regex_projection. Col reads every
// name as a name, an anchored one too, where Polars' col("^a.*$") is a pattern.
func (m MetaExpr) IsRegexProjection() bool {
	t, ok := m.e.node().(*expr.Match)
	if !ok {
		return false
	}
	_, re := t.M.(*expr.RegexMatcher)
	return re
}

// UndoAliases is the expression without its names, every Alias and every name
// function in it, as Polars' undo_aliases.
func (m MetaExpr) UndoAliases() Expr { return wrap(undoNames(m.e.node())) }

// unnamed is n without the names at its top.
func unnamed(n expr.Node) expr.Node {
	for {
		switch t := n.(type) {
		case *expr.Alias:
			n = t.Child
		case *expr.Rename:
			n = t.Child
		default:
			return n
		}
	}
}

func undoNames(n expr.Node) expr.Node {
	n = unnamed(n)
	kids := n.Children()
	if len(kids) == 0 {
		return n
	}
	out := make([]expr.Node, len(kids))
	for i, k := range kids {
		out[i] = undoNames(k)
	}
	return expr.Rebuild(n, out)
}

// Eq reports whether two expressions are one computation, as Polars' meta.eq: alike
// in their operations, their names, and their literals' values and types, so Lit(1)
// is not Lit(1.0).
func (m MetaExpr) Eq(other Expr) bool {
	return expr.Identity(m.e.node()) == expr.Identity(other.node())
}

// Pop is the inputs of the expression's outermost operation, in the order Polars'
// pop gives them, the last first: of a + b, b then a; of a column, none.
func (m MetaExpr) Pop() []Expr {
	kids := m.e.node().Children()
	out := make([]Expr, len(kids))
	for i, k := range kids {
		out[len(kids)-1-i] = wrap(k)
	}
	return out
}

// TreeFormat is the expression as a tree, an operation a line and its inputs under
// it, as Polars' tree_format draws one; ursus draws its own.
func (m MetaExpr) TreeFormat() string {
	var b strings.Builder
	var draw func(n expr.Node, lead, branch string)
	draw = func(n expr.Node, lead, branch string) {
		b.WriteString(lead + branch + nodeLabel(n) + "\n")
		kids := n.Children()
		next := lead
		switch branch {
		case "├── ":
			next += "│   "
		case "└── ":
			next += "    "
		}
		for i, k := range kids {
			if i == len(kids)-1 {
				draw(k, next, "└── ")
			} else {
				draw(k, next, "├── ")
			}
		}
	}
	draw(m.e.node(), "", "")
	return b.String()
}

// nodeLabel is what one node of an expression does, without its inputs.
func nodeLabel(n expr.Node) string {
	switch t := n.(type) {
	case *expr.Col, *expr.Lit, *expr.Match:
		return n.String()
	case *expr.Alias:
		return "alias(" + quote(t.Name) + ")"
	case *expr.Rename:
		return t.Label
	case *expr.Binary:
		return "binary: " + t.Op.String()
	case *expr.Unary:
		return t.Op.String()
	case *expr.Cast:
		return "cast(" + t.To.String() + ")"
	case *expr.Call:
		return t.Fn.String()
	case *expr.Agg:
		return t.Op.String()
	case *expr.Window:
		return "over"
	case *expr.Cond:
		return "when"
	case *expr.Reshape:
		return t.Op.String()
	case *expr.WinFn:
		return t.Fn.String()
	case *expr.UDF:
		return t.Name
	}
	return strings.ToLower(strings.TrimPrefix(fmt.Sprintf("%T", n), "*expr."))
}
