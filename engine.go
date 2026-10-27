package ursus

import (
	"runtime"
	"sync/atomic"

	"github.com/advenn/ursus/internal/execopt"
)

// Engine is what the queries run on it share: one memory budget, the directory they
// spill to, and its threads.
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
// # Threads
//
// A query given no WithThreads runs on the engine's threads, GOMAXPROCS by default,
// and where several run at once on a share of them (step 397, B2 of v0.8-scope.md):
// the threads divided by the queries running on more than one, this one counted,
// and at least one. Sixty-four queries at once each started a worker a thread before,
// eight hundred goroutines on eight cores, and a 200,000-row group-by from sixty-four
// goroutines answered 670 to 750 queries a second on its threads, and 890 on one
// each. A query takes its share as it is planned, and keeps it to its end. One on a
// single thread, as a small query is (WithThreads says when), takes none from the
// others. A query given WithThreads(n) runs on n, and counts among them.
//
// # Several engines
//
// Each engine's budget is its own, and a service that makes two, say one for
// requests and one for exports, divides its memory between them: two engines with
// the default budget would each assume half the machine.
//
// An Engine is safe for use by any number of goroutines.
type Engine struct {
	pool    *execopt.Pool
	threads int

	// parallel is how many of its running queries run on more than one thread: the
	// divisor of a query's share.
	parallel atomic.Int64
}

// EngineOption configures NewEngine.
type EngineOption func(*engineCfg)

type engineCfg struct {
	memLimit int64
	limitSet bool
	spillDir string
	threads  int
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

// WithEngineThreads sets how many threads the engine's queries share, at least one.
// The default is GOMAXPROCS as the engine is made: the CPUs a container's limit
// allows, where Go reads one.
func WithEngineThreads(n int) EngineOption {
	return func(c *engineCfg) { c.threads = max(n, 1) }
}

// NewEngine returns an engine of its own budget and threads, for the queries given it
// with WithEngine.
func NewEngine(opts ...EngineOption) *Engine {
	var c engineCfg
	for _, o := range opts {
		o(&c)
	}
	if !c.limitSet {
		c.memLimit = defaultMemoryLimit()
	}
	return newEngine(c.memLimit, c.spillDir, !c.limitSet, c.threads)
}

// newEngine is an engine of its budget, derived where it is the default's, and of
// threads, 0 for GOMAXPROCS.
func newEngine(limit int64, spillDir string, derived bool, threads int) *Engine {
	if threads < 1 {
		threads = runtime.GOMAXPROCS(0)
	}
	return &Engine{pool: execopt.NewPool(limit, spillDir, derived), threads: threads}
}

// share is the threads a query that wants want is given as it is planned: want,
// where it wants one; else the engine's threads divided by its queries on more than
// one, this one counted, at least one and at most want. A query given more than one
// counts among those until release.
func (e *Engine) share(want int) int {
	if want <= 1 {
		return 1
	}
	n := min(max(e.threads/int(e.parallel.Add(1)), 1), want)
	if n <= 1 {
		e.parallel.Add(-1)
	}
	return n
}

// frameWorkers is the threads an eager frame operation of want pieces runs on, the
// pieces a column's or a part's: its share of DefaultEngine's, as a query's, at most
// want; release gives them back (step 397). It took GOMAXPROCS each, whatever the
// queries running.
func frameWorkers(want int) (n int, release func()) {
	e := DefaultEngine()
	n = e.share(min(e.threads, want))
	return n, func() { e.release(n) }
}

// hold counts a query the caller gave n threads among the engine's on more than one.
func (e *Engine) hold(n int) int {
	if n > 1 {
		e.parallel.Add(1)
	}
	return n
}

// release ends a query's count among the engine's on more than one thread.
func (e *Engine) release(n int) {
	if n > 1 {
		e.parallel.Add(-1)
	}
}

// DefaultEngine returns the engine a query runs on when it is given none: its budget
// half the memory the process may use, the smaller of its cgroup's limit and the
// machine's RAM, or none where neither can be read; its spills in the system
// temporary directory; its threads GOMAXPROCS as it is first used.
func DefaultEngine() *Engine {
	if e := defaultEngine.Load(); e != nil {
		return e
	}
	defaultEngine.CompareAndSwap(nil, newEngine(defaultMemoryLimit(), "", true, 0))
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
	// MemoryStats.Peak counts a query's, each query's to a mebibyte, and the pages
	// their Parquet scans are reading.
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

	// Threads is how many threads the engine's queries share.
	Threads int

	// Parallel is how many of its queries run on more than one thread now, a query
	// given WithMemoryLimit too: what a query's share of Threads is divided by.
	Parallel int
}

// Stats returns what the engine's queries hold now, and have held: for a service's
// metrics, or a test that a budget was shared.
func (e *Engine) Stats() EngineStats {
	s := e.pool.Stats()
	return EngineStats{Held: s.Held, Peak: s.Peak, Limit: s.Limit, Running: s.Running, Spills: s.Spills,
		Threads: e.threads, Parallel: int(e.parallel.Load())}
}
