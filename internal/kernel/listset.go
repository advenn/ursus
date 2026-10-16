package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
	"slices"
)

// The list set operations and gather (step 223), which take a second List column.
//
// # As Polars has them
//
//   - set_union is the first list's distinct elements in the order they first
//     appear, then the second's not among them;
//   - set_intersection is the first's distinct elements the second holds, in the
//     first's order, and set_difference those it does not;
//   - set_symmetric_difference is set_difference, then the second's distinct
//     elements the first does not hold, in the second's order;
//   - elements are equal as group_by keys are: a null element is a value equal to
//     another null, NaN equals NaN, and −0 equals +0, the first list's spelling kept;
//   - a null list on either side gives a null list.
//
// gather picks each list's elements at a list of indices: a negative index counts
// from the end, a null index gives a null element, and an index out of range is
// refused, or gives null for gather_or_null. A null list, or null indices, give a
// null list.

// listSet is a set operation over the lists of a and b, whose elements are cast to
// out's.
func listSet(fn expr.CallFn, name string, out dtype.DataType, a, b *data.Column, n int) (*data.Column, error) {
	ac, alo, err := listElems(a, out.Inner())
	if err != nil {
		return nil, err
	}
	bc, blo, err := listElems(b, out.Inner())
	if err != nil {
		return nil, err
	}
	// One column of both lists' elements, so one encoder compares across them and
	// one Take builds the answer: a's first, then b's from base.
	both, err := ConcatColumns(ac.Name(), []*data.Column{ac, bc})
	if err != nil {
		return nil, err
	}
	base := int32(ac.Len())
	enc, err := NewGroupKeyEncoder(fn.String(), []*data.Column{both})
	if err != nil {
		return nil, err
	}
	al, bl := a.Lists(), b.Lists()
	offs := make([]int32, 1, n+1)
	var pick []int32
	valid := bitmap.NewBuilder(n)
	inA := make(map[string]struct{})
	inB := make(map[string]struct{})
	out1 := make(map[string]struct{}) // what this row has given out
	for i := range n {
		as, ae, aok := al.Get(rowOf(a, i))
		bs, be, bok := bl.Get(rowOf(b, i))
		valid.Append(aok && bok)
		if !aok || !bok {
			offs = append(offs, int32(len(pick)))
			continue
		}
		clear(inA)
		clear(inB)
		clear(out1)
		// Positions in both: a's from its window's start, b's after all of a's.
		as, ae, bs, be = as-alo, ae-alo, bs-blo+base, be-blo+base
		key := func(e int32) string { return string(enc.Encode(int(e))) }
		give := func(e int32, k string) {
			if _, done := out1[k]; !done {
				out1[k] = struct{}{}
				pick = append(pick, e)
			}
		}
		if fn != expr.FnListSetUnion {
			for e := bs; e < be; e++ {
				inB[key(e)] = struct{}{}
			}
		}
		for e := as; e < ae; e++ {
			k := key(e)
			_, held := inB[k]
			switch fn {
			case expr.FnListSetUnion:
				give(e, k)
			case expr.FnListSetIntersection:
				if held {
					give(e, k)
				}
			default: // difference, and the first half of the symmetric one
				if !held {
					give(e, k)
				}
			}
			if fn == expr.FnListSetSymmetricDifference {
				inA[k] = struct{}{}
			}
		}
		switch fn {
		case expr.FnListSetUnion:
			for e := bs; e < be; e++ {
				give(e, key(e))
			}
		case expr.FnListSetSymmetricDifference:
			for e := bs; e < be; e++ {
				k := key(e)
				if _, held := inA[k]; !held {
					give(e, k)
				}
			}
		}
		offs = append(offs, int32(len(pick)))
	}
	child, err := Take(both, pick)
	if err != nil {
		return nil, err
	}
	return data.NewList(name, offs, child, valid.Finish()), nil
}

// listGatherAt picks each list's elements at its indices.
func listGatherAt(name string, orNull bool, a, idx *data.Column, n int) (*data.Column, error) {
	ac, alo, err := listElems(a, a.DType().Inner())
	if err != nil {
		return nil, err
	}
	ic, ilo, err := listElems(idx, dtype.Int64)
	if err != nil {
		return nil, err
	}
	at := data.MustValues[int64](ic)
	al, il := a.Lists(), idx.Lists()
	offs := make([]int32, 1, n+1)
	var pick []int32
	valid := bitmap.NewBuilder(n)
	for i := range n {
		as, ae, aok := al.Get(rowOf(a, i))
		is, ie, iok := il.Get(rowOf(idx, i))
		valid.Append(aok && iok)
		if aok && iok {
			size := int64(ae - as)
			for e := is - ilo; e < ie-ilo; e++ {
				if !ic.IsValid(int(e)) {
					pick = append(pick, NullIndex)
					continue
				}
				k := at[e]
				if k < 0 {
					k += size
				}
				if k < 0 || k >= size {
					if orNull {
						pick = append(pick, NullIndex)
						continue
					}
					return nil, uerr.New(uerr.KindValue, "list.gather",
						"index %d is out of range for a list of %d elements, in row %d",
						at[e], size, i).
						Hint("Gather(indices, true) gives null for an index out of range")
				}
				pick = append(pick, as-alo+int32(k))
			}
		}
		offs = append(offs, int32(len(pick)))
	}
	child, err := Take(ac, pick)
	if err != nil {
		return nil, err
	}
	return data.NewList(name, offs, child, valid.Finish()), nil
}

