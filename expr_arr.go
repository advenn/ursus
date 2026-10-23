package ursus

import "github.com/advenn/ursus/internal/expr"

// ArrExpr is the array namespace, Polars' arr: `Col("xyz").Arr().ToList()`.
//
// An Array is a fixed-size list: Array(Int64, 3) holds three Int64s in each row, or
// is null, and casts to and from a List of its elements, a List's only where each of
// its rows holds that many (List().ToArray, Cast). It is read from and written to
// Arrow IPC as a FixedSizeList, and to Parquet as a list whose size the footer's
// Arrow schema keeps, as Polars writes it. As a key, an Array is refused, as a List
// is.
//
// New in 0.6.7.
type ArrExpr struct{ e Expr }

// Arr is the array namespace: functions of an Array column's rows.
func (e Expr) Arr() ArrExpr { return ArrExpr{e} }

func (a ArrExpr) call(fn expr.CallFn, args ...any) Expr {
	return ListExpr(a).call(fn, args...)
}

// ToList is each array as a List of its elements, Polars' arr.to_list.
func (a ArrExpr) ToList() Expr { return a.call(expr.FnArrToList) }
