package kernel

import (
	"hash/fnv"
	"math/rand/v2"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// More of the list namespace (step 269, L of v0.6.6-scope.md): shift, count_matches,
// any, all, item and sample, Polars' of the same names.

// listShift is list.shift(n): each list's elements n places later, nulls in the
// places they leave, a negative n earlier. n is a constant or a value a row, and a
// null n gives a null list.
func listShift(name string, list, by *data.Column, n int) (*data.Column, error) {
	cnt, err := asInt64(by)
	if err != nil {
		return nil, err
	}
	l := list.Lists()
	var pick []int32
	offs := make([]int32, 1, n+1)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		s, e, ok := l.Get(rowOf(list, i))
		k, kok := int64At(cnt, i)
		ok = ok && kok
		if ok {
			for j := s; j < e; j++ {
				from := int64(j) - k
				if from < int64(s) || from >= int64(e) {
					pick = append(pick, NullIndex)
				} else {
					pick = append(pick, int32(from))
				}
			}
		}
		offs = append(offs, int32(len(pick)))
		valid.Append(ok)
	}
	child, err := Take(l.Child(), pick)
	if err != nil {
		return nil, err
	}
	return data.NewList(name, offs, child, valid.Finish()), nil
}

// listCountMatches is list.count_matches(v): how many of each list's elements equal
// v, as == compares them, a UInt32. A null v counts the null elements, as Polars
// counts them; a null list gives null and an empty one 0.
func listCountMatches(name string, list, v *data.Column, n int) (*data.Column, error) {
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
	venc, err := NewGroupKeyEncoder("list.count_matches", []*data.Column{vc})
	if err != nil {
		return nil, err
	}
	eenc, err := NewGroupKeyEncoder("list.count_matches", []*data.Column{elems})
	if err != nil {
		return nil, err
	}
	l := list.Lists()
	res := make([]uint32, n)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		s, e, ok := l.Get(rowOf(list, i))
		valid.Append(ok)
		if !ok {
			continue
		}
		key := string(venc.Encode(rowOf(vc, i)))
		for j := s - lo; j < e-lo; j++ {
			if string(eenc.Encode(int(j))) == key {
				res[i]++
			}
		}
	}
	return data.NewFixed(name, dtype.Uint32, res, valid.Finish()), nil
}

// listAnyAll is list.any and list.all of Booleans. With ignoreNulls, Polars' default,
// a null element is skipped: any of no true is false, and all of no false is true,
// an empty list's too. Without it they are Kleene's: all of a true and a null is
// null. A null list gives null.
func listAnyAll(fn expr.CallFn, name string, c *data.Column, args []any) (*data.Column, error) {
	ignore, ok := argBool(args, 0)
	if !ok {
		return nil, uerr.Internalf("kernel: %s without ignore_nulls", fn)
	}
	isAny := fn == expr.FnListAny
	l := c.Lists()
	child := l.Child()
	n := c.Len()
	bits := bitmap.NewBuilder(n)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		s, e, ok := l.Get(i)
		if !ok {
			bits.Append(false)
			valid.Append(false)
			continue
		}
		decided, nulls := false, false
		for j := s; j < e && !decided; j++ {
			switch {
			case !child.IsValid(int(j)):
				nulls = true
			case child.Bools().Get(int(j)) == isAny:
				decided = true
			}
		}
		// any: true once a true is seen; all: false once a false is.
		bits.Append(decided == isAny)
		valid.Append(decided || ignore || !nulls)
	}
	return data.NewBool(name, bits.Finish(), valid.Finish()), nil
}

// listItem is list.item: each list's one element. A list of another length is
// refused, naming its row, as Polars refuses it, but an empty one with allowEmpty,
// which is null.
func listItem(name string, c *data.Column, args []any) (*data.Column, error) {
	allowEmpty, ok := argBool(args, 0)
	if !ok {
		return nil, uerr.Internalf("kernel: list.item without allow_empty")
	}
	l := c.Lists()
	sel := make([]int32, c.Len())
	for i := range c.Len() {
		s, e, ok := l.Get(i)
		switch {
		case !ok || e == s && allowEmpty:
			sel[i] = NullIndex
		case e-s != 1:
			return nil, uerr.New(uerr.KindValue, "list.item",
				"list.item takes a list of one element, and row %d has %d", i, e-s).
				Hint("allow_empty makes an empty list null")
		default:
			sel[i] = s
		}
	}
	out, err := Take(l.Child(), sel)
	if err != nil {
		return nil, err
	}
	return out.Rename(name), nil
}

// listSample is list.sample: k of each list's elements at random, k the constant n,
// or the fraction of the list's length, rounded down, where n is negative. Without
// replacement a list shorter than k is refused, as Polars refuses it, and the
// sample keeps the list's order unless shuffled; with replacement an empty list's is
// empty.
//
// Each list draws from a generator seeded by the seed and the list's own elements, so
// the answer does not depend on how the rows are batched, and two equal lists draw
// alike. Polars draws from its own generator: the values cannot be its.
func listSample(name string, c *data.Column, args []any) (*data.Column, error) {
	n, ok1 := argInt(args, 0)
	frac, ok2 := args[1].(float64)
	repl, ok3 := argBool(args, 2)
	shuffle, ok4 := argBool(args, 3)
	seed, ok5 := argInt(args, 4)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return nil, uerr.Internalf("kernel: list.sample without its arguments")
	}
	l := c.Lists()
	child := l.Child()
	enc, err := NewGroupKeyEncoder("list.sample", []*data.Column{child})
	if err != nil {
		return nil, err
	}
	return listRebuildErr(name, c, func(i int, s, e int32, out []int32) ([]int32, error) {
		m := int(e - s)
		k := int(n)
		if n < 0 {
			k = int(frac * float64(m))
		}
		if !repl && k > m {
			return nil, uerr.New(uerr.KindValue, "list.sample",
				"list.sample takes %d of row %d's %d elements without replacement", k, i, m).
				Hint("with replacement a list gives as many as asked")
		}
		h := fnv.New64a()
		for j := s; j < e; j++ {
			h.Write(enc.Encode(int(j)))
		}
		rng := rand.New(rand.NewPCG(uint64(seed), h.Sum64()))
		base := len(out)
		switch {
		case m == 0:
		case repl:
			for range k {
				out = append(out, s+int32(rng.IntN(m)))
			}
		default:
			idx := make([]int32, m)
			for j := range idx {
				idx[j] = s + int32(j)
			}
			for j := range k {
				r := j + rng.IntN(m-j)
				idx[j], idx[r] = idx[r], idx[j]
			}
			out = append(out, idx[:k]...)
		}
		if !shuffle {
			slices.Sort(out[base:])
		}
		return out, nil
	})
}

// listRebuildErr is listRebuild whose selection may fail: pick appends row i's
// chosen elements, by their index in the child.
func listRebuildErr(name string, c *data.Column,
	pick func(i int, s, e int32, out []int32) ([]int32, error)) (*data.Column, error) {

	l := c.Lists()
	var sel []int32
	offs := make([]int32, 1, c.Len()+1)
	for i := range c.Len() {
		s, e, ok := l.Get(i)
		if ok {
			var err error
			if sel, err = pick(i, s, e, sel); err != nil {
				return nil, err
			}
		}
		offs = append(offs, int32(len(sel)))
	}
	child, err := Take(l.Child(), sel)
	if err != nil {
		return nil, err
	}
	return data.NewList(name, offs, child, c.Validity()), nil
}
