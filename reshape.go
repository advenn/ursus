package ursus

import "github.com/advenn/ursus/internal/expr"

// Length-changing expressions (step 252).
//
// Each is Polars' expression of the same name: a column of its own length. In
// GroupBy(...).Agg(...) and Rolling and GroupByDynamic it is each group's values
// reshaped, a List a group, and an aggregate over it is one value a group:
//
//	lf.GroupBy(Col("g")).Agg(
//		Col("a").Sort().Alias("sorted"),           // a list a group
//		Col("a").Head(2).Sum().Alias("top2"),       // the first two, summed
//		Col("a").Unique().Len().Alias("distinct"),  // null counts, as a value
//	)
//
// An aggregate over one works in Select, WithColumns, Filter and .Over too, over the
// frame or the window: Select(Col("a").Sort().First()). A reshape on its own outside
// an aggregation, a column of another length beside the frame's, is refused at
// planning until a later step gives it a place; the frame-level forms, lf.Sort,
// lf.Head, lf.Unique and the rest, keep rows.
//
// Each is an implode of the group's values and the List kernel of the same name, so
// a group's values are held, as Polars holds them, and past a memory limit the
// implode refuses rather than spill. Where a List kernel and Polars differ, so does
// this: a sum of no values is null, and Get past the end is null where Polars raises.

func (e Expr) reshape(op expr.ReshapeOp, args ...int64) Expr {
	return wrap(&expr.Reshape{Op: op, Child: e.node(), Args: args})
}

// Sort is the values in ascending order, nulls first, as Polars' sort() puts them.
func (e Expr) Sort() Expr { return e.reshape(expr.ReshapeSort, 0) }

// SortDesc is the values in descending order, nulls first.
func (e Expr) SortDesc() Expr { return e.reshape(expr.ReshapeSort, 1) }

// Head is the first n values.
func (e Expr) Head(n int) Expr { return e.reshape(expr.ReshapeHead, int64(n)) }

// Tail is the last n values.
func (e Expr) Tail(n int) Expr { return e.reshape(expr.ReshapeTail, int64(n)) }

// Slice is length values from offset; a negative offset counts from the end.
func (e Expr) Slice(offset, length int) Expr {
	return e.reshape(expr.ReshapeSlice, int64(offset), int64(length))
}

// Reverse is the values in reverse order.
func (e Expr) Reverse() Expr { return e.reshape(expr.ReshapeReverse) }

// Unique is the distinct values in the order they first appear, a null among them.
// Polars' unique() orders them as it likes unless asked to keep their order.
func (e Expr) Unique() Expr { return e.reshape(expr.ReshapeUnique) }

// DropNulls is the values that are not null.
//
// The frame-level lf.DropNulls drops rows; this is the column's values, a shorter
// column, in an aggregation.
func (e Expr) DropNulls() Expr { return e.reshape(expr.ReshapeDropNulls) }

// Get is the value at index i, one value a group; a negative index counts from the
// end, and an index past the end is null.
func (e Expr) Get(i int) Expr { return e.reshape(expr.ReshapeGet, int64(i)) }

// DropNans is the values that are not NaN; a null stays, as Polars' drop_nans keeps
// it. A column of floats (step 253).
func (e Expr) DropNans() Expr { return e.reshape(expr.ReshapeDropNans) }

// GatherEvery is every n-th value, from offset (step 253).
func (e Expr) GatherEvery(n, offset int) Expr {
	return e.reshape(expr.ReshapeGatherEvery, int64(n), int64(offset))
}

// SortBy is the values in the order of by's, ascending, nulls first, stably: in a
// group, each value sorted by the by of its own row, as Polars' sort_by sorts
// (step 253). by is read on the same rows, so Col("a").SortBy(Col("b")).Last() is the
// a of the group's largest b.
func (e Expr) SortBy(by Expr) Expr {
	return wrap(&expr.Reshape{Op: expr.ReshapeSortBy, Child: e.node(), Args: []int64{0}, By: by.node()})
}

// SortByDesc is SortBy in descending order of by, nulls first.
func (e Expr) SortByDesc(by Expr) Expr {
	return wrap(&expr.Reshape{Op: expr.ReshapeSortBy, Child: e.node(), Args: []int64{1}, By: by.node()})
}
