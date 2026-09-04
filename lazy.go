package ursus

import (
	"context"
	"iter"
	"runtime"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/physical"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// LazyFrame is an unexecuted query.
//
// # Immutable
//
// Every builder method returns a NEW LazyFrame sharing an immutable plan. So
//
//	base := ursus.Scan(src)
//	a := base.Filter(ursus.Col("x").Gt(5))
//	b := base.Filter(ursus.Col("x").Lt(0))
//
// gives two independent queries, and `base` is unchanged. That idiom is the most
// common thing anyone does with a lazy frame, and it only works because nothing
// mutates in place. It also makes a LazyFrame safe to share between goroutines.
//
// # Sticky errors
//
// A builder cannot return an error without destroying chaining, so errors are
// carried and surfaced at Collect (or read early via Err). An unknown column in
// Select does not panic and does not fail silently — it fails at Collect, with the
// available columns and a spelling suggestion.
type LazyFrame struct {
	node plan.Node
	err  error
}

// Scan starts a query against a source.
func Scan(src plan.Source) *LazyFrame {
	if src == nil {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan", "nil source")}
	}
	return &LazyFrame{node: &plan.Scan{Src: src}}
}

// FromPlan builds a LazyFrame over an existing logical plan. It exists so that a
// future SQL frontend can lower into the same IR and hand back a frame, without
// the root package having to import it — which would be a cycle.
func FromPlan(n plan.Node) *LazyFrame { return &LazyFrame{node: n} }

// Plan returns the underlying logical plan.
func (lf *LazyFrame) Plan() plan.Node { return lf.node }

// Err returns the first deferred error, or nil.
func (lf *LazyFrame) Err() error { return lf.err }

// derive returns a new frame, propagating any existing error unchanged: the
// FIRST error wins, so the message points at the actual mistake rather than at a
// downstream consequence of it.
func (lf *LazyFrame) derive(n plan.Node) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	return &LazyFrame{node: n}
}

// nodes unwraps public expressions, collecting any deferred error.
func nodes(exprs []Expr) ([]expr.Node, error) {
	out := make([]expr.Node, len(exprs))
	for i, e := range exprs {
		if e.n == nil {
			return nil, uerr.New(uerr.KindValue, "", "expression %d is the zero Expr", i)
		}
		if err := e.Err(); err != nil {
			return nil, err
		}
		out[i] = e.n
	}
	return out, nil
}

// --- contexts ----------------------------------------------------------------

// Select computes a new set of columns, replacing the frame's columns entirely.
//
// Expressions may expand: Select(All()) keeps everything, and
// Select(ColDType(Float64).Suffix("_f")) selects and renames every float column.
func (lf *LazyFrame) Select(exprs ...Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	ns, err := nodes(exprs)
	if err != nil {
		return &LazyFrame{err: err}
	}
	return lf.derive(&plan.Project{Input: lf.node, Exprs: ns})
}

// Filter keeps rows where every predicate is true.
//
// Predicates are AND-ed. A row whose predicate is NULL is DROPPED, matching SQL's
// WHERE. One consequence worth knowing: Filter(p) and Filter(p.Not()) do not
// partition the input, because a row where p is null is dropped by both.
func (lf *LazyFrame) Filter(preds ...Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(preds) == 0 {
		return lf
	}
	ns, err := nodes(preds)
	if err != nil {
		return &LazyFrame{err: err}
	}
	return lf.derive(&plan.Filter{Input: lf.node, Preds: ns})
}

// Remove is the inverse of Filter: it drops rows where ANY predicate is true.
//
// Spelled out because the alternative reading is equally plausible: this is
// NOT(p1 OR p2 OR ...), i.e. a row survives only if every predicate is false.
// Rows where a predicate is null are dropped, same as Filter.
func (lf *LazyFrame) Remove(preds ...Expr) *LazyFrame {
	if lf.err != nil || len(preds) == 0 {
		return lf
	}
	combined := preds[0]
	for _, p := range preds[1:] {
		combined = combined.Or(p)
	}
	return lf.Filter(combined.Not())
}

// Head keeps at most n rows.
func (lf *LazyFrame) Head(n int) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	return lf.derive(&plan.Limit{Input: lf.node, N: n})
}

// Limit is an alias for Head.
func (lf *LazyFrame) Limit(n int) *LazyFrame { return lf.Head(n) }

// Pipe applies fn, for composing reusable query fragments.
func (lf *LazyFrame) Pipe(fn func(*LazyFrame) *LazyFrame) *LazyFrame { return fn(lf) }

