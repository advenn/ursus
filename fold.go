package ursus

import (
	"strings"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Folds and constructors (step 266, F1 of v0.6.6-scope.md): Polars' concat_list,
// fold, reduce, cum_fold, cum_reduce, cum_sum_horizontal and format.

// ConcatList is each row's operands in one List: a List operand's elements and
// another's value, Polars' concat_list. The elements meet at the type a conditional's
// branches meet at; a null value is a null element, and a null List makes the row's
// list null, as Polars makes it. Where Polars casts numbers beside strings to text,
// this refuses them: cast first.
func ConcatList(exprs ...Expr) Expr {
	if len(exprs) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "concat_list", "ConcatList needs an expression")})
	}
	return variadic("concat_list", exprs, func(ns []expr.Node) (expr.Node, error) {
		return &expr.Call{Fn: expr.FnConcatList, Args: ns}, nil
	})
}

// foldOf is a Fold of fn, its accumulator acc or nil.
func foldOf(op string, acc expr.Node, fn func(acc, x Expr) Expr, exprs []Expr, promote bool) Expr {
	if fn == nil {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, op, "%s needs a function", op)})
	}
	nodes := make([]expr.Node, len(exprs))
	for i, e := range exprs {
		nodes[i] = e.node()
	}
	build := func(a, x expr.Node) expr.Node { return fn(wrap(a), wrap(x)).node() }
	return wrap(&expr.Fold{Op: op, Acc: acc, Exprs: nodes, Fn: build, Promote: promote})
}

// Fold is fn applied from acc across exprs, acc = fn(acc, x) for each: Polars' fold,
// named after acc.
//
//	Fold(0, func(acc, x Expr) Expr { return acc.Add(x) }, All())   // every column's sum
//
// fn builds an expression, once, when the query is planned, and the expressions are
// written out first, so a selection of several columns folds over each. Polars runs
// its function over the columns per batch; built ahead, the fold is an expression the
// optimizer sees through, and no function of yours runs per row.
func Fold[A Operand](acc A, fn func(acc, x Expr) Expr, exprs ...Expr) Expr {
	return foldOf("fold", lift(acc), fn, exprs, false)
}

// Reduce is Fold whose accumulator is the first expression, named after it:
// Polars' reduce.
func Reduce(fn func(acc, x Expr) Expr, exprs ...Expr) Expr {
	return foldOf("reduce", nil, fn, exprs, false)
}

// CumFold is Fold's running values, a Struct named "cum_fold" with a field for each
// expression, named after it: Polars' cum_fold.
func CumFold[A Operand](acc A, fn func(acc, x Expr) Expr, exprs ...Expr) Expr {
	return foldOf("cum_fold", lift(acc), fn, exprs, false)
}

// CumReduce is Reduce's running values, a Struct named "cum_reduce" whose first
// field is the first expression: Polars' cum_reduce.
func CumReduce(fn func(acc, x Expr) Expr, exprs ...Expr) Expr {
	return foldOf("cum_reduce", nil, fn, exprs, false)
}

// CumSumHorizontal is the running sums across exprs, a Struct named "cum_sum", every
// field of the total's type: Polars' cum_sum_horizontal. A null makes the sums from
// it on null.
func CumSumHorizontal(exprs ...Expr) Expr {
	return foldOf("cum_sum", nil, func(acc, x Expr) Expr { return acc.Add(x) }, exprs, true)
}

// Format is f with each {} replaced by the next of args as text, {{ and }} a brace:
// Polars' format, a ConcatStr of its pieces. A null argument makes the row null.
// The column is named after the first argument, "literal" with none, as Polars names
// it. As many arguments as placeholders are wanted.
func Format(f string, args ...Expr) Expr {
	var pieces []string
	var cur strings.Builder
	holes := 0
	for i := 0; i < len(f); i++ {
		switch {
		case strings.HasPrefix(f[i:], "{{"), strings.HasPrefix(f[i:], "}}"):
			cur.WriteByte(f[i])
			i++
		case strings.HasPrefix(f[i:], "{}"):
			pieces = append(pieces, cur.String())
			cur.Reset()
			holes++
			i++
		default:
			cur.WriteByte(f[i])
		}
	}
	pieces = append(pieces, cur.String())
	if holes != len(args) {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "format",
			"format %q has %d placeholders and %d arguments", f, holes, len(args))})
	}
	parts := make([]Expr, 0, 2*len(args)+1)
	for i, p := range pieces {
		if p != "" {
			parts = append(parts, Lit(p))
		}
		if i < len(args) {
			parts = append(parts, args[i])
		}
	}
	if len(parts) == 0 {
		parts = append(parts, Lit(""))
	}
	name := "literal"
	if len(args) > 0 {
		name = expr.OutputName(args[0].node())
	}
	// Built at once, not as ConcatStr: a selection among the arguments formats each
	// of its columns, as Polars' format expands per column where its concat_str
	// takes them all as operands (step 302).
	nodes := make([]expr.Node, len(parts))
	for i, p := range parts {
		nodes[i] = p.node()
	}
	return wrap(&expr.Call{Fn: expr.FnConcatStr, Args: nodes}).Alias(name)
}
