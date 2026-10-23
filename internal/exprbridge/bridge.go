// Package exprbridge lets a package beside the root one build the root's
// expressions, package selector (step 322): the root sets Wrap when it initialises, so
// the root's API names no internal type.
package exprbridge

import "github.com/advenn/ursus/internal/expr"

// Wrap is the root package's wrap: an expression node as an ursus.Expr.
var Wrap func(expr.Node) any
