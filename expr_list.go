package ursus

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// ListExpr is the list namespace: `Col("tags").List().Len()`.
//
// The same wrapper shape `.str` and `.dt` use, for the reason ursus-api.md §4.5
// gives: methods return Expr, so chaining continues naturally through it.
//
// # Two halves, told apart by what they return
//
// Len, Get, Contains, Min, Max, Sum, Mean, NUnique, ArgMin and ArgMax answer a
// question ABOUT a list and hand back an ordinary column — a length, an element, a
// total, a position.
//
// Reverse, Head, Tail, Slice, Sort, Unique and DropNulls RESHAPE the list and hand
// back a list, so they chain into each other and into the first half:
// `Sort().Head(3).Get(0)`. The element type is unchanged by all seven — reshaping
// a list cannot change what it is a list OF.
//
// The set operations and Gather take a SECOND list, a column like the first, and
// hand back a list (step 223).
//
// # A null list is not an empty list
//
// It is the distinction the whole List type is careful about, and it survives
// into every method:
//
//	                Len()  Sum()  Min()  Get(0)  Contains(x)
//	[1, 2]            2      3      1      1      as asked
//	[]  (empty)       0     null   null   null      false
//	null (list)      null   null   null   null      null
//
// An empty list HAS a length, and it is zero. A null list has no length at all.
// An empty list contains nothing, which is a false answer to a real question; a
// null list cannot answer it. Working from "both are empty" gives three wrong
// cells here, none of which errors.
//
// The aggregates are where the two agree, and that is a choice: polars gives 0 for
// the sum of an empty list, ursus gives null, because ursus's Sum gives null for a
// group with no values. Agreeing with the aggregate beside it beats agreeing with
// polars.
type ListExpr struct{ e Expr }

// List opens the list namespace.
func (e Expr) List() ListExpr { return ListExpr{e} }

func (l ListExpr) call(fn expr.CallFn, args ...any) Expr {
	nodes := make([]expr.Node, 0, len(args)+1)
	nodes = append(nodes, l.e.node())
	for _, a := range args {
		nodes = append(nodes, litNode(a))
	}
	return wrap(&expr.Call{Fn: fn, Args: nodes})
}

// Len is the number of elements, as a Uint32 — matching `.str.len_bytes`, since a
// length is never negative and never needs 64 bits.
//
// Null ELEMENTS are counted: they occupy a slot, so `[1, null]` has length 2. A
// null LIST has no length and gives null.
func (l ListExpr) Len() Expr { return l.call(expr.FnListLen) }

// Get returns the element at i, or null if there is none there.
//
// A negative index counts from the END, the convention every dataframe library
// uses and the one `.str.slice` already follows here.
//
// Out of range is null rather than an error. Lists are ragged by nature, so
// asking for element 3 of a column whose rows mostly hold two is an ordinary
// question — erroring would make `Get` unusable on exactly the data it is for.
//
// i is a constant, or an expression of an integer type, one index a row (step
// 254), null where it is null. Polars' list.get(expr) errs out of range unless
// null_on_oob; here it is null either way, as for a constant.
func (l ListExpr) Get[I int | Expr](i I) Expr {
	if x, ok := any(i).(Expr); ok {
		return wrap(&expr.Call{Fn: expr.FnListGetExpr, Args: []expr.Node{l.e.node(), x.node()}})
	}
	return l.call(expr.FnListGet, int64(any(i).(int)))
}

// First is Get(0) and Last is Get(-1).
func (l ListExpr) First() Expr { return l.Get(0) }
func (l ListExpr) Last() Expr  { return l.Get(-1) }

// Contains reports whether the list holds v.
//
// The comparison is ursus's, not Go's: values go through the same group-key
// encoder `IsIn` uses, so NaN matches NaN and -0.0 matches +0.0, consistently
// with how GroupBy and Sort treat them.
//
// The element and v meet as == would compare them, as IsIn's values do: v of a type
// == refuses is refused, and `Contains(5000)` against Int8 elements is false.
//
// v may be an expression, one value a row (step 254). A null value there is found
// where the list holds a null, as Polars' list.contains finds it.
func (l ListExpr) Contains(v any) Expr {
	if x, ok := v.(Expr); ok {
		return wrap(&expr.Call{Fn: expr.FnListContainsExpr, Args: []expr.Node{l.e.node(), x.node()}})
	}
	return l.call(expr.FnListContains, v)
}

