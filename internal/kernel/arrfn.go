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