// listElems is a List column's own elements, its window of its child, cast to elem,
// and where the window starts.
func listElems(c *data.Column, elem dtype.DataType) (*data.Column, int32, error) {
	if c.DType().ID() != dtype.TypeList {
		return nil, 0, uerr.Internalf("kernel: a list operand of %s", c.DType())
	}
	l := c.Lists()
	lo, hi := l.Window()
	child := l.Child().Slice(int(lo), int(hi-lo))
	if child.DType() != elem {
		var err error
		if child, err = Cast(child.Name(), elem, true, child); err != nil {
			return nil, 0, err
		}
	}
	return child, lo, nil
}

// rowOf is the row of c that answers row i: row 0 of a literal, one row long.
func rowOf(c *data.Column, i int) int {
	if c.Len() == 1 {
		return 0
	}
	return i
}

// listSortBy sorts each list by the list beside it, element for element, as Polars'
// sort_by sorts a group by another column (step 253): stably, nulls first either way,
// descending when asked. The two lists of a row must be of one length; a null list
// on either side is a null answer.
func listSortBy(name string, desc bool, a, by *data.Column) (*data.Column, error) {
	if a.Len() != by.Len() {
		return nil, uerr.Internalf("kernel: list.sort_by over %d and %d rows", a.Len(), by.Len())
	}
	keys := by.Lists()
	cmp, err := NewComparator([]*data.Column{keys.Child()}, []SortSpec{{Descending: desc}})
	if err != nil {
		return nil, err
	}
	vals := a.Lists()
	valid := bitmap.And(a.Validity(), by.Validity())
	offs := make([]int32, 1, a.Len()+1)
	var sel []int32
	order := []int32{}
	for i := range a.Len() {
		as, ae, okA := vals.Get(i)
		bs, be, okB := keys.Get(i)
		if okA && okB {
			if ae-as != be-bs {
				return nil, uerr.New(uerr.KindValue, "list.sort_by",
					"row %d has %d values and %d keys to sort them by", i, ae-as, be-bs).
					Hint("each row's keys are its own values' keys, one for each")
			}
			order = order[:0]
			for k := range ae - as {
				order = append(order, k)
			}
			slices.SortStableFunc(order, func(x, y int32) int { return cmp(int(bs+x), int(bs+y)) })
			for _, k := range order {
				sel = append(sel, as+k)
			}
		}
		offs = append(offs, int32(len(sel)))
	}
	child, err := Take(vals.Child(), sel)
	if err != nil {
		return nil, err
	}
	return data.NewList(name, offs, child, valid), nil
}

// listsOf is concat_list(ops...) (step 266): each row's operands end to end in one
// list of out's element type, a List operand's elements and another's value, a null
// value a null element. A null List operand makes the row null, as Polars makes it.
func listsOf(name string, out dtype.DataType, ops []*data.Column, n int) (*data.Column, error) {
	elem := out.Inner()
	type source struct {
		list *data.Column // the operand, a List; nil for a value
		val  *data.Column // the operand of a value, at the element type
		lo   int32        // where the List's window of its child starts
		base int32        // where its elements, or its values, start in the pool
	}
	srcs := make([]source, len(ops))
	parts := make([]*data.Column, 0, len(ops))
	var size int32
	for k, c := range ops {
		if c.DType().ID() == dtype.TypeList {
			elems, lo, err := listElems(c, elem)
			if err != nil {
				return nil, err
			}
			srcs[k] = source{list: c, lo: lo, base: size}
			parts = append(parts, elems)
			size += int32(elems.Len())
			continue
		}
		if c.DType() != elem {
			var err error
			if c, err = Cast(c.Name(), elem, true, c); err != nil {
				return nil, err
			}
		}
		srcs[k] = source{val: c, base: size}
		parts = append(parts, c)
		size += int32(c.Len())
	}
	pool, err := ConcatColumns(name, parts)
	if err != nil {
		return nil, err
	}
	var pick []int32
	offs := make([]int32, 1, n+1)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		ok := true
		for _, s := range srcs {
			if s.list != nil && !s.list.IsValid(rowOf(s.list, i)) {
				ok = false
				break
			}
		}
		if ok {
			for _, s := range srcs {
				if s.list == nil {
					pick = append(pick, s.base+int32(rowOf(s.val, i)))
					continue
				}
				st, e, _ := s.list.Lists().Get(rowOf(s.list, i))
				for j := st; j < e; j++ {
					pick = append(pick, s.base+j-s.lo)
				}
			}
		}
		offs = append(offs, int32(len(pick)))
		valid.Append(ok)
	}
	child, err := Take(pool, pick)
	if err != nil {
		return nil, err
	}
	return data.NewList(name, offs, child, valid.Finish()), nil
}
