package ursus

import "github.com/advenn/ursus/internal/expr"

// ExprOf wraps a node built by hand, for the tests that need a node no public
// constructor builds: a udf whose kernel panics without the public wrapper, which
// is how a bug in an ursus kernel looks to the engine.
func ExprOf(n expr.Node) Expr { return wrap(n) }