// --- introspection -----------------------------------------------------------

// CollectSchema returns the result schema without reading any data.
//
// It takes a context because resolving a source's schema is real I/O — a Parquet
// footer, a CSV sample. A schema request that could not be cancelled would be a
// hole in the cancellation story, which is why this is not the no-argument
// Schema() the design documents originally specified.
func (lf *LazyFrame) CollectSchema(ctx context.Context) (*Schema, error) {
	if lf.err != nil {
		return nil, lf.err
	}
	resolved, err := plan.Resolve(ctx, lf.node)
	if err != nil {
		return nil, err
	}
	return resolved.Schema()
}

// ExplainOption configures Explain.
type ExplainOption func(*explainCfg)

type explainCfg struct {
	optimized bool
	schema    bool
}

// Optimized selects the optimized plan (the default) or the plan as written.
func Optimized(b bool) ExplainOption { return func(c *explainCfg) { c.optimized = b } }

// WithSchemas annotates every plan node with its output schema.
func WithSchemas() ExplainOption { return func(c *explainCfg) { c.schema = true } }

// Explain renders the query plan.
//
// The output is stable and diff-friendly on purpose: it is what golden tests
// snapshot, and a plan diff is the only thing that notices a query which still
// returns the right answer while reading forty columns instead of two.
func (lf *LazyFrame) Explain(ctx context.Context, opts ...ExplainOption) (string, error) {
	cfg := explainCfg{optimized: true}
	for _, o := range opts {
		o(&cfg)
	}
	if lf.err != nil {
		return "", lf.err
	}

	n, err := plan.Resolve(ctx, lf.node)
	if err != nil {
		return "", err
	}
	if cfg.optimized {
		if n, err = optimizer().Run(n, plan.DefaultFlags()); err != nil {
			return "", err
		}
	}
	return plan.Explain(n, plan.ExplainOptions{Projection: true, Schema: cfg.schema})
}

// --- execution ---------------------------------------------------------------

// CollectOption configures execution.
type CollectOption func(*collectCfg)

type collectCfg struct {
	batchSize int
	threads   int
	flags     plan.Flags
	verify    bool
	memLimit  int64
	spillDir  string
	stats     *MemoryStats
	budget    *execopt.Budget
}

// WithBatchSize sets the rows per batch. Results must not depend on it; the test
// suite varies it precisely to check that.
func WithBatchSize(n int) CollectOption { return func(c *collectCfg) { c.batchSize = n } }

// WithOptFlags overrides which optimizer rules run. Use it to benchmark a rule's
// value or to bisect a wrong answer down to the rule that caused it.
func WithOptFlags(f plan.Flags) CollectOption { return func(c *collectCfg) { c.flags = f } }

// WithVerify makes the optimizer assert schema preservation after every rule.
// On in tests, off by default because it costs a schema resolution per rule.
func WithVerify() CollectOption { return func(c *collectCfg) { c.verify = true } }

// WithThreads sets how many workers a query runs on. Default runtime.NumCPU().
//
// Results do not depend on it. Parallelism over the PIPELINE — the scan and the
// stateless per-batch operators above it — is ORDER-PRESERVING: workers process
// batches concurrently and the results are read back in input order, so the same
// query returns byte-identical output at any thread count. That is worth stating
// because it is not what every engine does, and because five things in ursus
// quietly depend on batch order — stable sort, First and Last, distinct's
// first-row rule, limit, and first-appearance group ids.
//
// WithThreads(1) reproduces the serial operator tree exactly, which is the knob to
// reach for when bisecting.
//
// # Group-by also runs N-ways, and only where that cannot change an answer
//
// Since step 17 a hash aggregation is drained by several sinks whose partial
// tables are merged. It is NOT order-preserving in the same structural way — the
// dispatcher is round-robin, so no worker holds a contiguous portion of the input
// — so it is used only where the result cannot depend on order at all: not under
// MaintainOrder, not with First, Last, ArgMin or ArgMax, and not under a memory
// limit, because a sink that has spilled cannot be merged. Every one of those
// falls back to the serial path silently and exactly.
//
// Sort and join breakers remain serial. They still benefit, because their input
// arrives faster.
//
// The trade, stated: a parallel group-by holds roughly N times the group table,
// because each worker builds its own. That is why the memory limit disables it
// rather than dividing the budget.
func WithThreads(n int) CollectOption {
	return func(c *collectCfg) {
		if n < 1 {
			n = 1
		}
		c.threads = n
	}
}

