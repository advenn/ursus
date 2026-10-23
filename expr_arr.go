package ursus

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// ArrExpr is the array namespace, Polars' arr: `Col("xyz").Arr().Sum()`.
//
// An Array is a fixed-size list: Array(Int64, 3) holds three Int64s in each row, or
// is null, and casts to and from a List of its elements, a List's only where each of
// its rows holds that many (List().ToArray, Cast). It is read from and written to
// Arrow IPC as a FixedSizeList, and to Parquet as a list whose size the footer's
// Arrow schema keeps, as Polars writes it. As a key, an Array is refused, as a List
// is.
//
// Its functions are a List's of the same elements, and answer as List()'s do, nulls
// and all: a function of each array's values gives a value, and one that reshapes it
// gives a List, but Reverse, Sort, Shift and Eval of an element's value, which keep
// each array's length, keep its Array. ToStruct and Dot read its size.
//
// New in 0.6.7.
type ArrExpr struct{ e Expr }

// Arr is the array namespace: functions of an Array column's rows.
func (e Expr) Arr() ArrExpr { return ArrExpr{e} }

func (a ArrExpr) call(fn expr.CallFn, args ...any) Expr {
	return ListExpr(a).call(fn, args...)
}

// list is each array as a List, whose namespace answers most of this one's.
func (a ArrExpr) list() ListExpr { return a.ToList().List() }

// ToList is each array as a List of its elements, Polars' arr.to_list.
func (a ArrExpr) ToList() Expr { return a.call(expr.FnArrToList) }

// Len is each array's size, null where it is null.
func (a ArrExpr) Len() Expr { return a.list().Len() }

// Min, Max, Sum, Mean and Median reduce each array's values as List()'s do: a null
// element is passed over, and a sum of integers is an Int128.
func (a ArrExpr) Min() Expr    { return a.list().Min() }
func (a ArrExpr) Max() Expr    { return a.list().Max() }
func (a ArrExpr) Sum() Expr    { return a.list().Sum() }
func (a ArrExpr) Mean() Expr   { return a.list().Mean() }
func (a ArrExpr) Median() Expr { return a.list().Median() }

// Std and Var are each array's standard deviation and variance with ddof, Polars'
// arr.std and arr.var, whose ddof is 1 unless given.
func (a ArrExpr) Std(ddof int) Expr { return a.list().Std(ddof) }
func (a ArrExpr) Var(ddof int) Expr { return a.list().Var(ddof) }

// NUnique is each array's count of distinct values, a null among them.
func (a ArrExpr) NUnique() Expr { return a.list().NUnique() }

// ArgMin and ArgMax are the position in each array of its least or greatest value.
func (a ArrExpr) ArgMin() Expr { return a.list().ArgMin() }
func (a ArrExpr) ArgMax() Expr { return a.list().ArgMax() }

// First and Last are each array's first and last element, Get the i-th, from the
// end where i is negative, null past either end. i may be an expression, one a row.
func (a ArrExpr) First() Expr { return a.list().First() }
func (a ArrExpr) Last() Expr  { return a.list().Last() }
func (a ArrExpr) Get[I int | Expr](i I) Expr {
	return a.list().Get(i)
}

// Contains reports whether each array holds v, CountMatches how many times; v may
// be an expression, one a row. See List().Contains.
func (a ArrExpr) Contains(v any) Expr     { return a.list().Contains(v) }
func (a ArrExpr) CountMatches(v any) Expr { return a.list().CountMatches(v) }

// Any and All are each Boolean array's or and and, nulls passed over with
// ignoreNulls, as List()'s.
func (a ArrExpr) Any(ignoreNulls bool) Expr { return a.list().Any(ignoreNulls) }
func (a ArrExpr) All(ignoreNulls bool) Expr { return a.list().All(ignoreNulls) }

// Head, Tail and Slice are each array's first n, last n, or length from offset, as
// a List, Polars' default: List().ToArray(n) makes one an Array again.
func (a ArrExpr) Head(n int) Expr               { return a.list().Head(n) }
func (a ArrExpr) Tail(n int) Expr               { return a.list().Tail(n) }
func (a ArrExpr) Slice(offset, length int) Expr { return a.list().Slice(offset, length) }

// Unique is each array's distinct values, each first where it first appears, as a
// List.
func (a ArrExpr) Unique() Expr { return a.list().Unique() }

// Join joins each array's strings with sep, passing nulls over, as List().Join.
func (a ArrExpr) Join(sep string) Expr { return a.list().Join(sep) }

// Explode is each array's elements, a row each, a null array one null row.
func (a ArrExpr) Explode() Expr { return a.e.Explode() }

