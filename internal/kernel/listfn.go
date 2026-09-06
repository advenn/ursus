package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// List kernels: the `.list` namespace, reducing a list to a scalar.
//
// # A null list is not an empty list, and that is the whole difficulty
//
// The two look identical from inside: both hold no elements, and the offsets do
// not move for either. Only the row's validity bit separates them. Every function
// here has to keep them apart, and they differ in a way that is easy to get
// backwards:
//
//	                len   sum   min   get(0)   contains(x)
//	[1, 2]           2     3     1      1        as asked
//	[]  (empty)      0    null  null   null       false
//	null (list)     null  null  null   null       null
//
// An empty list HAS a length, and it is zero; a null list has no length at all.
// And an empty list CONTAINS nothing, which is a false answer to a real question,
// where a null list cannot answer it at all.
//
// The AGGREGATES are the row where the two agree, and that is deliberate. polars
// answers 0 for the sum of an empty list; ursus answers null, because ursus's own
// Sum answers null for a group with no values. A `.list.sum()` that disagreed with
// the `Sum()` beside it would be the worse surprise — a user who knew one would be
// wrong about the other.
//
// Null ELEMENTS are a third thing again: they occupy a slot, so len counts them,
// and the aggregates skip them exactly as the column-wide aggregates skip null
// rows.

// ListCall applies a list function to a column.
//
// The signature mirrors StrCall, and out comes from expr.ResolveCall so the
// kernel and the plan cannot disagree about the result type.
func ListCall(fn expr.CallFn, name string, out dtype.DataType,
	c *data.Column, args []any, needle []byte) (*data.Column, error) {

	if c.DType().ID() != dtype.TypeList {
		return nil, uerr.Internalf("kernel: %s on a %s column", fn, c.DType())
	}
	acc := c.Lists()
	n := c.Len()

	switch fn {
	case expr.FnListLen:
		vals := make([]uint32, n)
		valid := bitmap.NewBuilder(n)
		for i := range n {
			start, end, ok := acc.Get(i)
			valid.Append(ok) // a null list has no length
			if ok {
				vals[i] = uint32(end - start)
			}
		}
		return data.NewFixed(name, dtype.Uint32, vals, valid.Finish()), nil

	case expr.FnListGet:
		return listGet(name, c, args)

	case expr.FnListContains:
		return listContains(name, c, needle)

	case expr.FnListMin, expr.FnListMax, expr.FnListSum, expr.FnListMean:
		return listReduce(fn, name, out, c)
	}
	return nil, uerr.Internalf("kernel: unknown list call %s", fn)
}

// listGet picks one element per row, by index.
//
// A negative index counts from the END, which is the convention every dataframe
// library uses and the same one str.slice already follows. Out of range is null
// rather than an error: a ragged column is the normal case for lists, so asking
// for element 3 of rows that mostly have two is a question, not a mistake.
//
// It is expressed as a Take over computed indices so the element type needs no
// switch — NullIndex covers the null list, the empty list and the out-of-range
// row, and Take turns all three into a null.
func listGet(name string, c *data.Column, args []any) (*data.Column, error) {
	idx, ok := argInt(args, 0)
	if !ok {
		return nil, uerr.Internalf("kernel: list.get has no index argument")
	}
	acc := c.Lists()
	sel := make([]int32, c.Len())
	for i := range c.Len() {
		start, end, present := acc.Get(i)
		length := int64(end - start)
		at := idx
		if at < 0 {
			at += length
		}
		if !present || at < 0 || at >= length {
			sel[i] = NullIndex
			continue
		}
		sel[i] = start + int32(at)
	}
	out, err := Take(acc.Child(), sel)
	if err != nil {
		return nil, err
	}
	return out.Rename(name), nil
}

