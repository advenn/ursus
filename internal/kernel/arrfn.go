package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// ArrCall evaluates an `.arr` call (steps 341 and 342). An Array is held as a List
// is, each of its rows its size: to_list is the same column under the List's type,
// and reverse, sort and to_struct are the List's functions of it, reverse and sort
// keeping its Array.
func ArrCall(fn expr.CallFn, name string, out dtype.DataType, c *data.Column, args []any) (*data.Column, error) {
	in := c.DType()
	if in.ID() != dtype.TypeArray {
		return nil, uerr.Internalf("kernel: %s on a %s column", fn, in)
	}
	asList := c.WithDType(dtype.List(in.Inner()))
	switch fn {
	case expr.FnArrToList:
		return asList.Rename(name), nil
	case expr.FnArrReverse:
		r, err := ListCall(expr.FnListReverse, name, asList.DType(), asList, args, nil)
		if err != nil {
			return nil, err
		}
		return r.WithDType(out), nil
	case expr.FnArrSort:
		r, err := ListCall(expr.FnListSort, name, asList.DType(), asList, args, nil)
		if err != nil {
			return nil, err
		}
		return r.WithDType(out), nil
	case expr.FnArrToStruct:
		names := make([]any, len(out.Fields()))
		for i, f := range out.Fields() {
			names[i] = f.Name
		}
		return ListCall(expr.FnListToStruct, name, out, asList, names, nil)
	}
	return nil, uerr.Internalf("kernel: no kernel for %s", fn)
}

// arrDot is each row's sum of its two arrays' products, as Polars' arr.dot: a
// product of a null element is none, and a row is null where either array is
// (step 342). The rows' elements are gathered side by side, multiplied as columns,
// and summed by list.sum.
func arrDot(name string, out dtype.DataType, a, b *data.Column, n int) (*data.Column, error) {
	la, lb := a.Lists(), b.Lists()
	size := int32(a.DType().Size())
	selA := make([]int32, 0, n*int(size))
	selB := make([]int32, 0, n*int(size))
	offs := make([]int32, 1, n+1)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		sa, _, oka := la.Get(rowOf(a, i))
		sb, _, okb := lb.Get(rowOf(b, i))
		ok := oka && okb
		for j := int32(0); ok && j < size; j++ {
			selA, selB = append(selA, sa+j), append(selB, sb+j)
		}
		offs = append(offs, int32(len(selA)))
		valid.Append(ok)
	}
	ea, err := Take(la.Child(), selA)
	if err != nil {
		return nil, err
	}
	eb, err := Take(lb.Child(), selB)
	if err != nil {
		return nil, err
	}
	m, err := expr.ResolveBinary(expr.OpMul, ea.DType(), eb.DType())
	if err != nil {
		return nil, err
	}
	if m.NeedsCastL(ea.DType()) {
		if ea, err = Cast(ea.Name(), m.CastL, true, ea); err != nil {
			return nil, err
		}
	}
	if m.NeedsCastR(eb.DType()) {
		if eb, err = Cast(eb.Name(), m.CastR, true, eb); err != nil {
			return nil, err
		}
	}
	prod, err := Binary(expr.OpMul, "item", m.Out, ea, eb)
	if err != nil {
		return nil, err
	}
	return ListCall(expr.FnListSum, name, out, data.NewList(name, offs, prod, valid.Finish()), nil, nil)
}

