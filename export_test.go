package ursus

import (
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// ExprOf wraps a node built by hand, for the tests that need a node no public
// constructor builds: a udf whose kernel panics without the public wrapper, which
// is how a bug in an ursus kernel looks to the engine.
func ExprOf(n expr.Node) Expr { return wrap(n) }

// BudgetOf is the budget a query collected with opts would run under.
func BudgetOf(opts ...CollectOption) *execopt.Budget { return newCollectCfg(opts).budget }

// SetDefaultMemoryLimit replaces the default budget for a test, which a test needs
// small enough to reach: DefaultEngine, by one whose budget is n and behaves as the
// default's. The returned function restores it.
func SetDefaultMemoryLimit(n int64) (restore func()) {
	old := defaultEngine.Swap(newEngine(n, "", true))
	return func() { defaultEngine.Store(old) }
}

// What step 186 took out of the public API, each because it took or returned a type
// in an internal package that no caller outside the module could name, the module's
// own tests still reach through these.

// Scan starts a query against a source.
func Scan(src plan.Source) *LazyFrame { return scan(src) }

// FromPlan builds a LazyFrame over a logical plan.
func FromPlan(n plan.Node) *LazyFrame { return &LazyFrame{node: n} }

// Plan returns the LazyFrame's logical plan.
func (lf *LazyFrame) Plan() plan.Node { return lf.node }

// Batch returns the DataFrame's batch.
func (df *DataFrame) Batch() *data.Batch { return df.batch }

// Press holds n bytes on e's budget as another query running on it would, until
// release is called: an engine's pressure, for a test.
func Press(e *Engine, n int64) (release func()) {
	b := e.pool.Budget("")
	b.Join()
	b.Account("press").RetainBytes(n)
	return b.Leave
}
