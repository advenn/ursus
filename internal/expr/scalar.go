package expr

// An aggregate in a row context (v0.6.5 step 250, E1 of v0.6.5-scope.md).
//
// Polars takes an aggregate in select, with_columns and filter as a scalar, the one
// value broadcast to every row, and a select of aggregates alone as one row. Here an
// expression in those contexts yields a value a row, so an aggregate was refused,
// pointing at GroupBy. But a scalar subexpression, one that reads every column it
// reads inside an aggregate, is the same aggregate over a window with no partition,
// which computes it once and writes it back to every row, and streams and spills
// past a limit (steps 197 and 242). BindScalars makes that window; a Select of
// scalars alone is a keyless group-by, built by the caller.

// IsScalar reports whether n is one value for the whole frame: it aggregates, and
// reads no column, and no window's answer, outside an aggregate.
func IsScalar(n Node) bool { return HasUnboundAgg(n) && !readsRows(n) }

// IsConstant reports whether n reads no row and aggregates nothing: a literal, or
// an expression of literals.
func IsConstant(n Node) bool { return !HasUnboundAgg(n) && !readsRows(n) && !HasWindow(n) }

// readsRows reports whether n reads a value a row outside an aggregate: a column, a
// selection of them, or a window's answer.
func readsRows(n Node) bool {
	switch n.(type) {
	case *Agg:
		return false
	case *Col, *Match, *Window, *Element:
		return true
	}
	for _, c := range n.Children() {
		if readsRows(c) {
			return true
		}
	}
	return false
}

// BindScalars wraps each largest scalar subexpression of n in a window with no
// partition, so that a row context takes it as Polars does. A name at the root, an
// alias or a prefix, stays at the root: naming reads through a window either way,
// but Explain then shows the window where a user would write it.
func BindScalars(n Node) Node {
	switch t := n.(type) {
	case *Alias, *Rename:
		return Rebuild(t, []Node{BindScalars(t.Children()[0])})
	}
	if !HasUnboundAgg(n) {
		return n
	}
	if IsScalar(n) {
		return &Window{Child: n}
	}
	kids := n.Children()
	out := make([]Node, len(kids))
	for i, c := range kids {
		out[i] = BindScalars(c)
	}
	return Rebuild(n, out)
}
