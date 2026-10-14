package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// Membership and indexing whose argument is a column (step 254, E5 of
// v0.6.5-scope.md): is_in against each row's own list, and list.get and
// list.contains with a per-row index and value. The constant forms (listfn.go,
// InSet) are unchanged; these read their argument a row at a time, each operand a
// column of the batch's height or a literal one row long.
//
// Values meet as == would compare them, the constant forms' rule, and are compared
// by their group-key encoding at that type, so NaN matches NaN and -0.0 matches
// +0.0 as everywhere else.

// setAbove is the list size past which a one-row list, a literal against every
// row, is put in a hash set once rather than scanned a row at a time.
const setAbove = 16

// isInEach is x.is_in(list), each row's value in the row's own list: null where the
// value or the list is, as Polars' is_in with nulls_equal=False is, and false for a
// value its list does not hold, a null element too.
func isInEach(name string, x, list *data.Column, n int) (*data.Column, error) {
	if x.DType().IsNull() {
		return NullColumn(name, dtype.Bool, n)
	}
	b, err := expr.ResolveBinary(expr.OpEq, x.DType(), list.DType().Inner())
	if err != nil {
		return nil, err
	}
	xc := x
	if xc.DType() != b.CastL {
		if xc, err = Cast(x.Name(), b.CastL, true, x); err != nil {
			return nil, err
		}
	}
	elems, lo, err := listElems(list, b.CastL)
	if err != nil {
		return nil, err
	}
	xenc, err := NewGroupKeyEncoder("is_in", []*data.Column{xc})
	if err != nil {
		return nil, err
	}
	eenc, err := NewGroupKeyEncoder("is_in", []*data.Column{elems})
	if err != nil {
		return nil, err
	}
	ll := list.Lists()
	find := scanFor(ll, elems, eenc, lo, list.Len() == 1 && n > 1)
	bits := bitmap.NewBuilder(n)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		r := rowOf(xc, i)
		s, e, ok := ll.Get(rowOf(list, i))
		ok = ok && xc.IsValid(r)
		valid.Append(ok)
		bits.Append(ok && find(s, e, xenc.Encode(r)))
	}
	return data.NewBool(name, bits.Finish(), valid.Finish()), nil
}

// listContainsEach is list.contains(v) a row at a time: null where the list is, and
// a null value found where the list holds a null, as Polars' list.contains is.
func listContainsEach(name string, list, v *data.Column, n int) (*data.Column, error) {
	elem := list.DType().Inner()
	meet := elem
	if !v.DType().IsNull() {
		b, err := expr.ResolveBinary(expr.OpEq, elem, v.DType())
		if err != nil {
			return nil, err
		}
		meet = b.CastL
	}
	vc := v
	if vc.DType() != meet {
		var err error
		if vc, err = Cast(v.Name(), meet, true, v); err != nil {
			return nil, err
		}
	}
	elems, lo, err := listElems(list, meet)
	if err != nil {
		return nil, err
	}
	venc, err := NewGroupKeyEncoder("list.contains", []*data.Column{vc})
	if err != nil {
		return nil, err
	}
	eenc, err := NewGroupKeyEncoder("list.contains", []*data.Column{elems})
	if err != nil {
		return nil, err
	}
	ll := list.Lists()
	// A null encodes to its own tag, which a null element's encoding equals, so the
	// one comparison finds a null value among nulls.
	find := scanFor(ll, elems, eenc, lo, list.Len() == 1 && n > 1)
	bits := bitmap.NewBuilder(n)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		s, e, ok := ll.Get(rowOf(list, i))
		valid.Append(ok)
		bits.Append(ok && find(s, e, venc.Encode(rowOf(vc, i))))
	}
	return data.NewBool(name, bits.Finish(), valid.Finish()), nil
}

// scanFor is a lookup of an encoded value among the elements s..e of a list, its
// elements' window starting at lo. With once, the list is one row against every row,
// and a long one goes into a hash set the first time it is asked.
func scanFor(ll data.ListAccessor, elems *data.Column, enc *GroupKeyEncoder, lo int32,
	once bool) func(s, e int32, key []byte) bool {

	scan := func(s, e int32, key []byte) bool {
		for j := s - lo; j < e-lo; j++ {
			if string(enc.Encode(int(j))) == string(key) {
				return true
			}
		}
		return false
	}
	if !once {
		return scan
	}
	var set map[string]struct{}
	return func(s, e int32, key []byte) bool {
		if e-s <= setAbove {
			return scan(s, e, key)
		}
		if set == nil {
			set = make(map[string]struct{}, e-s)
			for j := s - lo; j < e-lo; j++ {
				set[string(enc.Encode(int(j)))] = struct{}{}
			}
		}
		_, ok := set[string(key)]
		return ok
	}
}

// listGetEach is list.get(i) with a per-row index: from the end where it is
// negative, and null where the list, the index or an element there is not, as the
// constant form is.
func listGetEach(name string, list, idx *data.Column, n int) (*data.Column, error) {
	ic := idx
	if ic.DType() != dtype.Int64 {
		var err error
		if ic, err = Cast(idx.Name(), dtype.Int64, true, idx); err != nil {
			return nil, err
		}
	}
	at := data.MustValues[int64](ic)
	ll := list.Lists()
	sel := make([]int32, n)
	for i := range n {
		r := rowOf(ic, i)
		s, e, ok := ll.Get(rowOf(list, i))
		if !ok || !ic.IsValid(r) {
			sel[i] = NullIndex
			continue
		}
		size := int64(e - s)
		k := at[r]
		if k < 0 {
			k += size
		}
		if k < 0 || k >= size {
			sel[i] = NullIndex
			continue
		}
		sel[i] = s + int32(k)
	}
	out, err := Take(ll.Child(), sel)
	if err != nil {
		return nil, err
	}
	return out.Rename(name), nil
}