// Eval runs body over each list's elements, the list its group: Polars' list.eval
// (step 268). Element() is the elements; the body reads nothing else.
//
//	Col("l").List().Eval(Element().Mul(2))                  // each element doubled
//	Col("l").List().Eval(Element().Sub(Element().Mean()))   // each list centred
//	Col("l").List().Eval(Element().Rank(RankAverage, false)) // ranked within its list
//
// An expression of a value an element keeps each list's length; a constant is
// repeated along it. An aggregate is a list of one, its value; a reshape or a filter
// is the list it makes. A window function, rank or cum_sum, is computed a list at a
// time. A null list stays null. As the list functions here, the sum of an empty list
// is null, where Polars' is 0.
func (l ListExpr) Eval(body Expr) Expr {
	return wrap(expr.LowerListEval(l.e.node(), body.node(), false))
}

// Agg is Eval whose aggregate is its value, not a list of one: Polars' list.agg.
//
//	Col("l").List().Agg(Element().Filter(Element().Gt(0)).Sum())   // each list's positive sum
func (l ListExpr) Agg(body Expr) Expr {
	return wrap(expr.LowerListEval(l.e.node(), body.node(), true))
}

// Filter keeps each list's elements pred is true on: Polars' list.filter, Eval of
// Element().Filter(pred). A null predicate drops the element, as Filter drops a row.
func (l ListExpr) Filter(pred Expr) Expr { return l.Eval(Element().Filter(pred)) }

// Element is a list's elements, inside List().Eval, Agg and Filter: Polars'
// pl.element(). Anywhere else it is refused.
func Element() Expr { return wrap(&expr.Element{}) }

// Min and Max reduce each list to its smallest or largest element, keeping the
// element type. Null elements are skipped, exactly as the column-wide aggregates
// skip null rows.
func (l ListExpr) Min() Expr { return l.call(expr.FnListMin) }
func (l ListExpr) Max() Expr { return l.call(expr.FnListMax) }

// Sum totals each list.
//
// It widens an integer element to Int128, because the column-wide Sum does and a
// per-row sum that overflowed where that one does not would be the worse
// surprise. The rule is not restated here — the type comes from the aggregate's
// own binding, so the two cannot drift.
//
// An empty list sums to NULL, not zero — the same answer ursus's Sum gives for a
// group with no values. polars differs here.
func (l ListExpr) Sum() Expr { return l.call(expr.FnListSum) }

// Mean averages each list, as a Float64. Null elements are skipped, so the
// divisor is the count of PRESENT elements.
func (l ListExpr) Mean() Expr { return l.call(expr.FnListMean) }

// NUnique counts each list's distinct elements, as a Uint64, as the column-wide
// NUnique counts a group's: null elements are skipped, NaN is one value, and −0 and
// +0 are one. An empty list counts 0, a null list is null.
//
// Polars counts a null element as one more value, and answers a UInt32: [3, null, 1,
// 1, null] is 3 there and 2 here. Agreeing with the aggregate beside it is the rule
// Sum states.
func (l ListExpr) NUnique() Expr { return l.call(expr.FnListNUnique) }

// ArgMin and ArgMax are the position in each list of its smallest or largest
// element, as a Uint64, as the column-wide ArgMin and ArgMax give a group's: the
// first of equal elements, counting null elements as positions but never choosing
// one. An empty list, a list of nulls, and a null list are null. The element type
// must have an order.
//
// NaN is the largest value, as it is for Max: [2.0, NaN, 1.0] has its ArgMax at 1.
// Polars skips NaN there, answering 0, and its positions are UInt32s.
func (l ListExpr) ArgMin() Expr { return l.call(expr.FnListArgMin) }
func (l ListExpr) ArgMax() Expr { return l.call(expr.FnListArgMax) }

// Reverse puts each list's elements in the opposite order. Null elements move with
// everything else — they occupy a slot.
func (l ListExpr) Reverse() Expr { return l.call(expr.FnListReverse) }

// Head keeps the first n elements of each list, Tail the last n.
//
// Asking for more than a row holds returns the whole row, not an error and not a
// padded list. Lists are ragged by nature, so asking for three tags from a column
// whose rows mostly have two is an ordinary question — the same reasoning Get uses
// for an index past the end.
//
// A negative n is refused: "the first -2 elements" has no reading worth guessing
// at. Slice is where a negative number means something.
func (l ListExpr) Head(n int) Expr { return l.call(expr.FnListHead, int64(n)) }

// Tail keeps the last n elements. See Head.
func (l ListExpr) Tail(n int) Expr { return l.call(expr.FnListTail, int64(n)) }

// Slice takes length elements from offset.
//
// A negative OFFSET counts from the end, matching Get and `.str.slice`; the
// clamping is `.str.slice`'s too, so an offset past either edge lands in range and
// a length running off the end stops there. A negative length is refused.
func (l ListExpr) Slice(offset, length int) Expr {
	return l.call(expr.FnListSlice, int64(offset), int64(length))
}

