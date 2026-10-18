package ursus

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// CatExpr is the categorical namespace, Polars' .cat, on an Enum, the categorical
// type ursus has: `Col("fruit").Cat().LenChars()`.
//
// The text functions read each value as its category's text, as Str() reads an Enum,
// and each takes a String too; Polars' take only an Enum or a Categorical.
type CatExpr struct{ e Expr }

// Cat opens the categorical namespace.
func (e Expr) Cat() CatExpr { return CatExpr{e} }

// GetCategories is an Enum's categories, in their order, a String column named as
// the expression: a column of its own length in a selection and a list a group in an
// aggregation, as Polars' get_categories is.
func (c CatExpr) GetCategories() Expr {
	return wrap(&expr.Call{Fn: expr.FnCatGetCategories, Args: []expr.Node{c.e.node()}})
}

// Physical is each value's index in its Enum's categories, at Polars' width: UInt8
// up to 255 categories, UInt16 up to 65,535, and UInt32 past them.
func (c CatExpr) Physical() Expr {
	return wrap(&expr.Call{Fn: expr.FnCatPhysical, Args: []expr.Node{c.e.node()}})
}

// StartsWith and EndsWith are whether each value's text starts or ends with a
// constant: Polars' cat.starts_with and cat.ends_with, which take no expression.
func (c CatExpr) StartsWith(prefix string) Expr { return c.e.Str().StartsWith(prefix) }
func (c CatExpr) EndsWith(suffix string) Expr   { return c.e.Str().EndsWith(suffix) }

// LenBytes and LenChars are each value's text's length in bytes and in characters,
// a UInt32.
func (c CatExpr) LenBytes() Expr { return c.e.Str().LenBytes() }
func (c CatExpr) LenChars() Expr { return c.e.Str().LenChars() }

// Slice is length characters of each value's text from offset, which counts from
// the end where negative; with no length, the rest: Polars' cat.slice, a String.
func (c CatExpr) Slice(offset int, length ...int) Expr {
	switch len(length) {
	case 0:
		return c.e.Str().call(expr.FnStrSlice, int64(offset))
	case 1:
		return c.e.Str().Slice(offset, length[0])
	}
	return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "cat.slice", "cat.slice takes at most one length, got %d", len(length))})
}
