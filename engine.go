package ursus

import (
	"sync/atomic"

	"github.com/advenn/ursus/internal/execopt"
)

// Engine is what the queries run on it share: one memory budget, and the directory
// they spill to.
//
// Every query runs on one, DefaultEngine unless WithEngine names another, so the
// queries of a process share one budget, half the memory it may use, where before
// 0.8 each query took the half for itself and sixty-four at once assumed thirty-two
// times the machine.
//
//	reports := ursus.NewEngine(ursus.WithEngineMemoryLimit(2 << 30))
//	df, err := q.Collect(ctx, ursus.WithEngine(reports))
//
// # What sharing changes
//
// A query's operators spill when the engine's queries together hold more than its
// budget, and this query more than what the others leave it, never less than an
// even share of the budget among the queries running. Alone on an engine, a query
// spills exactly where it did with a budget of its own.
//
// Another query's holding makes a query spill sooner, not fail: a query is refused,
// with an error naming the operator that cannot divide what it holds, only when it
// alone holds more than the whole budget, as before. Spilling holds a little of its
// own, its files' buffers and its rows' positions, so a query whose peak alone is
// near the whole budget can pass it when the others make it spill. And what cannot
// spill, an implode's lists, a pivot's pairs or a Collect's result, can take a
// process past its engine's budget when many queries hold it at once.
//
// Where the queries share a budget, an unordered group-by's row order can move with
// what the others hold, as it moved with scheduling under the default budget before:
// its contents do not. A query given WithMemoryLimit has a budget of its own apart
// from its engine's, and spills where it would alone, so its row order is the same
// on every run.
//
// # Several engines
//
// Each engine's budget is its own, and a service that makes two, say one for
// requests and one for exports, divides its memory between them: two engines with
// the default budget would each assume half the machine.
//
// An Engine is safe for use by any number of goroutines.
type Engine struct {
	pool *execopt.Pool
}

// EngineOption configures NewEngine.
type EngineOption func(*engineCfg)

type engineCfg struct {
	memLimit int64
	limitSet bool
	spillDir string
}

// WithEngineMemoryLimit sets the engine's budget, in bytes, which its queries share;
// 0 for none. WithMemoryLimit says what a budget bounds.
//
// Without it the budget is half the memory the process may use, as DefaultEngine's,
// and the engine sets the Go runtime's soft memory limit as the first query runs on
// it, as DefaultEngine does: see SetProcessMemoryLimit. With it, the engine leaves
// the runtime's limit alone, and a program under a container's limit sets it.
func WithEngineMemoryLimit(bytes int64) EngineOption {
	return func(c *engineCfg) { c.memLimit, c.limitSet = bytes, true }
}

// WithEngineSpillDir sets the directory the engine's queries spill to, where a query
// gives no WithSpillDir of its own. The default is the system temporary directory.
func WithEngineSpillDir(dir string) EngineOption {
	return func(c *engineCfg) { c.spillDir = dir }
}

// NewEngine returns an engine of its own budget, for the queries given it with
// WithEngine.
func NewEngine(opts ...EngineOption) *Engine {
	var c engineCfg
	for _, o := range opts {
		o(&c)
	}
	if !c.limitSet {
		return newEngine(defaultMemoryLimit(), c.spillDir, true)
	}
	return newEngine(c.memLimit, c.spillDir, false)
}

func newEngine(limit int64, spillDir string, derived bool) *Engine {
	return &Engine{pool: execopt.NewPool(limit, spillDir, derived)}
}

// DefaultEngine returns the engine a query runs on when it is given none: its budget
// half the memory the process may use, the smaller of its cgroup's limit and the
// machine's RAM, or none where neither can be read; its spills in the system
// temporary directory.
func DefaultEngine() *Engine {
	if e := defaultEngine.Load(); e != nil {
		return e
	}
	defaultEngine.CompareAndSwap(nil, newEngine(defaultMemoryLimit(), "", true))
	return defaultEngine.Load()
}

var defaultEngine atomic.Pointer[Engine]

// WithEngine runs the query on e, sharing e's budget with its other queries. Nil is
// DefaultEngine.
func WithEngine(e *Engine) CollectOption {
	return func(c *collectCfg) { c.engine = e }
}

// EngineStats is what an engine's queries hold now, and have held.
type EngineStats struct {
	// Held is what the operators of the queries running on the engine hold now, as
	// MemoryStats.Peak counts a query's, each query's to a mebibyte.
	Held int64

	// Peak is the most Held has been since the engine was made.
	Peak int64

	// Limit is the engine's budget, or 0 for none.
	Limit int64

	// Running is how many queries are running on the engine's budget now. A query
	// with a WithMemoryLimit of its own is apart from it, and not counted.
	Running int

	// Spills is how many files the engine's queries have written: MemoryStats.Spills,
	// summed.
	Spills int64
}

// Stats returns what the engine's queries hold now, and have held: for a service's
// metrics, or a test that a budget was shared.
func (e *Engine) Stats() EngineStats {
	s := e.pool.Stats()
	return EngineStats{Held: s.Held, Peak: s.Peak, Limit: s.Limit, Running: s.Running, Spills: s.Spills}
}
