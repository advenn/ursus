package ursus

import (
	"github.com/advenn/ursus/internal/expr"
)

// StructExpr is the struct namespace: `Col("person").Struct().Field("age")`.
//
// The same wrapper shape `.str`, `.dt` and `.list` use, for the reason ursus-api.md
// §4.5 gives: methods return Expr, so chaining continues naturally through it.
//
// # A null struct is not a struct of nulls
//
// Both hold no field values, and only the struct's own validity separates them:
//
//	                          Field("age")   renders as
//	{age: 30, city: "NY"}          30        {age: 30, city: "NY"}
//	{age: null, city: null}       null       {age: null, city: null}
//	null (the struct)             null       null
//
// The last two rows answer identically through Field — there is no age either way —
// and differ where it matters, on the struct itself. Working from "a struct with
// nothing in it is absent" gives the wrong answer for a real struct that happens to
// hold nulls, and nothing errors.
type StructExpr struct{ e Expr }

// Struct opens the struct namespace.
func (e Expr) Struct() StructExpr { return StructExpr{e} }

// Field reads one field out of each struct.
//
// The result is an ordinary column of the field's own type, so everything else in
// the language applies to it: filter on it, group by it, join on it.
//
// A name the struct does not have is refused when the query is PLANNED, with the
// available names listed — the type of the result depends on which field was asked
// for, so the name has to be resolved against the schema anyway.
func (s StructExpr) Field(name string) Expr {
	return wrap(&expr.Call{Fn: expr.FnStructField, Args: []expr.Node{s.e.n, litNode(name)}})
}
