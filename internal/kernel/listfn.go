package kernel

import (
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// List kernels: the `.list` namespace.
//
// Two halves with different shapes. One REDUCES a list to a scalar — len, get,
// contains, and the aggregates — and one RESHAPES it and hands back a list —
// reverse, head, tail, slice, sort, unique, drop_nulls. The second half is
// listRebuild plus a closure each, because picking which element indices survive
// is the only thing that differs between them.
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
// the aggregates skip them exactly as the column-wide aggregates skip null rows,
// reverse and sort keep them, and drop_nulls is the one function that removes
// them.
//
// For the reshaping half the null list is the EASY case — pick is simply not
// called, so the list stays null without anything having to say so. That only
// holds because listRebuild skips it; see the note there.

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

	case expr.FnListReverse:
		return listRebuild(name, c, func(start, end int32, out []int32) []int32 {
			for e := end - 1; e >= start; e-- {
				out = append(out, e)
			}
			return out
		})

	case expr.FnListHead, expr.FnListTail:
		return listEnds(fn, name, c, args)

	case expr.FnListSlice:
		return listSlice(name, c, args)

	case expr.FnListSort:
		return listSort(name, c, args)

	case expr.FnListUnique:
		return listUnique(name, c)

	case expr.FnListDropNulls:
		child := acc.Child()
		return listRebuild(name, c, func(start, end int32, out []int32) []int32 {
			for e := start; e < end; e++ {
				if child.IsValid(int(e)) {
					out = append(out, e)
				}
			}
			return out
		})
	}
	return nil, uerr.Internalf("kernel: unknown list call %s", fn)
}

// listRebuild is every list -> LIST function in the namespace, once.
//
// Reverse, Head, Tail, Slice, Sort, Unique and DropNulls all do the same thing:
// choose which of a row's element indices survive, and in what order. Nothing
// else differs between them, so pick is the only part each one writes.
//
// A NULL LIST STAYS NULL because c.Validity() is carried through unchanged. That
// is the whole of it, and it is worth being exact about which line does the work:
// removing the `ok` guard below and calling pick for a null row changes NOTHING —
// a null row's element range is empty, so every pick here produces nothing from it
// anyway. That was checked by doing it. The guard is a precondition for whatever
// pick is written next ("you are only asked about lists that exist"), not the
// defence; replacing c.Validity() with an all-set bitmap is the mistake that turns
// every null list into an empty one, and it breaks all seven functions at once.
//
// THE ELEMENT TYPE NEVER APPEARS. Take does the gather, so List(String) works for
// exactly the reason List(Int64) does and a new element type needs nothing here.
func listRebuild(name string, c *data.Column,
	pick func(start, end int32, out []int32) []int32) (*data.Column, error) {

	acc := c.Lists()
	offs := make([]int32, 1, c.Len()+1)
	var sel []int32
	for i := range c.Len() {
		if start, end, ok := acc.Get(i); ok {
			sel = pick(start, end, sel)
		}
		offs = append(offs, int32(len(sel)))
	}
	child, err := Take(acc.Child(), sel)
	if err != nil {
		return nil, err
	}
	return data.NewList(name, offs, child, c.Validity()), nil
}

// listEnds is head and tail: the first or last n elements of each row.
//
// Asking for more elements than a row holds returns the whole row rather than an
// error or a padded list. Lists are ragged by nature — asking for three tags from
// a column whose rows mostly have two is an ordinary question, the same reasoning
// list.get uses for an out-of-range index.
//
// A NEGATIVE n is refused, because there is no reading of "the first -2 elements"
// worth guessing at. list.slice's OFFSET is different and does allow a negative
// value, where it counts from the end.
func listEnds(fn expr.CallFn, name string, c *data.Column, args []any) (*data.Column, error) {
	n, ok := argInt(args, 0)
	if !ok {
		return nil, uerr.Internalf("kernel: %s has no count argument", fn)
	}
	if n < 0 {
		return nil, uerr.New(uerr.KindValue, "list",
			"%s(%d): the number of elements cannot be negative", fn, n).
			Hint("use list.slice for a range measured from the end")
	}
	head := fn == expr.FnListHead
	return listRebuild(name, c, func(start, end int32, out []int32) []int32 {
		// Clamped in int64 before narrowing: start+int32(n) would wrap for a large
		// n, and the wrap would produce a silently empty list.
		k := int32(min(n, int64(end-start)))
		if head {
			end = start + k
		} else {
			start = end - k
		}
		for e := start; e < end; e++ {
			out = append(out, e)
		}
		return out
	})
}