// Reverse is each array's elements last first, Sort ascending and SortDesc
// descending, nulls first, and SortWith either way with nulls first or last: an
// Array of its size, as Polars keeps it.
func (a ArrExpr) Reverse() Expr  { return a.call(expr.FnArrReverse) }
func (a ArrExpr) Sort() Expr     { return a.call(expr.FnArrSort, false) }
func (a ArrExpr) SortDesc() Expr { return a.call(expr.FnArrSort, true) }
func (a ArrExpr) SortWith(descending, nullsLast bool) Expr {
	return a.call(expr.FnArrSort, descending, nullsLast)
}

// Shift moves each array's elements n places later, nulls in the places they leave,
// earlier where n is negative: an Array of its size. n may be an expression, one a
// row; a null n is a null array.
func (a ArrExpr) Shift[N Operand](n N) Expr {
	return wrap(&expr.Call{Fn: expr.FnArrShift, Args: []expr.Node{a.e.node(), lift(n)}})
}

// Eval runs body over each array's elements, the array its group, as List().Eval:
// a body of a value an element, Element().Mul(2), keeps the array's length, and so
// its Array. With asList the answer is a List, any body's, as Polars'
// eval(as_list=True); without, a body that changes the length is refused, as
// Polars refuses it.
func (a ArrExpr) Eval(body Expr, asList bool) Expr {
	if asList {
		return a.list().Eval(body)
	}
	n := expr.LowerListEval(a.e.node(), body.node(), false)
	switch n.(type) {
	case *expr.ListMap, *expr.Err:
		return wrap(n)
	}
	return wrap(&expr.Err{E: arrEvalLength(body)})
}

// arrEvalLength is Polars' refusal of an eval whose body changes each array's length.
func arrEvalLength(body Expr) error {
	return uerr.New(uerr.KindUnsupported, "arr.eval",
		"arr.eval of %s changes each array's length, which an Array cannot", body.node()).
		Hint("Eval(body, true) gives a List of any body's answer, as Polars' as_list")
}

// Agg runs body over each array's elements, an aggregate's value its answer, as
// List().Agg.
func (a ArrExpr) Agg(body Expr) Expr { return a.list().Agg(body) }

// ToStruct makes each array a Struct, a field an element, named by names, as many
// as its size, or field_0 on without them, as Polars names them. A null array is a
// null struct, where Polars gives a struct of nulls, as List().ToStruct.
func (a ArrExpr) ToStruct(names ...string) Expr {
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = n
	}
	return a.call(expr.FnArrToStruct, args...)
}

// Dot is each row's sum of this array's and other's products, element by element,
// of two Arrays of numbers of one size: Polars' arr.dot. A null element's product is
// passed over, and a row is null where either array is. A sum of integers is an
// Int128, as Sum's.
func (a ArrExpr) Dot(other Expr) Expr {
	return wrap(&expr.Call{Fn: expr.FnArrDot, Args: []expr.Node{a.e.node(), other.node()}})
}

// Reshape is the values in Arrays of shape, Polars' reshape: shape's first dimension
// is the rows, -1 to infer them, and each one after it an Array of the next, the
// last innermost, so Reshape(-1, 3) of six values is two rows of an Array(T, 3), and
// Reshape(-1, 1, 3) two of an Array(Array(T, 3), 1). Of one dimension, Reshape(-1),
// it is the values themselves, a List's or an Array's elements flattened. The values
// are an expression's leaves: a List's or an Array's elements, an Array's null row
// its size of nulls, as Polars lays one out, and a List's null or empty one none.
//
// It is a column of its own length, as Explode is; in an aggregation, a list of the
// Arrays a group makes. Values the shape does not divide are refused, as Polars
// refuses them.
//
// New in 0.6.7.
func (e Expr) Reshape(shape ...int) Expr {
	dims := make([]int64, len(shape))
	for i, d := range shape {
		dims[i] = int64(d)
	}
	return e.reshape(expr.ReshapeArr, dims...)
}

// ConcatArr is each row's operands in one Array, Polars' concat_arr: an Array
// operand's elements and another's value, in order, so two Arrays of 2 and of 3 and
// a value make an Array of 6. The operands are of one element type, as Polars wants
// them: cast one first. A null Array makes the row null, and a null value is a null
// element.
//
// New in 0.6.7.
func ConcatArr(exprs ...Expr) Expr {
	if len(exprs) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "concat_arr", "ConcatArr needs an expression")})
	}
	return variadic("concat_arr", exprs, func(ns []expr.Node) (expr.Node, error) {
		return &expr.Call{Fn: expr.FnConcatArr, Args: ns}, nil
	})
}