// listReshape is each list's values in Arrays of shape, as a list: Polars' reshape
// of a column, or of a group in an aggregation (step 343). The values are the leaves,
// what a List's or an Array's elements hold; an Array's null row is its size of
// nulls, as Polars lays one out, and a List's null or empty one none. The first
// dimension is the Arrays a list makes, -1 to infer it; past one dimension each is
// an Array of the next, the last innermost. A list whose leaves the rest do not
// divide is refused, as Polars refuses it.
func listReshape(name string, out dtype.DataType, c *data.Column, shape []int64) (*data.Column, error) {
	lists := c.Lists()
	per := int64(1)
	for _, d := range shape[1:] {
		per *= d
	}
	// Where no level below the lists has a null row, each list's values are one run
	// of the deepest column: its leaves are a slice of it, no gather.
	deep, runs := leafRuns(c)
	var leaves []int32
	offs := make([]int32, 1, c.Len()+1)
	var rows int32
	for i := range c.Len() {
		s, e, ok := lists.Get(i)
		var k int64
		switch {
		case !ok:
		case runs != nil:
			k = int64(runs[i+1] - runs[i])
		default:
			mark := len(leaves)
			var err error
			if leaves, deep, err = leavesOf(lists.Child(), s, e, leaves); err != nil {
				return nil, err
			}
			k = int64(len(leaves) - mark)
		}
		switch {
		case per == 0 && k > 0:
			return nil, uerr.New(uerr.KindValue, "reshape",
				"cannot reshape %d values into a shape holding a zero dimension, %v", k, shape)
		case per > 0 && k%per != 0, shape[0] >= 0 && per > 0 && k/per != shape[0]:
			return nil, uerr.New(uerr.KindValue, "reshape",
				"cannot reshape %d values into the shape %v", k, shape).
				Hint("the values must fill each dimension; -1 first infers the rows")
		}
		if per > 0 {
			rows += int32(k / per)
		}
		offs = append(offs, rows)
	}
	var flat *data.Column
	if runs != nil {
		flat = deep.Slice(int(runs[0]), int(runs[len(runs)-1]-runs[0]))
	} else {
		if deep == nil {
			deep = lists.Child()
			for deep.DType().IsListLike() {
				deep = deep.Child()
			}
		}
		var err error
		if flat, err = Take(deep, leaves); err != nil {
			return nil, err
		}
	}
	// The Arrays, innermost first, each over the last.
	col, t := flat, out.Inner()
	sizes := shape[1:]
	for j := len(sizes) - 1; j >= 0; j-- {
		w := int32(sizes[j])
		m := int32(col.Len())
		if w > 0 {
			m /= w
		}
		inner := make([]int32, m+1)
		for r := range inner {
			inner[r] = int32(r) * w
		}
		at := t
		for range j {
			at = at.Inner()
		}
		col = data.NewList(name, inner, col, bitmap.AllSet(int(m))).WithDType(at)
	}
	return data.NewList(name, offs, col, c.Validity()).WithDType(out), nil
}

// leafRuns is where each of c's lists' values start in the column at the bottom of
// its Lists and Arrays, and where the last ends, and that column, when each list's
// values are one run of it: when no Array below c has a null row, whose values are its
// size of nulls; nil otherwise. A List's null or empty row holds none either way.
func leafRuns(c *data.Column) (*data.Column, []int32) {
	n := c.Len()
	runs := make([]int32, n+1)
	copy(runs, data.Reinterpret[int32](c.RawOffsets())[:n+1])
	col := c.Child()
	for col.DType().IsListLike() {
		if col.DType().ID() == dtype.TypeArray && col.NullCount() > 0 {
			return nil, nil
		}
		offs := data.Reinterpret[int32](col.RawOffsets())
		for i, r := range runs {
			runs[i] = offs[r]
		}
		col = col.Child()
	}
	return col, runs
}

// leavesOf appends to sel where the values of child's rows s to e are in the column
// at the bottom of child's Lists and Arrays, a NullIndex for each of an Array's null
// row's, and gives that column.
func leavesOf(child *data.Column, s, e int32, sel []int32) ([]int32, *data.Column, error) {
	if !child.DType().IsListLike() {
		for j := s; j < e; j++ {
			sel = append(sel, j)
		}
		return sel, child, nil
	}
	l := child.Lists()
	deep := l.Child()
	for deep.DType().IsListLike() {
		deep = deep.Child()
	}
	for j := s; j < e; j++ {
		cs, ce, ok := l.Get(int(j))
		if !ok {
			if child.DType().ID() == dtype.TypeArray {
				for range leafCount(child.DType()) {
					sel = append(sel, NullIndex)
				}
			}
			continue
		}
		var err error
		if sel, _, err = leavesOf(l.Child(), cs, ce, sel); err != nil {
			return nil, nil, err
		}
	}
	return sel, deep, nil
}

// leafCount is how many values an Array of t holds a row: its size times its
// element's, an Array's in turn.
func leafCount(t dtype.DataType) int {
	n := 1
	for t.ID() == dtype.TypeArray {
		n *= t.Size()
		t = t.Inner()
	}
	return n
}

// concatArr is each row's operands in one Array (step 343): concat_list's, an Array
// operand's elements, as a List's, and another's value, the answer relabelled.
func concatArr(name string, out dtype.DataType, ops []*data.Column, n int) (*data.Column, error) {
	lists := make([]*data.Column, len(ops))
	for i, c := range ops {
		lists[i] = c
		if c.DType().ID() == dtype.TypeArray {
			lists[i] = c.WithDType(dtype.List(c.DType().Inner()))
		}
	}
	r, err := listsOf(name, dtype.List(out.Inner()), lists, n)
	if err != nil {
		return nil, err
	}
	return r.WithDType(out), nil
}