// MemoryStats reports what a query actually held.
//
// Peak is the number that demonstrates bounded memory: that a query FINISHED
// proves only that it did not run out, while a peak far below the input size
// proves it never held the input at all.
type MemoryStats struct {
	// Peak is the largest total the query retained at one moment, in bytes.
	//
	// It counts what buffering operators HOLD ACROSS BATCHES, deduplicated by
	// allocation so a payload two operators share is counted once. It does not
	// count a kernel's transient output, because ursus's allocator reports
	// nothing and there is no hook at the allocation site.
	Peak int64

	// Limit is the ceiling that was in force, or 0 if there was none.
	Limit int64

	// Spills is how many files a spilling operator wrote: sorted runs for a sort,
	// radix partitions for a group-by, and for a join BOTH sides' partitions —
	// counted at every level of the recursion.
	//
	// It is a count of FILES, not of bytes and not of distinct spill events, and a
	// group-by that recursed reports more than one level's fan-out — which is the
	// only public evidence that a partition did not fit on the first try.
	Spills int64
}

// WithMemoryLimit caps what buffering operators may hold, in bytes.
//
//	df, err := ursus.ScanParquet("120gb/*.parquet").
//	    Sort(ursus.Desc(ursus.Col("ts"))).
//	    SinkParquet(ctx, "sorted.parquet",
//	        ursus.WithMemoryLimit(512<<20),
//	        ursus.WithSpillDir("/tmp/ursus"))
//
// # What it bounds
//
// Sort spills past the limit and merges the runs back, so a sort over more data
// than memory works. Group-by spills too, by radix-partitioning the keys it cannot
// hold: past the limit new keys are routed to one of sixteen files per level and
// re-aggregated afterwards. Join partitions BOTH sides on the join key and replays
// the pairs, so an equi-join over more data than memory works as well. Window,
// reverse, hstack, unique and tail cannot spill: past the limit they FAIL, with an
// error naming the operator, which is a better outcome than being killed by the OS
// with no explanation.
//
// A bounded sort — one under a Head or a TopK — never spills at all: past the
// limit it discards everything outside the current best k, which is exact.
//
// # The one thing the limit CAN change
//
// An unordered group-by's ROW ORDER depends on the limit, because the point at
// which it stops admitting new keys does. Past that point a new key goes to a
// partition file and comes back after every resident group, so the output is
// resident-first then partition-major. A spilling JOIN is the same, for the same
// reason: rows whose key stayed in memory come out in probe order, and the rest
// follow bucket by bucket.
//
// The CONTENT is invariant: same rows, same values, at every limit and at none.
// Only the sequence moves. A group-by can opt out with GroupBy.MaintainOrder, which
// restores first-appearance order exactly, spilled or not.
//
// A JOIN CANNOT, and that is a decision rather than an omission. Restoring probe
// order means holding the whole output to permute it, and a join's output can be
// larger than both of its inputs — so the flag would only work in the cases that did
// not need it. TestMemoryLimitIsNotASemanticKnob pins the content half for both, and
// TestJoinOrderIsUnspecifiedUnderALimit pins that the order really does move.
//
// # What a group-by's limit does NOT bound
//
// Partitioning divides the KEY SPACE, so it bounds a group-by whose problem is
// cardinality. It cannot divide a single key, and quantile, median and n_unique
// hold state per VALUE rather than per group — so a group-by over a few very large
// groups still fails, with an error that says which of the two situations it is in.
//
// The join has the same wall in two places: a CROSS join has no key at all, and one
// join key with more build rows than the limit cannot be split however deep the
// recursion goes. Both refuse with a message naming which it is.
//
// # What it does not bound
//
// Collect materialises the whole result by definition, and asking for a frame in
// memory is a request to hold it. The larger-than-RAM story runs through
// SinkParquet and CollectBatches, both of which stream.
func WithMemoryLimit(bytes int64) CollectOption {
	return func(c *collectCfg) { c.memLimit = bytes }
}

// WithSpillDir chooses where spill files are written. The default is the system
// temporary directory. Files are removed when the query finishes, including on
// error and on an early break out of CollectBatches.
func WithSpillDir(dir string) CollectOption {
	return func(c *collectCfg) { c.spillDir = dir }
}

// WithMemoryStats stores the query's memory accounting into *out when the query
// finishes.
//
// It is written at the end of Collect, Count, CollectInto, SinkParquet, SinkCSV,
// WriteParquet and WriteCSV, and after the last batch of CollectBatches — so
// reading it early gives a partial figure rather than a wrong one.
//
// The sinks matter most and were the ones this list used to omit: they are the
// consumers that stream, so they are where a memory limit is worth setting.
func WithMemoryStats(out *MemoryStats) CollectOption {
	return func(c *collectCfg) { c.stats = out }
}