// Sort orders each list's elements ascending; SortDesc orders them descending.
//
// The ordering is ursus's own — the same comparator a column-wide Sort uses — so a
// list of values and a column of the same values order identically. That includes
// null placement: nulls come FIRST, matching the default of `Asc()`. Both
// placements are defensible and this is the one ursus picked.
//
// Equal elements keep their input order.
func (l ListExpr) Sort() Expr     { return l.call(expr.FnListSort, false) }
func (l ListExpr) SortDesc() Expr { return l.call(expr.FnListSort, true) }

// SortWith sorts each list in either direction with its nulls first or last: Polars'
// list.sort(descending, nulls_last) (step 269).
func (l ListExpr) SortWith(descending, nullsLast bool) Expr {
	return l.call(expr.FnListSort, descending, nullsLast)
}

// Concat is each list followed by others' elements, or their values: Polars'
// list.concat, ConcatList(l, others...). A null list among them makes the row's null.
func (l ListExpr) Concat(others ...Expr) Expr { return ConcatList(append([]Expr{l.e}, others...)...) }

// Shift moves each list's elements n places later, nulls in the places they leave, a
// negative n earlier: Polars' list.shift. n is a constant or an expression, a count a
// row; a null count gives a null list.
func (l ListExpr) Shift[N Operand](n N) Expr {
	return wrap(&expr.Call{Fn: expr.FnListShift, Args: []expr.Node{l.e.node(), lift(n)}})
}

// Diff is each element less the one n places before it, null for the first n:
// Polars' list.diff, its null_behavior "ignore"; with dropNulls, "drop", those first
// n are dropped. It is Eval(Element().Diff(n)).
func (l ListExpr) Diff(n int, dropNulls bool) Expr {
	d := Element().Diff(n)
	if dropNulls {
		d = d.Slice(n, math.MaxInt32)
	}
	return l.Eval(d)
}

// CountMatches is how many of each list's elements equal v, a UInt32: Polars'
// list.count_matches. v is a value, nil for null, which counts the null elements as
// Polars counts them, or an expression, a value a row; they meet as == compares.
func (l ListExpr) CountMatches(v any) Expr {
	var arg expr.Node
	switch x := v.(type) {
	case nil:
		arg = &expr.Lit{DT: dtype.Null}
	case Expr:
		arg = x.node()
	default:
		arg = litNode(x)
	}
	return wrap(&expr.Call{Fn: expr.FnListCountMatches, Args: []expr.Node{l.e.node(), arg}})
}

// Any and All are whether some, or every, element of a List of Booleans is true:
// Polars' list.any and list.all. With ignoreNulls, Polars' default, a null element is
// skipped, so an empty list is false for Any and true for All; without it they are
// Kleene's, all of a true and a null null.
func (l ListExpr) Any(ignoreNulls bool) Expr { return l.call(expr.FnListAny, ignoreNulls) }
func (l ListExpr) All(ignoreNulls bool) Expr { return l.call(expr.FnListAll, ignoreNulls) }

// Item is each list's one element: Polars' list.item. A list of another length is
// refused, but an empty one with allowEmpty, which is null.
func (l ListExpr) Item(allowEmpty bool) Expr { return l.call(expr.FnListItem, allowEmpty) }

// SampleN is n of each list's elements at random, and SampleFraction the fraction of
// its length, rounded down: Polars' list.sample. Without replacement a list shorter
// than the sample is refused, as Polars refuses it, and the sample keeps the list's
// order unless shuffle.
//
// The draw is seeded by seed and the list's own elements, so it does not depend on how
// the rows are batched and two equal lists draw alike. Polars draws a seed when none
// is given; here it is always given. The values are not Polars', whose generator is
// its own.
func (l ListExpr) SampleN(n int, withReplacement, shuffle bool, seed uint64) Expr {
	if n < 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "list.sample", "list.sample takes %d elements", n)})
	}
	return l.call(expr.FnListSample, int64(n), 0.0, withReplacement, shuffle, int64(seed))
}

func (l ListExpr) SampleFraction(fraction float64, withReplacement, shuffle bool, seed uint64) Expr {
	if fraction < 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "list.sample", "list.sample takes a fraction of %g", fraction)})
	}
	return l.call(expr.FnListSample, int64(-1), fraction, withReplacement, shuffle, int64(seed))
}

// Unique drops repeated elements, keeping the FIRST occurrence and leaving the
// order alone — sorting as a side effect would make `Unique().Head(2)` mean
// something different from `Head(2)` on the same data.
//
// Equality is ursus's, as it is for Contains: NaN dedupes against NaN. A null
// element counts as a value here, so a list of three nulls keeps one.
func (l ListExpr) Unique() Expr { return l.call(expr.FnListUnique) }

