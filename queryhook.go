package ursus

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/physical"
	"github.com/advenn/ursus/internal/uerr"
)

// QueryStats is what a query did, given to its engine's hook as it ends (step 403,
// C3 of v0.8-scope.md).
type QueryStats struct {
	// Op is how the query was run, as its errors name it: "collect",
	// "collect_batches", "count", "profile", "sink_parquet", "write_database" and the
	// rest.
	Op string

	// Duration is from the call to its end, planning and all; for CollectBatches,
	// from the loop's start to its end, the loop's body included.
	Duration time.Duration

	// Rows is the rows the query gave: a frame's, the batches', those a writer wrote,
	// a count's.
	Rows int64

	// Peak is the most its operators held at once, in bytes, as MemoryStats.Peak.
	Peak int64

	// Spills is how many files it wrote to its spill directory.
	Spills int64

	// Threads is how many threads it ran on.
	Threads int

	// Err is why it failed, or nil: an *Error, or its context's error where it was
	// cancelled.
	Err error
}

// WithEngineQueryHook gives the engine a function it calls as each of its queries
// ends, with the query's context and what it did, for a service's own metrics: a
// histogram of durations, a counter of spills, a log of failures by Kind. ursus
// imports no logging or tracing package; the context carries what the service put
// in it, a request's route or a trace's span.
//
// The hook runs on the goroutine that ran the query, before the call that ran it
// returns, so it should be quick, and a panic in it is the caller's own. It is called
// once a query, by Collect, CollectBatches, Count, Profile and every writer, a
// query that failed included; not for what a DataFrame does without a query.
func WithEngineQueryHook(fn func(ctx context.Context, s QueryStats)) EngineOption {
	return func(c *engineCfg) { c.hook = fn }
}

// query is a running query's figures for its engine's hook: shared by the copies of
// its collectCfg, as done's receiver is one.
type query struct {
	hook  func(context.Context, QueryStats)
	start time.Time
	rows  atomic.Int64
}

// counted is a query's root, counting the rows it hands on for the hook.
type counted struct {
	physical.Operator
	rows *atomic.Int64
}

func (c *counted) Next(ctx context.Context) (*data.Batch, error) {
	b, err := c.Operator.Next(ctx)
	if b != nil {
		c.rows.Add(int64(b.Rows()))
	}
	return b, err
}

// end is uerr.Catch for an entry point that runs a query, and then its report: it
// recovers a panic into *err, and tells the engine's hook of the query, the error
// included. It is deferred first, on a cfg declared before it is filled, so that it
// runs last, after done:
//
//	var cfg collectCfg
//	defer cfg.end(ctx, "collect", &err)
//	cfg = newCollectCfg(opts)
//	defer cfg.done()
func (c *collectCfg) end(ctx context.Context, op string, err *error) {
	if v := recover(); v != nil {
		*err = uerr.FromPanic(v, op)
	}
	c.report(ctx, op, *err)
}

// report tells the engine's hook of the query, if it has one and the query was
// begun. CollectBatches calls it alone: a range function may not recover around
// its loop's body.
func (c *collectCfg) report(ctx context.Context, op string, err error) {
	q := c.query
	if q == nil || q.hook == nil {
		return
	}
	q.hook(ctx, QueryStats{
		Op:       op,
		Duration: time.Since(q.start),
		Rows:     q.rows.Load(),
		Peak:     c.budget.Peak(),
		Spills:   c.budget.Spills(),
		Threads:  c.work.threads,
		Err:      err,
	})
}
