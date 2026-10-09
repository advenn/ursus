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
