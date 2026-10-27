package physical

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// evalListMap evaluates a ListMap (step 268): its body over the lists' elements end
// to end, a batch of them with each element's values a list beside it, and the
// answers a List with the lists' offsets and nulls.
//
// A window function in the body runs a list at a time: the lists are its partitions,
// already in order, so its segments are the lists' bounds, ordered within each by
// rank's child or the window's order keys as the frame's windows are.
func evalListMap(ctx context.Context, m *expr.ListMap, b *data.Batch) (*data.Column, error) {
	f, err := m.Field(b.Schema())
	if err != nil {
		return nil, err
	}
	lc, err := evalColumn(ctx, m.List, b)
	if err != nil {
		return nil, err
	}
	l := lc.Lists()
	lo, hi := l.Window()
	elems := l.Child().Slice(int(lo), int(hi-lo))
	n := lc.Len()
	offs := make([]int32, n+1)
	list := make([]int32, elems.Len()) // each element's list
	for i := range n {
		s, e, _ := l.Get(i)
		offs[i] = s - lo
		for j := s; j < e; j++ {
			list[j-lo] = int32(i)
		}
	}
	offs[n] = hi - lo
	scope, err := m.Scope(b.Schema())
	if err != nil {
		return nil, err
	}
	cols := []*data.Column{elems.Rename(expr.ElementName)}
	for i, o := range m.Outer {
		oc, err := evalColumn(ctx, o, b)
		if err != nil {
			return nil, err
		}
		if oc, err = kernel.Take(oc, list); err != nil {
			return nil, err
		}
		cols = append(cols, oc.Rename(expr.OuterName(i)))
	}
	eb := data.NewBatchRows(scope, cols, elems.Len())
	body, eb, err := bodyWindows(ctx, m.Body, eb, list)
	if err != nil {
		return nil, err
	}
	out, err := evalColumn(ctx, body, eb)
	if err != nil {
		return nil, err
	}
	if want := f.Type.Inner(); out.DType() != want {
		return nil, uerr.Internalf("physical: list.eval's body gave %s, typed %s", out.DType(), want)
	}
	// An Array's map is one of its size (step 342).
	return data.NewList(f.Name, offs, out.Rename(f.Name), lc.Validity()).WithDType(f.Type), nil
}

// bodyWindows computes each window function of a list's body, innermost first, a
// list at a time, and puts its answers beside the elements, the body reading them as
// columns.
func bodyWindows(ctx context.Context, n expr.Node, eb *data.Batch, list []int32) (expr.Node, *data.Batch, error) {
	kids := n.Children()
	if len(kids) > 0 {
		out := make([]expr.Node, len(kids))
		changed := false
		for i, c := range kids {
			x, b, err := bodyWindows(ctx, c, eb, list)
			if err != nil {
				return nil, nil, err
			}
			out[i], eb, changed = x, b, changed || x != c
		}
		if changed {
			n = expr.Rebuild(n, out)
		}
	}
	var fn *expr.WinFn
	var order []expr.OrderKey
	switch t := n.(type) {
	case *expr.WinFn:
		fn = t
	case *expr.Window:
		w, ok := t.Child.(*expr.WinFn)
		if !ok {
			return nil, nil, uerr.New(uerr.KindUnsupported, "list.eval",
				"%s inside list.eval is not there yet: a window there is a function of the list", t)
		}
		fn, order = w, t.OrderBy
	default:
		return n, eb, nil
	}
	if fn.Fn == expr.WinCumulativeEval {
		return nil, nil, uerr.New(uerr.KindUnsupported, "list.eval",
			"cumulative_eval inside list.eval is not there yet")
	}
	f, err := fn.Field(eb.Schema())
	if err != nil {
		return nil, nil, err
	}
	child, err := evalColumn(ctx, fn.Child, eb)
	if err != nil {
		return nil, nil, err
	}
	var fill *data.Column
	if fn.Fill != nil {
		if fill, err = evalColumn(ctx, fn.Fill, eb); err != nil {
			return nil, nil, err
		}
		if fill.DType() != f.Type {
			if fill, err = kernel.Cast(fill.Name(), f.Type, true, fill); err != nil {
				return nil, nil, err
			}
		}
		if child.DType() != f.Type {
			if child, err = kernel.Cast(child.Name(), f.Type, true, child); err != nil {
				return nil, nil, err
			}
		}
	}
	rows := eb.Rows()
	keys := []*data.Column{data.NewFixed("__list", dtype.Int32, list, bitmap.AllSet(rows))}
	specs := []kernel.SortSpec{{}}
	if fn.Fn == expr.WinRank {
		keys = append(keys, child)
		specs = append(specs, kernel.SortSpec{Descending: fn.Params.Descending})
	}
	for _, k := range order {
		c, err := evalColumn(ctx, k.Expr, eb)
		if err != nil {
			return nil, nil, err
		}
		keys = append(keys, c)
		specs = append(specs, kernel.SortSpec{Descending: k.Descending, NullsLast: k.NullsLast})
	}
	var perm []int32
	if len(keys) == 1 {
		// The elements lie a list at a time, in order, so sorting by the list alone,
		// stably, is the identity: half of an eval's time over a million elements
		// (step 386, W1 of v0.7-scope.md).
		perm = make([]int32, rows)
		for j := range perm {
			perm[j] = int32(j)
		}
	} else {
		var err error
		if perm, err = kernel.ArgSortColumns(keys, specs, rows); err != nil {
			return nil, nil, err
		}
	}
	bounds := []int32{0}
	for j := 1; j < rows; j++ {
		if list[perm[j]] != list[perm[j-1]] {
			bounds = append(bounds, int32(j))
		}
	}
	bounds = append(bounds, int32(rows))
	if rows == 0 {
		bounds = bounds[:1]
	}
	name := "\x00window" + itoa(eb.Schema().Len())
	ans, err := kernel.WinCall(fn.Fn, fn.Params, name, f.Type, child, fill, kernel.Segments{Perm: perm, Bounds: bounds})
	if err != nil {
		return nil, nil, err
	}
	fields := append(eb.Schema().FieldSlice(), dtype.Field{Name: name, Type: f.Type, Nullable: true})
	s, err := dtype.NewSchema(fields...)
	if err != nil {
		return nil, nil, err
	}
	cols := append(append([]*data.Column(nil), eb.Columns()...), ans.Rename(name))
	return &expr.Col{Name: name}, data.NewBatchRows(s, cols, rows), nil
}
