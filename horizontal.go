package ursus

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// ConcatStr joins its expressions' values, row by row, with sep between each two:
// Polars' concat_str.
//
//	ConcatStr(" ", Col("first"), Col("last"))           // "Ada Lovelace"
//	ConcatStr("", Col("code"), Lit("-"), Col("n"))       // "AB-7", n formatted
//
// A value that is not a String is formatted as a Cast to String formats it. A row is
// null where any of its values is, as Polars' default is; to skip a null instead,
// fill it first, e.g. Col("middle").FillNull(""). The answer is named after the first
// expression.
// ConcatStrIgnoreNulls is ConcatStr past nulls, Polars' concat_str(ignore_nulls=True):
// each row's values that are not null joined by sep, and a row of nulls alone "",
// not null (step 271).
func ConcatStrIgnoreNulls(sep string, exprs ...Expr) Expr {
	if len(exprs) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindSchema, "concat_str",
			"ConcatStrIgnoreNulls requires at least one expression")})
	}
	args := make([]expr.Node, 0, len(exprs)+1)
	for _, e := range exprs {
		args = append(args, e.node())
	}
	return wrap(&expr.Call{Fn: expr.FnConcatStrIgnoreNulls, Args: append(args, litNode(sep))})
}

func ConcatStr(sep string, exprs ...Expr) Expr {
	if len(exprs) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindSchema, "concat_str",
			"ConcatStr requires at least one expression")})
	}
	args := make([]expr.Node, 0, 2*len(exprs))
	for i, e := range exprs {
		if i > 0 && sep != "" {
			args = append(args, litNode(sep))
		}
		args = append(args, e.node())
	}
	return wrap(&expr.Call{Fn: expr.FnConcatStr, Args: args})
}

// Struct builds a Struct column whose fields are its expressions, each named as its
// expression is: Polars' pl.struct. Struct().Field reads a field back.
//
//	Struct(Col("lat"), Col("lon")).Alias("point")   // {lat: 51.5, lon: -0.1}
//
// Two expressions of one name would be two fields Field could not tell apart, so the
// query is refused; alias one. The struct itself is never null: a row whose values
// are all null is a struct of nulls, which StructExpr's doc tells apart from a null
// struct. The answer is named after the first expression.
func Struct(exprs ...Expr) Expr {
	if len(exprs) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindSchema, "struct",
			"Struct requires at least one expression")})
	}
	args := make([]expr.Node, len(exprs))
	for i, e := range exprs {
		args[i] = e.node()
	}
	return wrap(&expr.Call{Fn: expr.FnStructOf, Args: args})
}

// horizontalOf is fn over exprs, at least one.
func horizontalOf(fn expr.CallFn, api string, exprs []Expr) Expr {
	if len(exprs) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindSchema, fn.String(),
			"%s requires at least one expression", api)})
	}
	args := make([]expr.Node, len(exprs))
	for i, e := range exprs {
		args[i] = e.node()
	}
	return wrap(&expr.Call{Fn: fn, Args: args})
}

// MaxHorizontal and MinHorizontal are each row's largest and smallest value across
// exprs, as Polars' max_horizontal and min_horizontal. The values meet at one type
// as a conditional's branches do, an Int32 and a Float64 at Float64, and are
// compared as Max and Min compare a column's: nulls skipped, null when every value
// is, and NaN the largest, where Polars skips it. The answer is named after the
// first expression.
func MaxHorizontal(exprs ...Expr) Expr {
	return horizontalOf(expr.FnMaxHorizontal, "MaxHorizontal", exprs)
}
func MinHorizontal(exprs ...Expr) Expr {
	return horizontalOf(expr.FnMinHorizontal, "MinHorizontal", exprs)
}

// SumHorizontal and MeanHorizontal are each row's sum and mean across exprs, as Sum
// and Mean give a column's: nulls skipped, an integer sum widened to Int128, a mean
// the nearest double to the exact one, and either null when every value is null.
// Polars' sum_horizontal keeps an integer operand's type, so wraps, and sums a row
// of nulls to 0. The answer is named after the first expression.
func SumHorizontal(exprs ...Expr) Expr {
	return horizontalOf(expr.FnSumHorizontal, "SumHorizontal", exprs)
}
func MeanHorizontal(exprs ...Expr) Expr {
	return horizontalOf(expr.FnMeanHorizontal, "MeanHorizontal", exprs)
}

// AnyHorizontal and AllHorizontal are each row's Or and And across exprs, Kleene's,
// as Polars' any_horizontal and all_horizontal: a true decides Any and a false
// decides All, and a row that nothing decides is null if it holds a null. The
// expressions must be Boolean. The answer is named after the first.
func AnyHorizontal(exprs ...Expr) Expr {
	return horizontalOf(expr.FnAnyHorizontal, "AnyHorizontal", exprs)
}
func AllHorizontal(exprs ...Expr) Expr {
	return horizontalOf(expr.FnAllHorizontal, "AllHorizontal", exprs)
}