// listSlice takes a sub-range of each row.
//
// The clamping is str.slice's, deliberately: a negative offset counts from the
// end, an offset past either edge clamps into range, and a length running past
// the end stops there. Two slice operations in one library that disagreed about
// the edges would be worse than either rule alone.
func listSlice(name string, c *data.Column, args []any) (*data.Column, error) {
	off, ok := argInt(args, 0)
	length, hasLen := argInt(args, 1)
	if !ok || !hasLen {
		return nil, uerr.Internalf("kernel: list.slice needs an offset and a length")
	}
	if length < 0 {
		return nil, uerr.New(uerr.KindValue, "list",
			"list.slice(%d, %d): the length cannot be negative", off, length)
	}
	return listRebuild(name, c, func(start, end int32, out []int32) []int32 {
		n := int64(end - start)
		at := off
		if at < 0 {
			at += n
		}
		at = max(0, min(at, n))
		stop := n
		if length < n-at {
			stop = at + length
		}
		for e := start + int32(at); e < start+int32(stop); e++ {
			out = append(out, e)
		}
		return out
	})
}

// listSort orders each row's elements.
//
// It uses the same Comparator a column-wide Sort does, which is what makes a list
// of values and a column of the same values order identically — including where
// the nulls land. Nulls come FIRST by default, matching SortSpec's zero value and
// so the public Asc(); it is a choice, not an accident, and both placements are
// defensible.
//
// The comparator is built ONCE for the whole column rather than per row: it
// resolves a type switch into a closure, which is the entire reason it exists.
//
// The sort is STABLE, so elements that compare equal keep their input order. For
// Int64 or String that is unobservable — two elements of one column that compare
// equal ARE the same value — and swapping in the unstable sort passes every test
// here. It stops being unobservable the moment two DISTINGUISHABLE values compare
// equal, and -0.0 against +0.0 already does: the comparator answers 0 for them
// (checked directly), so on a List(Float64) stability is what decides which sign
// comes out first.
func listSort(name string, c *data.Column, args []any) (*data.Column, error) {
	desc, _ := args[0].(bool)
	cmp, err := NewComparator([]*data.Column{c.Lists().Child()},
		[]SortSpec{{Descending: desc}})
	if err != nil {
		return nil, err
	}
	return listRebuild(name, c, func(start, end int32, out []int32) []int32 {
		base := len(out)
		for e := start; e < end; e++ {
			out = append(out, e)
		}
		// Sorted in place inside the selection being built, so there is no scratch
		// slice and nothing to allocate per row.
		slices.SortStableFunc(out[base:], func(a, b int32) int {
			return cmp(int(a), int(b))
		})
		return out
	})
}

// listUnique drops repeats, keeping the FIRST occurrence and the input order.
//
// Keeping the first matches distinctOp's documented rule for whole rows, and
// leaving the order alone matters because sorting as a side effect would make
// unique().head(2) mean something different from head(2) on the same data.
//
// Equality is the group-key encoder's, the same authority is_in and list.contains
// use: floats go through OrderKey first, so NaN dedupes against NaN. A null
// element is a value like any other here — it encodes distinctly, so a row of
// three nulls keeps one.
func listUnique(name string, c *data.Column) (*data.Column, error) {
	enc, err := NewGroupKeyEncoder("list.unique", []*data.Column{c.Lists().Child()})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	return listRebuild(name, c, func(start, end int32, out []int32) []int32 {
		clear(seen) // one map for the column; lists are short
		for e := start; e < end; e++ {
			k := enc.Encode(int(e))
			if _, dup := seen[string(k)]; dup {
				continue
			}
			// Encode aliases its buffer, so the key has to be copied to be kept —
			// which the string conversion in an assignment does.
			seen[string(k)] = struct{}{}
			out = append(out, e)
		}
		return out
	})
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