func baseCollectCfg() collectCfg {
	return collectCfg{
		batchSize: physical.DefaultOptions().BatchSize,
		threads:   runtime.NumCPU(),
		flags:     plan.DefaultFlags(),
	}
}

// finish turns the accumulated knobs into the budget the operators share. It is
// separate from baseCollectCfg because the sinks apply their options through a
// different union type and still need exactly this step.
func (c *collectCfg) finish() {
	c.budget = execopt.NewBudget(c.memLimit, c.spillDir)
}

func newCollectCfg(opts []CollectOption) collectCfg {
	cfg := baseCollectCfg()
	for _, o := range opts {
		o(&cfg)
	}
	cfg.finish()
	return cfg
}

// report copies the budget's final figures out, if the caller asked for them.
func (c collectCfg) report() {
	if c.stats == nil {
		return
	}
	*c.stats = MemoryStats{
		Peak:   c.budget.Peak(),
		Limit:  c.budget.Limit(),
		Spills: c.budget.Spills(),
	}
}

// optimizer builds the rule set and gives it a constant folder.
//
// The injection is the whole reason plan.ConstEvaluator exists: constant folding
// needs the real kernel, the real kernel is at L50 with Arrow behind it, and
// internal/plan at L40 deliberately has no Arrow dependency at all. Wiring it
// here costs nothing — this file already imports internal/physical — and keeps
// `go test ./internal/plan` runnable without GOEXPERIMENT.
func optimizer() *plan.Optimizer {
	o := plan.NewOptimizer()
	o.SetConstEvaluator(physical.KernelFolder{})
	return o
}

// compile runs the full pipeline: resolve, optimize, lower to operators.
func (lf *LazyFrame) compile(ctx context.Context, cfg collectCfg) (physical.Operator, error) {
	if lf.err != nil {
		return nil, lf.err
	}
	resolved, err := plan.Resolve(ctx, lf.node)
	if err != nil {
		return nil, err
	}
	o := optimizer()
	o.Verify = cfg.verify
	optimized, err := o.Run(resolved, cfg.flags)
	if err != nil {
		return nil, err
	}
	return physical.PlanRoot(ctx, optimized, physical.Options{
		BatchSize: cfg.batchSize,
		Threads:   cfg.threads,
		Budget:    cfg.budget,
	})
}

// Collect runs the query and returns the whole result.
func (lf *LazyFrame) Collect(ctx context.Context, opts ...CollectOption) (*DataFrame, error) {
	cfg := newCollectCfg(opts)
	root, err := lf.compile(ctx, cfg)
	if err != nil {
		return nil, err
	}
	b, err := exec.Collect(ctx, root)
	cfg.report()
	if err != nil {
		return nil, err
	}
	return &DataFrame{batch: b}, nil
}

// CollectBatches streams the result without materialising the whole frame.
//
// Breaking out of the loop tears the operator tree down, so an early exit is safe.
func (lf *LazyFrame) CollectBatches(ctx context.Context, opts ...CollectOption) iter.Seq2[*DataFrame, error] {
	return func(yield func(*DataFrame, error) bool) {
		cfg := newCollectCfg(opts)
		defer cfg.report()
		root, err := lf.compile(ctx, cfg)
		if err != nil {
			yield(nil, err)
			return
		}
		for b, err := range exec.Batches(ctx, root) {
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(&DataFrame{batch: b}, nil) {
				return
			}
		}
	}
}

// Count runs the query and returns the row count, retaining no data.
func (lf *LazyFrame) Count(ctx context.Context, opts ...CollectOption) (int64, error) {
	cfg := newCollectCfg(opts)
	defer cfg.report()
	root, err := lf.compile(ctx, cfg)
	if err != nil {
		return 0, err
	}
	return exec.Count(ctx, root)
}

// CollectInto runs the query and decodes each row into a T.
//
// A generic METHOD, which is the Go 1.27 feature this library was waiting for:
// before, this had to be a package-level function taking the frame as an argument.
func (lf *LazyFrame) CollectInto[T any](ctx context.Context, opts ...CollectOption) ([]T, error) {
	df, err := lf.Collect(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return decodeRows[T](df.batch)
}

var _ = data.Batch{} // data is used through DataFrame
var _ dtype.TypeID   // dtype is used through the aliases above
