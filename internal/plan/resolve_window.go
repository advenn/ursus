package plan

import (
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

	var specs []expr.Node
	var names []string
	byKey := map[string]string{}

	var rewrite func(expr.Node) (expr.Node, error)
	rewrite = func(n expr.Node) (expr.Node, error) {
		switch t := n.(type) {
		case *expr.Window:
			// A window inside a window has no defined meaning: the inner one has
			// already written one value per row, so partitioning it again would
			// aggregate values that are themselves per-partition constants.
			//
			// The BODY is exempt from the top level, because that is where the
			// windowed thing lives: `x.CumSum().Over(g)` is a WinFn under a Window
			// by construction. What is checked is everything below that one layer,
			// plus the keys, which must be per-row values.
			nested := func(n expr.Node) bool { return expr.HasWindow(n) }
			bad := false
			for _, k := range t.PartitionBy {
				bad = bad || nested(k)
			}
			for _, k := range t.OrderBy {
				bad = bad || nested(k.Expr)
			}
			if fn, isFn := t.Child.(*expr.WinFn); isFn {
				for _, c := range fn.Children() {
					bad = bad || nested(c)
				}
			} else {
				bad = bad || nested(t.Child)
			}
			if bad {
				return nil, uerr.New(uerr.KindUnsupported, op,
					"a window may not contain another window: %s", t.String()).
					Hint("compute the inner window into a column with WithColumns first")
			}
			key := expr.Identity(t)
			name, seen := byKey[key]
			if !seen {
				name = winTempPrefix + strconv.Itoa(len(specs))
				byKey[key] = name
				specs = append(specs, &expr.Alias{Child: t, Name: name})
				names = append(names, name)
			}
			return &expr.Col{Name: name}, nil

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

	return &Window{Input: input, Exprs: specs}, out, names, nil
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
// Filter, Sort and join keys are all row-preserving, so a window is meaningful in
// each — `Filter(x.Sum().Over(g).Gt(100))` is SQL's QUALIFY. What is missing is not
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
