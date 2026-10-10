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
	return wrap(&expr.Call{Fn: expr.FnStructField, Args: []expr.Node{s.e.node(), litNode(name)}})
}

// WithFields sets fields of each struct: an expression named as one of its fields
// replaces that field in place, and one of a new name is added after the others,
// each named as its expression is. Polars' struct.with_fields.
//
//	Col("person").Struct().WithFields(
//		Col("person").Struct().Field("age").Add(1).Alias("age"),  // replaces age
//		Col("city"),                                              // adds city
//	)
//
// Each expression is read from the frame, so a field of the struct is read as it is
// anywhere, through Field, and named as any expression is: a Field read is named after
// its struct, so alias it to set a field of that name. The struct's own validity is kept: a null struct stays
// null. Two expressions of one name would set one field twice, and are refused.
func (s StructExpr) WithFields(exprs ...Expr) Expr {
	args := make([]expr.Node, 0, len(exprs)+1)
	args = append(args, s.e.node())
	for _, e := range exprs {
		args = append(args, e.node())
	}
	return wrap(&expr.Call{Fn: expr.FnStructWithFields, Args: args})
}
