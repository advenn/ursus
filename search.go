package ursus

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Search, peaks and hashing (step 279, M3 of v0.6.6-scope.md). SearchSorted, IndexOf
// and the peaks compare in the total order Sort uses, NaN above every number and -0
// with 0, as Polars' do; ursus's Lt and Eq are IEEE's. Each is written in aggregates
// and window functions there were, so each answers in a Select, a group's aggregation
// and a window alike.

// orderCmp is -1, 0 or 1 a row as a sorts before, with or after b.
func orderCmp(a, b expr.Node) Expr { return wrap(horizontal(expr.FnOrderCmp, a, b)) }

// operandOf is a value's node: an Expr's, or a Go value's literal.
func operandOf(v any) expr.Node {
	if x, ok := v.(Expr); ok {
		return x.node()
	}
	return litNode(v)
}

// SearchSide is where SearchSorted places a value among those equal to it.
type SearchSide string

const (
	SideAny   SearchSide = "any" // anywhere among its equals: the first, here
	SideLeft  SearchSide = "left"
	SideRight SearchSide = "right"
)

// SearchSorted is the index at which value would be inserted into the column, sorted
// ascending, or descending, to keep it sorted, a UInt32: Polars' search_sorted, its
// value a Go value or a null. It is an aggregate, so an Expr holding one, such as
// Max(), cannot be the value, as an aggregate is not taken inside another's input; a
// column of values is not one value either, where Polars searches for each. SideLeft
// is before its equals and SideRight after them; SideAny is SideLeft.
//
// Its nulls are where Polars' are: first, where the first value is null, else last.
// A null value goes before them, or after them with SideRight; where there is no
// null it is 0. Over an unsorted column it is the count of values that sort before
// value, where Polars' binary search lands where it lands.
func (e Expr) SearchSorted(value any, side SearchSide, descending bool) Expr {
	if side != SideAny && side != SideLeft && side != SideRight {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "search_sorted", "%q is not a side", side).
			Hint("SideAny, SideLeft or SideRight")})
	}
	left := side != SideRight
	nulls := e.NullCount()
	nullsFirst := e.First().IsNull()
	if value == nil {
		var first, last Expr = Lit(int64(0)), e.Count()
		if !left {
			first, last = nulls, e.Len()
		}
		return When(nulls.Eq(0)).Then(Lit(int64(0))).When(nullsFirst).Then(first).Otherwise(last).Cast(Uint32)
	}
	c := orderCmp(e.node(), operandOf(value))
	var before Expr
	switch {
	case !descending && left:
		before = c.Eq(-1)
	case !descending:
		before = c.Le(0)
	case left:
		before = c.Eq(1)
	default:
		before = c.Ge(0)
	}
	count := before.FillNullWith(false).Sum().FillNullWith(0)
	return count.Add(When(nullsFirst).Then(nulls).Otherwise(Lit(int64(0)))).Cast(Uint32)
}

// IndexOf is the index of the first value equal to value, a UInt32, null where there
// is none: Polars' index_of. value is a Go value, or a null, which finds the first
// null; an Expr holding an aggregate cannot be, as SearchSorted's cannot. NaN finds
// NaN and 0 finds -0, as Polars finds them.
func (e Expr) IndexOf(value any) Expr {
	var hit Expr
	if value == nil {
		hit = e.IsNull()
	} else {
		hit = orderCmp(e.node(), operandOf(value)).Eq(0).FillNullWith(false)
	}
	return When(hit.Any()).Then(hit.ArgMax().Cast(Uint32)).Otherwise(Null(Uint32))
}

// PeakMax is whether each value is greater than both its neighbours, and PeakMin
// whether it is less, a Boolean: Polars' peak_max and peak_min. A value at an end is
// compared with its type's zero there, as Polars fills its shifts with 0, so the
// first of [3, 1] is a peak and of [-1, -2] is not; a null neighbour makes it null.
// Text is refused, as Polars refuses it. A group's own peaks are .Over(key), where
// Polars' group_by gives each group's as a list; an aggregation takes aggregates.
func (e Expr) PeakMax() Expr { return e.peak(1) }
func (e Expr) PeakMin() Expr { return e.peak(-1) }

func (e Expr) peak(want int64) Expr {
	side := func(n int) Expr {
		// An end's shift is null where the column's is not: IsNull is never null.
		end := e.IsNull().Shift(n).IsNull()
		sign := wrap(&expr.Call{Fn: expr.FnOrderSign, Args: []expr.Node{e.node()}})
		return When(end).Then(sign).Otherwise(orderCmp(e.node(), e.Shift(n).node()))
	}
	return side(1).Eq(want).And(side(-1).Eq(want))
}

// RleID is each row's run's number, from 0, a run being rows of equal values, a
// UInt32: Polars' rle_id. NaN runs with NaN, -0 with 0 and a null with a null, as
// Polars runs them. It is a window function, over the column or .Over(key) a
// partition's own; Polars' group_by gives each group's as a list. A Struct cannot be
// a key here and is refused, where Polars runs it.
func (e Expr) RleID() Expr { return e.winFn(expr.WinRleID, expr.WinParams{}) }

// Rle is each run of equal values' length and value, a Struct of len, a UInt32, and
// value: Polars' rle, a column of its own length in a Select and a list a group in an
// aggregation. Its runs are RleID's.
func (e Expr) Rle() Expr { return e.reshape(expr.ReshapeRle) }

// Hash is each value's 64-bit hash, seeded, a UInt64: Polars' hash. Values a
// group-by takes as one hash alike, -0 with 0 and NaN with NaN, and a null has a hash
// of its own. The algorithm is ursus's, so the numbers are not Polars'
// (v0.6.6-scope.md §6.3).
func (e Expr) Hash(seed uint64) Expr {
	return wrap(&expr.Call{Fn: expr.FnHash, Args: []expr.Node{e.node(), litNode(int64(seed))}})
}
