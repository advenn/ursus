package ursus

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Selection by position and the index functions (step 267, F2 of v0.6.6-scope.md):
// Polars' nth, first, last, arg_where, arg_sort_by, arg_sort, arg_true and
// arg_unique.

// Nth selects columns by position, in the order given: Polars' nth. A negative
// index counts from the end, and one past either end is refused when the query is
// planned. Like Col("a", "b"), it expands: Nth(0, 1).Mul(2) is two columns.
func Nth(indices ...int) Expr {
	if len(indices) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "nth", "Nth needs an index")})
	}
	return wrap(&expr.Match{M: &expr.IndexMatcher{Indices: append([]int(nil), indices...)}})
}

// First is the frame's first column, Nth(0), or with names their first values,
// Col(names...).First(): Polars' first.
func First(names ...string) Expr {
	if len(names) == 0 {
		return Nth(0)
	}
	return Col(names...).First()
}

// Last is the frame's last column, Nth(-1), or with names their last values: Polars'
// last.
func Last(names ...string) Expr {
	if len(names) == 0 {
		return Nth(-1)
	}
	return Col(names...).Last()
}

func flag(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ArgSort is the indices that sort the column, a UInt32 column of its length:
// Polars' arg_sort. Ties keep their order, in either direction, and nulls come first
// unless nullsLast. In an aggregation it is a list a group, of positions within it.
func (e Expr) ArgSort(descending, nullsLast bool) Expr {
	return e.reshape(expr.ReshapeArgSort, flag(descending), flag(nullsLast))
}

// ArgTrue is the indices of the true values of a Boolean column, UInt32: Polars'
// arg_true. A null is not true.
func (e Expr) ArgTrue() Expr { return e.reshape(expr.ReshapeArgTrue) }

// ArgWhere is the indices of the rows pred is true on: Polars' arg_where,
// pred.ArgTrue().
func ArgWhere(pred Expr) Expr { return pred.ArgTrue() }

// ArgUnique is the index of each distinct value's first row, in the order they first
// appear, a null among them: Polars' arg_unique.
func (e Expr) ArgUnique() Expr { return e.reshape(expr.ReshapeArgUnique) }

// ArgSortBy is the indices that sort the frame by keys, as Sort(keys...) sorts it,
// UInt32: Polars' arg_sort_by. Each key is Asc or Desc, NullsLast or not. In an
// aggregation it takes one key; Polars 1.44.1 answers several there with indices past
// the group's end.
func ArgSortBy(keys ...SortKey) Expr {
	if len(keys) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "arg_sort_by", "ArgSortBy needs a key")})
	}
	nodes := make([]expr.Node, len(keys))
	args := make([]int64, 0, 2*len(keys))
	for i, k := range keys {
		if k.k.Expr == nil {
			return wrap(&expr.Err{E: errNilSortKey(i)})
		}
		nodes[i] = k.k.Expr
		args = append(args, flag(k.k.Descending), flag(k.k.NullsLast))
	}
	return wrap(&expr.Reshape{Op: expr.ReshapeArgSortBy, Child: &expr.Call{Fn: expr.FnStructOf, Args: nodes}, Args: args})
}
