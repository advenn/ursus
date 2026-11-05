package expr

import "strconv"

// --- generic rebuild ------------------------------------------------------------

// Rebuild returns a copy of n with its children replaced, in the order Children
// returns them.
//
// # Why this exists
//
// Rewriting an expression tree means copying every node with new children, and
// before this there were two hand-written copies of that walk — substitute, and the
// physical planner's extractAggs — with a third about to be added for windows. Each
// copy enumerates the node types and carries their non-child fields across by hand,
// which is exactly the shape of the bug step 7 hit twice: an Agg rebuilt without its
// Params silently became a different aggregate, and a Window rebuilt without its
// Mapping would silently become a different window.
//
// Copying the struct and replacing only the child fields makes that class impossible:
// a new field on any node is carried by default, and the only way to get it wrong is
// to add a child field without updating Children, which the length check catches.
//
// It PANICS on an unknown node type, for the same reason substitute did: Node is
// sealed, so reaching the default means a node type was added without teaching the
// rewriter about it, and silently dropping its children is worse than a crash.
func Rebuild(n Node, kids []Node) Node {
	if got, want := len(kids), len(n.Children()); got != want {
		panic("expr: Rebuild given " + itoa(got) + " children for a node with " + itoa(want))
	}
	switch t := n.(type) {
	case *Col, *Lit, *Err, *Match:
		return n // leaves

	case *Binary:
		c := *t
		c.L, c.R = kids[0], kids[1]
		return &c
	case *Unary:
		c := *t
		c.Child = kids[0]
		return &c
	case *Alias:
		c := *t
		c.Child = kids[0]
		return &c
	case *Cast:
		c := *t
		c.Child = kids[0]
		return &c
	case *Rename:
		c := *t
		c.Child = kids[0]
		return &c
	case *Agg:
		c := *t
		c.Child = kids[0]
		return &c
	case *Filter:
		c := *t
		c.Child, c.Pred = kids[0], kids[1]
		return &c
	case *Unnest:
		return &Unnest{Child: kids[0], Fields: t.Fields}
	case *Fold:
		return t.rebuild(kids)
	case *Variadic:
		return t.rebuild(kids)
	case *Element, *OuterRef:
		return n // leaves of a list's body
	case *ListMap:
		return &ListMap{List: kids[0], Body: t.Body, Outer: append([]Node(nil), kids[1:]...)}
	case *Reshape:
		c := *t
		c.Child = kids[0]
		if t.By != nil {
			c.By = kids[1]
		}
		return &c
	case *Call:
		c := *t
		c.Args = append([]Node(nil), kids...)
		return &c
	case *Cond:
		c := *t
		c.Pred, c.Then, c.Else = kids[0], kids[1], kids[2]
		return &c

	case *Window:
		c := *t
		c.Child = kids[0]
		c.PartitionBy = append([]Node(nil), kids[1:1+len(t.PartitionBy)]...)
		ord := make([]OrderKey, len(t.OrderBy))
		for i, k := range t.OrderBy {
			ord[i] = OrderKey{
				Expr: kids[1+len(t.PartitionBy)+i], Descending: k.Descending,
				NullsLast: k.NullsLast,
			}
		}
		c.OrderBy = ord
		return &c
	case *WinFn:
		c := *t
		c.Child = kids[0]
		if t.Fill != nil {
			c.Fill = kids[1]
		}
		return &c

	case *UDF:
		// Copying the struct carries Impl, Out, Nullable and Name across
		// unchanged, which is the point: rebuilding must not alter what the user
		// declared, only which child it applies to.
		//
		// This arm is not optional. Rebuild is reached by substitute during
		// multi-column expansion — Col("a","b").MapElements(...) — and by
		// simplify.walk on EVERY optimizer pass, and the default arm below panics
		// rather than erroring.
		c := *t
		c.Child = kids[0]
		return &c

	default:
		panic("expr: Rebuild: unhandled node type")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