// listContains reports whether each row holds the value.
//
// A null list answers null: it is not that the value is absent, it is that there
// is no list to look in. An EMPTY list answers false, because looking in it is a
// well-defined thing to do and the answer is no.
//
// A null ELEMENT never matches — a null is the absence of a value, not a value
// that might equal the one sought.
//
// # Comparison goes through the group-key encoder
//
// The same machinery is_in uses, for the same reason: it is the single authority
// on value equality, and it puts floats through OrderKey first, so NaN matches
// NaN and -0.0 matches +0.0 exactly as GroupBy and Sort treat them. Comparing raw
// bits would give IEEE's answer instead, and a `.list.contains(NaN)` that is
// always false would be a defensible-looking bug.
//
// needle is the encoded value, built by the evaluator through kernel.EncodeOne
// with a STRICT cast to the element type — the same preparation buildInSet does,
// so `contains(5000)` against Int8 elements reports that 5000 does not fit rather
// than silently matching nothing.
func listContains(name string, c *data.Column, needle []byte) (*data.Column, error) {
	acc := c.Lists()
	child := acc.Child()

	enc, err := NewGroupKeyEncoder("list.contains", []*data.Column{child})
	if err != nil {
		return nil, err
	}

	bits := bitmap.NewBuilder(c.Len())
	valid := bitmap.NewBuilder(c.Len())
	for i := range c.Len() {
		start, end, present := acc.Get(i)
		valid.Append(present)
		found := false
		if present {
			for e := start; e < end; e++ {
				if child.IsValid(int(e)) && string(enc.Encode(int(e))) == string(needle) {
					found = true
					break
				}
			}
		}
		bits.Append(found)
	}
	return data.NewBool(name, bits.Finish(), valid.Finish()), nil
}

// listReduce runs a per-row aggregate by reusing the ACCUMULATORS.
//
// Accumulator.AddBatch takes "groups[i] is the group ordinal of row i", and a
// list's offsets say exactly that: element j belongs to row i. So a per-row
// aggregate is the existing accumulator fed a groups array built from the
// offsets — which means no per-type reduction code at all, and every type the
// column-wide aggregates support works here through the same flat typed storage.
// windowSink.finishAggregate uses them the same way.
//
// # No post-mask is needed, and that was checked
//
// An empty list and a null list both contribute zero elements, so the accumulator
// cannot tell them apart — and it does not have to, because ursus's aggregates
// answer NULL for a group with no values either way. A validity mask was written
// here first and then removed: it provably changed nothing, and dead code with a
// careful justification attached is worse than no code.
//
// It would be needed the moment an accumulator answered 0 for an empty group
// instead. TestListNamespaceSemantics pins both rows for exactly that reason, so
// the equivalence is deliberate rather than something that happens to hold.
func listReduce(fn expr.CallFn, name string, out dtype.DataType,
	c *data.Column) (*data.Column, error) {

	op, err := listAggOp(fn)
	if err != nil {
		return nil, err
	}
	elem := c.DType().Inner()
	bind, err := expr.ResolveAggBinding(op, elem)
	if err != nil {
		return nil, err
	}
	acc, err := NewAccumulator(op, elem, bind, expr.AggParams{})
	if err != nil {
		return nil, err
	}

	lists := c.Lists()
	child := lists.Child()
	groups := make([]int32, child.Len())
	for i := range c.Len() {
		start, end, _ := lists.Get(i)
		for e := start; e < end; e++ {
			groups[e] = int32(i)
		}
	}

	acc.Reserve(c.Len())
	if err := acc.AddBatch(groups, child); err != nil {
		return nil, err
	}
	res, err := acc.Finish(name, c.Len())
	if err != nil {
		return nil, err
	}
	return res, nil
}

func listAggOp(fn expr.CallFn) (expr.AggOp, error) {
	switch fn {
	case expr.FnListMin:
		return expr.AggMin, nil
	case expr.FnListMax:
		return expr.AggMax, nil
	case expr.FnListSum:
		return expr.AggSum, nil
	case expr.FnListMean:
		return expr.AggMean, nil
	}
	return 0, uerr.Internalf("kernel: %s is not a list aggregate", fn)
}
