package plan

import (
	"slices"
	"strconv"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// winTempPrefix names the columns a Window node publishes. Callers that must not
// leak them project them away again.
const winTempPrefix = "__win"

// extractWindows lifts every window out of exprs into a Window node beneath input.
//
// Each window subtree is replaced by a reference to a temporary column, and the
// windows themselves become the new node's expressions — the same two-phase shape
// the physical planner uses for aggregates, done one level higher so that Explain
// shows the window and the optimizer can refuse to push a predicate through it.
//
// Identical windows share one temporary. That dedup keys on expr.Identity, which
// starts from String() — which is why Window.String renders the partition keys, the
// order keys and the mapping: two windows that rendered the same would become one
// computation and every row would get the wrong partition's answer.
//
// It keyed on String() itself until step 72, and String() leaves out two things:
// a strong literal's type, and which udf a name belongs to. The first merged
// `(i8 + int8(100)).max()` with `(i8 + int64(100)).max()` and gave the second the
// first one's Int8 answer; Identity adds both. plan.CheckUDFNames still refuses two
// udfs sharing a name, and still before this runs: the merge below DROPS the losing
// subtree rather than sharing it, so afterwards the second udf is not in the tree to
// be found.
//
// A window inside a window is computed beneath it, in its scope (step 335): each
// ordered function, aggregate and window in the outer window's operands is a window
// of its own, partitioned and ordered as the outer one, and the outer one reads its
// temporary. The windows are computed in layers, one Window node a layer, each over
// the temporaries of the layers beneath it, so x.Shift(1).CumSum().Over(g) shifts
// within each g and then sums within each g, as Polars computes it.
//
// It returns input unchanged and exprs unchanged when there is no window, so the
// ordinary path pays one HasWindow walk and nothing else.
func extractWindows(input Node, exprs []expr.Node, op string) (Node, []expr.Node, []string, error) {
	any := false
	for _, e := range exprs {
		if expr.HasWindow(e) {
			any = true
			break
		}
	}
	if !any {
		return input, exprs, nil, nil
	}

	var specs [][]expr.Node // a layer's windows, the innermost first
	var names []string
	byKey := map[string]string{}
	layerOf := map[string]int{}

	// register adds w as a temporary, computed one layer past the deepest temporary
	// it reads, and is a reference to it. Identical windows share one.
	register := func(w *expr.Window) expr.Node {
		key := expr.Identity(w)
		name, seen := byKey[key]
		if !seen {
			layer := 0
			expr.Walk(w, func(x expr.Node) bool {
				if c, ok := x.(*expr.Col); ok {
					if l, ok := layerOf[c.Name]; ok {
						layer = max(layer, l+1)
					}
				}
				return true
			})
			name = winTempPrefix + strconv.Itoa(len(names))
			byKey[key] = name
			layerOf[name] = layer
			for len(specs) <= layer {
				specs = append(specs, nil)
			}
			specs[layer] = append(specs[layer], &expr.Alias{Child: w, Name: name})
			names = append(names, name)
		}
		return &expr.Col{Name: name}
	}

	var rewrite func(expr.Node) (expr.Node, error)
	rewrite = func(n expr.Node) (expr.Node, error) {
		switch t := n.(type) {
		case *expr.Window:
			// A body that is not itself one aggregate or ordered function is handed
			// the window's keys, part by part (distribute).
			d, ok, err := distribute(t)
			if err != nil {
				return nil, err
			}
			if ok {
				return rewrite(d)
			}
			// A key must be a value of its row's, not a window's.
			bad := false
			for _, k := range t.PartitionBy {
				bad = bad || expr.HasWindow(k)
			}
			for _, k := range t.OrderBy {
				bad = bad || expr.HasWindow(k.Expr)
			}
			if bad {
				return nil, uerr.New(uerr.KindUnsupported, op,
					"a window's keys may not hold a window: %s", t.String()).
					Hint("compute the inner window into a column with WithColumns first")
			}
			// An ordering over an aggregate, refused here, while the query is
			// resolved, as Window.Field refuses one: a window inside another takes
			// its order, and built later it would first be typed inside an optimizer
			// rule, and read as ursus's own failure.
			if a, ok := t.Child.(*expr.Agg); ok && len(t.OrderBy) > 0 {
				return nil, expr.OrderedAggRefusal(a.Op)
			}
			// Nothing in the body to window. The physical planner refused this alone,
			// so Explain printed a plan Collect would not run.
			switch t.Child.(type) {
			case *expr.Agg, *expr.WinFn:
			default:
				if t.Mapping == expr.MapGroupsToRows {
					return nil, uerr.New(uerr.KindType, "over",
						"%s is not a window function", t.Child.String()).
						Hint("Over applies to an aggregate such as .Sum() or .Mean(), or to " +
							"an ordered function such as .Rank(...) or .CumSum(...)")
				}
			}
			// What its operands window is computed first, beneath it.
			body, err := liftOperands(t, rewrite)
			if err != nil {
				return nil, err
			}
			w := *t
			w.Child = body
			return register(&w), nil

		case *expr.WinFn:
			// A bare ordered function with no .Over() is a window over the whole
			// frame — one partition. Normalising it here means the planner and the
			// sink only ever see one shape.
			return rewrite(&expr.Window{Child: t})

		default:
			kids := n.Children()
			if len(kids) == 0 {
				return n, nil
			}
			out := make([]expr.Node, len(kids))
			for i, c := range kids {
				r, err := rewrite(c)
				if err != nil {
					return nil, err
				}
				out[i] = r
			}
			return expr.Rebuild(n, out), nil
		}
	}

	out := make([]expr.Node, len(exprs))
	for i, e := range exprs {
		// The output name has to survive the rewrite. `Col("x").Mean().Over(g)` is
		// named "x", and replacing it with a reference to __win0 would rename the
		// result to "__win0" — so anything whose name changed is re-aliased.
		want := expr.OutputName(e)
		r, err := rewrite(e)
		if err != nil {
			return nil, nil, nil, err
		}
		if expr.OutputName(r) != want {
			r = &expr.Alias{Child: r, Name: want}
		}
		out[i] = r
	}

	for _, layer := range specs {
		input = &Window{Input: input, Exprs: layer}
	}
	return input, out, names, nil
}

// orderFor is the order a window of body takes from the window it is inside, whose
// order is order: none for an aggregate whose answer does not depend on it, where an
// order written over one is refused, and else order.
func orderFor(body expr.Node, order []expr.OrderKey) []expr.OrderKey {
	if a, ok := body.(*expr.Agg); ok && !a.Op.IsOrderDependent() {
		return nil
	}
	return order
}

// liftOperands is the body of window t with each ordered function, aggregate and
// window in its operands made a window of t's scope and rewritten, by rewrite, into
// a reference to its temporary, as Polars computes a window inside a window, the
// inner one within the outer one's partitions and in its order:
//   - an ordered function, x.Shift(1) in x.Shift(1).CumSum().Over(g), is windowed by
//     t's partition and order;
//   - an aggregate, x.Mean() in (x - x.Mean()).CumSum().Over(g), by t's partition,
//     and by its order too only where the answer depends on it, which is refused
//     as an ordering over an aggregate always is;
//   - a window a sugar built takes t's keys in place of its own, as distribute
//     hands them down; one the user wrote, x.Sum().Over(g) in
//     x.Sum().Over(g).CumSum().Over(h), is partitioned within t's partitions, by h
//     then g, and ordered by its own order or else by t's.
//
// Under a mapping but the default, which does not give a row its own value, a
// window inside is refused.
func liftOperands(t *expr.Window, rewrite func(expr.Node) (expr.Node, error)) (expr.Node, error) {
	nested := false
	for _, c := range t.Child.Children() {
		nested = nested || expr.HasWindow(c) || expr.HasAgg(c)
	}
	if !nested {
		return t.Child, nil
	}
	if t.Mapping != expr.MapGroupsToRows {
		return nil, uerr.New(uerr.KindUnsupported, "over",
			"a window mapped %s may not contain another window or an aggregate: %s", t.Mapping, t.String()).
			Hint("compute the inner one into a column with WithColumns first")
	}
	var lift func(expr.Node) (expr.Node, error)
	lift = func(n expr.Node) (expr.Node, error) {
		switch u := n.(type) {
		case *expr.WinFn:
			return rewrite(&expr.Window{Child: u, PartitionBy: t.PartitionBy, OrderBy: t.OrderBy})
		case *expr.Agg:
			if len(t.OrderBy) > 0 && u.Op.IsOrderDependent() {
				return nil, expr.OrderedAggRefusal(u.Op)
			}
			return rewrite(&expr.Window{Child: u, PartitionBy: t.PartitionBy})
		case *expr.Window:
			w := *u
			switch {
			case u.Inherits:
				w.PartitionBy, w.OrderBy, w.Mapping, w.Inherits = t.PartitionBy, t.OrderBy, t.Mapping, false
			case u.Mapping != expr.MapGroupsToRows:
				return nil, uerr.New(uerr.KindUnsupported, "over",
					"a window mapped %s may not be inside another window: %s", u.Mapping, u.String()).
					Hint("compute it into a column with WithColumns first")
			default:
				w.PartitionBy = append(slices.Clone(t.PartitionBy), u.PartitionBy...)
				if len(u.OrderBy) == 0 {
					w.OrderBy = orderFor(u.Child, t.OrderBy)
				}
			}
			return rewrite(&w)
		}
		kids := n.Children()
		if len(kids) == 0 {
			return n, nil
		}
		out := make([]expr.Node, len(kids))
		for i, c := range kids {
			r, err := lift(c)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return expr.Rebuild(n, out), nil
	}
	kids := t.Child.Children()
	out := make([]expr.Node, len(kids))
	for i, c := range kids {
		r, err := lift(c)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return expr.Rebuild(t.Child, out), nil
}

// distribute hands a window's partition, order and mapping down into a body that is
// not itself one aggregate or ordered function (step 148): each aggregate and each
// ordered function in the body is windowed alone, and each window a sugar built
// (expr.Window.Inherits) takes the outer keys in place of its own. The outer window
// is then gone, and no window left holds another.
//
// So (x - x.Mean()).Over(g) is x - x.Mean().Over(g), as Polars computes it, where
// the physical window refused a body that is not one window function. And so the
// sugars that build windows the user never wrote work inside .Over(g), which was
// audit A8: Diff's and PctChange's Shift, an ordered function with no Over of its
// own, and FillNull(FillMean)'s mean over the frame.
//
// # Why handing it down is the same answer
//
// Under the default mapping, a window's body is computed per partition and each row
// gets its own partition's value. A body that is elementwise around its windowed
// parts therefore equals the same elementwise expression over those parts, each
// windowed by the same keys.
//
// It declines, and the window stays as it was:
//   - for a body that is one aggregate or ordered function, the ordinary window,
//     including one whose operand holds a window: x.Diff(1).CumSum().Over(g)
//     would need one window computed below another, and is refused;
//   - for a body with a window the user wrote inside it, whose scope is the user's:
//     refused as a window inside a window;
//   - for a body with nothing windowed in it, which extractWindows refuses as not
//     a window function;
//   - for a mapping but the default, the only one that maps back to rows.
func distribute(w *expr.Window) (expr.Node, bool, error) {
	if w.Mapping != expr.MapGroupsToRows {
		return nil, false, nil
	}
	switch w.Child.(type) {
	case *expr.WinFn, *expr.Agg:
		return nil, false, nil
	}
	var refused error
	scope := func(n expr.Node) expr.Node {
		return &expr.Window{Child: n, PartitionBy: w.PartitionBy, OrderBy: w.OrderBy, Mapping: w.Mapping}
	}
	scoped := 0
	var down func(expr.Node) (expr.Node, bool)
	down = func(n expr.Node) (expr.Node, bool) {
		switch t := n.(type) {
		case *expr.Window:
			if t.Inherits {
				scoped++
				return scope(t.Child), true
			}
			// One the user wrote is computed within the outer window's partitions,
			// as Polars computes it, in its own order or else the outer one's.
			if t.Mapping != expr.MapGroupsToRows {
				return nil, false
			}
			inner := *t
			inner.PartitionBy = append(slices.Clone(w.PartitionBy), t.PartitionBy...)
			if len(t.OrderBy) == 0 {
				inner.OrderBy = orderFor(t.Child, w.OrderBy)
			}
			scoped++
			return &inner, true
		case *expr.Agg:
			// Refused here, while the query is resolved, as Window.Field refuses the
			// same window written by hand: built later, it would first be typed
			// inside an optimizer rule, and read as ursus's own failure. An
			// aggregate whose answer does not depend on the order takes none.
			if len(w.OrderBy) > 0 && t.Op.IsOrderDependent() {
				refused = expr.OrderedAggRefusal(t.Op)
				return nil, false
			}
			scoped++
			s := scope(t).(*expr.Window)
			s.OrderBy = orderFor(t, w.OrderBy)
			return s, true
		case *expr.WinFn:
			scoped++
			return scope(t), true
		}
		kids := n.Children()
		if len(kids) == 0 {
			return n, true
		}
		out := make([]expr.Node, len(kids))
		for i, c := range kids {
			r, ok := down(c)
			if !ok {
				return nil, false
			}
			out[i] = r
		}
		return expr.Rebuild(n, out), true
	}
	d, ok := down(w.Child)
	if refused != nil {
		return nil, false, refused
	}
	return d, ok && scoped > 0, nil
}

// dropTemps projects away the window temporaries, restoring the schema the caller
// would have had without them.
//
// Needed wherever the node above the Window does not already choose its own output
// columns. Project does; WithColumns does not — its output is its input plus what it
// adds, so the temporaries would otherwise appear in the frame.
func dropTemps(n Node, keep []string) Node {
	exprs := make([]expr.Node, len(keep))
	for i, name := range keep {
		exprs[i] = &expr.Col{Name: name}
	}
	return &Project{Input: n, Exprs: exprs}
}

// rejectWindow refuses a window in a context that cannot host one yet.
//
// Sort and join keys are row-preserving, so a window is meaningful in each, as it is
// in Filter, where `Filter(x.Sum().Over(g).Gt(100))` is SQL's QUALIFY and is computed
// beneath the filter since step 250. What is missing is not
// the semantics but the plumbing: each would need the window computed below it and
// the temporary projected away above it, and the node above them does not choose its
// own columns. The message names the rewrite that works today rather than implying
// the query is meaningless.
//
// The rewrite moves the WINDOW, not the expression holding it. It used to move the
// whole of e, which in Agg is an aggregate: WithColumns refuses one, and Agg then
// refuses the bare Col("w") it is left with (audit A9).
func rejectWindow(e expr.Node, op string) error {
	var w expr.Node
	expr.Walk(e, func(x expr.Node) bool {
		switch x.(type) {
		case *expr.Window, *expr.WinFn:
			if w == nil {
				w = x
			}
			return false
		}
		return w == nil
	})
	if w == nil {
		return nil
	}
	err := uerr.New(uerr.KindUnsupported, op,
		"a window expression is not supported in %s yet: %s", op, e.String()).
		Hint("compute the window first, .WithColumns(%s.Alias(\"w\")), and use "+
			"Col(\"w\") in its place", w.String())
	if _, bare := w.(*expr.WinFn); bare && op == "agg" {
		err.Hint("in WithColumns %s runs over the whole frame; .Over(the group-by "+
			"keys) runs it within each group", w.String())
	}
	return err
}
