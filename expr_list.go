package ursus

import (
	"github.com/advenn/ursus/internal/expr"
)

// ListExpr is the list namespace: `Col("tags").List().Len()`.
//
// The same wrapper shape `.str` and `.dt` use, for the reason ursus-api.md §4.5
// gives: methods return Expr, so chaining continues naturally through it.
//
// # These reduce a list to a SCALAR
//
// Every method here answers a question ABOUT a list and hands back an ordinary
// column — a length, an element, a total. None of them build a list. The
// operations that do (`sort`, `unique`, `reverse`, `slice`, the set operations)
// are a separate problem and are not here yet.
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
	nodes = append(nodes, l.e.n)
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
func (l ListExpr) Get(i int) Expr { return l.call(expr.FnListGet, int64(i)) }

// First is Get(0) and Last is Get(-1).
func (l ListExpr) First() Expr { return l.Get(0) }
func (l ListExpr) Last() Expr  { return l.Get(-1) }

// Contains reports whether the list holds v.
//
// The comparison is ursus's, not Go's: values go through the same group-key
// encoder `IsIn` uses, so NaN matches NaN and -0.0 matches +0.0, consistently
// with how GroupBy and Sort treat them.
//
// The cast to the element type is STRICT. `Contains(int64(5000))` against Int8
// elements reports that 5000 does not fit rather than quietly matching nothing.
func (l ListExpr) Contains(v any) Expr { return l.call(expr.FnListContains, v) }

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
