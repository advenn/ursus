package ursus

import (
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
)

// ExprOf wraps a node built by hand, for the tests that need a node no public
// constructor builds: a udf whose kernel panics without the public wrapper, which
// is how a bug in an ursus kernel looks to the engine.
func ExprOf(n expr.Node) Expr { return wrap(n) }

// BudgetOf is the budget a query collected with opts would run under.
func BudgetOf(opts ...CollectOption) *execopt.Budget { return newCollectCfg(opts).budget }

// SetDefaultMemoryLimit replaces the default budget for a test, which a test needs
// small enough to reach; the returned function restores it.
func SetDefaultMemoryLimit(n int64) (restore func()) {
	old := defaultMemoryLimit
	defaultMemoryLimit = func() int64 { return n }
	return func() { defaultMemoryLimit = old }
}