// DropNulls removes the null ELEMENTS from each list. A null LIST stays null —
// there is nothing to drop from, which is not the same as having dropped
// everything.
func (l ListExpr) DropNulls() Expr { return l.call(expr.FnListDropNulls) }

// Join joins each list's strings with sep, skipping null elements, as Polars'
// list.join does by default: ["a", null, "b"] joins with "-" to "a-b". An empty list
// joins to "", and a null list is null. The list must be of String; cast it first
// otherwise, e.g. .Cast(List(String)).
func (l ListExpr) Join(sep string) Expr { return l.call(expr.FnListJoin, sep) }

// operand is a function of this list and a second one.
func (l ListExpr) operand(fn expr.CallFn, other Expr) Expr {
	return wrap(&expr.Call{Fn: fn, Args: []expr.Node{l.e.node(), other.node()}})
}

// SetUnion, SetIntersection, SetDifference and SetSymmetricDifference treat each
// row's two lists as sets, as Polars' set_union and its siblings do:
//
//   - SetUnion is this list's distinct elements in the order they first appear,
//     then other's that are not among them;
//   - SetIntersection is this list's distinct elements that other holds, in this
//     list's order, and SetDifference those it does not;
//   - SetSymmetricDifference is SetDifference, then other's distinct elements this
//     list does not hold, in other's order.
//
// Elements are equal as group_by keys are: a null element is a value, equal to
// another null; NaN equals NaN; −0 equals +0. A null list on either side gives a null
// list. The two element types meet as a join's keys do: an Int64 and a Float64 are
// refused, where Polars meets them at Float64 and compares rounded integers.
func (l ListExpr) SetUnion(other Expr) Expr { return l.operand(expr.FnListSetUnion, other) }
func (l ListExpr) SetIntersection(other Expr) Expr {
	return l.operand(expr.FnListSetIntersection, other)
}
func (l ListExpr) SetDifference(other Expr) Expr {
	return l.operand(expr.FnListSetDifference, other)
}
func (l ListExpr) SetSymmetricDifference(other Expr) Expr {
	return l.operand(expr.FnListSetSymmetricDifference, other)
}

// Gather picks each list's elements at the positions in indices, a List of
// integers a row, as Polars' list.gather does: a negative index counts from the end,
// and a null index gives a null element. An index out of range fails the query, or
// with nullOnOOB gives a null element. A null list, or null indices, give a null
// list.
func (l ListExpr) Gather(indices Expr, nullOnOOB bool) Expr {
	if nullOnOOB {
		return l.operand(expr.FnListGatherOrNull, indices)
	}
	return l.operand(expr.FnListGather, indices)
}

// ToStruct makes each list a Struct whose fields are its first elements, named by
// names: field i is element i, null where the list is shorter, and the elements past
// the last field are dropped. A null list is a null struct, where Polars gives a
// struct of nulls. At least one name, each different. Polars' to_struct with its
// fields named; the names are needed because a plan's schema cannot wait for the
// data to say how long the lists are.
func (l ListExpr) ToStruct(names ...string) Expr {
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = n
	}
	return l.call(expr.FnListToStruct, args...)
}

// Median is each list's median, the column aggregate over its elements (step 253).
func (l ListExpr) Median() Expr { return l.call(expr.FnListMedian) }

// Std is each list's standard deviation with ddof, as the aggregate's (step 253).
func (l ListExpr) Std(ddof int) Expr { return l.call(expr.FnListStd, int64(ddof)) }

// Var is each list's variance with ddof, as the aggregate's (step 253).
func (l ListExpr) Var(ddof int) Expr { return l.call(expr.FnListVar, int64(ddof)) }

// Quantile is each list's q-quantile, interpolated as the aggregate's (step 253).
func (l ListExpr) Quantile(q float64, interp Interpolation) Expr {
	return l.call(expr.FnListQuantile, q, int64(interp))
}

// DropNans drops each list's NaN elements; a null stays (step 253).
func (l ListExpr) DropNans() Expr { return l.call(expr.FnListDropNans) }

// GatherEvery is every n-th element of each list, from offset (step 253).
func (l ListExpr) GatherEvery(n, offset int) Expr {
	return l.call(expr.FnListGatherEvery, int64(n), int64(offset))
}

// SortBy sorts each list by the list beside it, element for element, ascending,
// nulls first, stably (step 253). Two lists of a row must be of one length.
func (l ListExpr) SortBy(by Expr) Expr { return l.operand(expr.FnListSortBy, by) }

// SortByDesc is SortBy in descending order, nulls first.
func (l ListExpr) SortByDesc(by Expr) Expr { return l.operand(expr.FnListSortByDesc, by) }
