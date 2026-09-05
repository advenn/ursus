package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
)

// Operator is the pull-based execution interface.
//
// # Pull now, push later, without rewriting the operators
//
// v0.1 executes a single-threaded volcano: the consumer calls Next and the tree
// pulls. Pull was chosen because it is debuggable (one stack trace shows the whole
// pipeline), it starts no goroutines so there is nothing to leak on cancellation,
// and it is the natural shape for CollectBatches where the caller drives.
//
// The morsel-driven push engine planned for v0.2 wants the opposite polarity. The
// move that makes that migration cheap is below: Filter and Project are NOT
// Operators. They are BatchOps — stateless per-batch transforms — and a 20-line
// adapter runs a BatchOp inside the pull chain. In v0.2 the same BatchOp values
// are handed to the scheduler unchanged. Zero kernels, zero evaluator and zero
// BatchOps have to change; only the driver does.
//
// # A driver MUST deliver batches in input order
//
// This is an obligation, not an accident, and it went unwritten for four steps
// while the only driver was a serial loop whose `append` happened to satisfy it.
// Five things depend on it:
//
//	sortSink        stability — ties keep their input order
//	positionAcc     First and Last are POSITIONAL
//	distinctOp      keeps the FIRST row per key
//	limitOp         which rows survive, not just how many
//	hashAggSink     group ids are assigned in first-appearance order
//
// None of the five would error if it were violated; they would return different
// rows. So a scheduler may run operators concurrently — parallelOp does — but it
// may not reorder what they produce. See internal/physical/parallel.go.
type Operator interface {
	Schema() *dtype.Schema

	// Next returns the next non-empty batch, or (nil, io.EOF) at end of stream.
	// Implementations must check ctx.Err() before doing work.
	Next(ctx context.Context) (*data.Batch, error)

	Close() error
}

// BatchOp is a stateless, order-independent transform of one batch into one batch.
//
// Every operator that CAN be a BatchOp MUST be one. That is the rule that keeps
// the pull-to-push migration mechanical.
type BatchOp interface {
	Schema() *dtype.Schema
	Apply(ctx context.Context, in *data.Batch) (*data.Batch, error)
	Close() error
}

// --- stage: the entire cost of having chosen pull ----------------------------

type stage struct {
	child Operator
	op    BatchOp
}

func (s *stage) Schema() *dtype.Schema { return s.op.Schema() }

func (s *stage) Next(ctx context.Context) (*data.Batch, error) {
	for {
		in, err := s.child.Next(ctx)
		if err != nil {
			return nil, err
		}
		out, err := s.op.Apply(ctx, in)
		if err != nil {
			return nil, err
		}
		// A batch that filtered down to nothing is not end-of-stream. Returning it
		// would make the consumer stop early; the loop is what keeps Filter honest.
		if out.Rows() == 0 {
			continue
		}
		return out, nil
	}
}

func (s *stage) Close() error { return errors.Join(s.op.Close(), s.child.Close()) }

// --- scan --------------------------------------------------------------------

type scanOp struct {
	src    source.BatchSource
	schema *dtype.Schema
}

func (s *scanOp) Schema() *dtype.Schema { return s.schema }
func (s *scanOp) Close() error          { return s.src.Close() }

func (s *scanOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := s.src.Next(ctx)
		if err != nil {
			return nil, err
		}
		if b.Rows() == 0 {
			continue // an empty batch is not end-of-stream
		}
		return b, nil
	}
}

// --- filter ------------------------------------------------------------------

type filterOp struct {
	schema *dtype.Schema
	preds  []expr.Node
}

func (f *filterOp) Schema() *dtype.Schema { return f.schema }
func (f *filterOp) Close() error          { return nil }

func (f *filterOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	out := in
	// Conjuncts are applied in sequence rather than combined into one AND tree.
	// Each one shrinks the batch, so later predicates evaluate over fewer rows —
	// and it keeps the Kleene combination out of the hot path entirely.
	for _, p := range f.preds {
		mask, err := evalColumn(ctx, p, out)
		if err != nil {
			return nil, err
		}
		out, err = kernel.FilterBatch(out, mask)
		if err != nil {
			return nil, err
		}
		if out.Rows() == 0 {
			break
		}
	}
	return out, nil
}

// --- project -----------------------------------------------------------------

type projectOp struct {
	schema *dtype.Schema
	exprs  []expr.Node
}

func (p *projectOp) Schema() *dtype.Schema { return p.schema }
func (p *projectOp) Close() error          { return nil }

func (p *projectOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	cols := make([]*data.Column, len(p.exprs))
	for i, e := range p.exprs {
		c, err := evalColumn(ctx, e, in)
		if err != nil {
			return nil, err
		}
		cols[i] = c.Rename(p.schema.Field(i).Name)
	}
	if len(cols) == 0 {
		return data.NewBatchRows(p.schema, nil, in.Rows()), nil
	}
	return data.NewBatch(p.schema, cols)
}

func broadcast(c *data.Column, n int) (*data.Column, error) {
	sel := make([]int32, n)
	// All zeros: gather row 0 n times.
	return kernel.Take(c, sel)
}

// evalColumn evaluates e against b and returns a column of exactly b.Rows() rows.
//
// # Why this is a function and not three lines repeated at each call site
//
// An expression with no column reference — Lit(1), or a comparison of two
// literals — evaluates to a length-1 column, because that is all the information
// there is. Every operator that puts an evaluated column next to the batch's other
// columns therefore has to broadcast it first.
//
// Six operators evaluate expressions against a batch. Three of them broadcast and
// three did not, and the consequences differed by which:
//
//	GroupBy(Lit(1))   PANICKED — the group-key encoder indexed row 1 of a 1-row column
//	Sort(Lit(1))      failed as "a bug in ursus" from a batch row-count mismatch
//	Filter(Lit(true)) reached FilterBatch with a mask shorter than the batch
//
// Three call sites with the guard and three without is the same shape as the
// running-schema walk that step 2's audit found four copies of, two already
// diverged. The fix is the same: one definition, so a seventh operator gets it by
// construction rather than by remembering.
//
// Broadcasting belongs HERE rather than inside Eval. Eval recurses, and the
// kernels already accept a length-1 operand and repeat it; broadcasting at every
// level would materialise n copies of a literal for each node of an expression
// tree instead of once at the boundary.
func evalColumn(ctx context.Context, e expr.Node, b *data.Batch) (*data.Column, error) {
	c, err := Eval(ctx, e, b)
	if err != nil {
		return nil, err
	}
	if c.Len() == b.Rows() {
		return c, nil
	}
	if c.Len() != 1 {
		return nil, uerr.Internalf(
			"physical: expression %s produced %d rows for a %d-row batch",
			e.String(), c.Len(), b.Rows())
	}
	return broadcast(c, b.Rows())
}

// --- limit -------------------------------------------------------------------

type limitOp struct {
	child     Operator
	remaining int
}

func (l *limitOp) Schema() *dtype.Schema { return l.child.Schema() }
func (l *limitOp) Close() error          { return l.child.Close() }

func (l *limitOp) Next(ctx context.Context) (*data.Batch, error) {
	if l.remaining <= 0 {
		return nil, io.EOF
	}
	b, err := l.child.Next(ctx)
	if err != nil {
		return nil, err
	}
	if b.Rows() > l.remaining {
		b = b.Slice(0, l.remaining)
	}
	l.remaining -= b.Rows()
	return b, nil
}

// --- planning ----------------------------------------------------------------

// Options tunes physical planning.
type Options struct {
	BatchSize int

	// Threads is the number of workers over the pipeline portion of the plan.
	// 0 or 1 means serial, and reproduces the pre-step-5 operator tree exactly.
	Threads int

	// Budget is the query's memory ceiling and spill location, shared by every
	// buffering operator in the tree. A nil Budget means unlimited and untracked,
	// so an Options built by hand still works.
	Budget *execopt.Budget
}

// batchSize returns the configured batch size, or the default when Options was
// built by hand and left it zero.
func (o Options) batchSize() int {
	if o.BatchSize <= 0 {
		return DefaultOptions().BatchSize
	}
	return o.BatchSize
}

// DefaultOptions returns sensible planning defaults.
//
// 8192 rows is the usual columnar sweet spot: large enough that per-batch overhead
// and SIMD setup amortise away, small enough that a batch's working set stays in
// L2 and that a cancelled query stops promptly.
func DefaultOptions() Options {
	return Options{BatchSize: 8192, Threads: 1, Budget: execopt.NewBudget(0, "")}
}

// PlanRoot lowers a plan and parallelises the top of it if it is a pipeline.
//
// Plan itself parallelises each child that a NON-pipeline operator consumes; this
// covers the remaining case, a query that is nothing but a pipeline. The two
// together mean every maximal pipeline in the tree is wrapped exactly once.
func PlanRoot(ctx context.Context, n plan.Node, opts Options) (Operator, error) {
	op, err := Plan(ctx, n, opts)
	if err != nil {
		return nil, err
	}
	return parallelise(op, opts), nil
}

// planPipelined plans a child and parallelises it.
//
// Called from exactly the operators that CONSUME a pipeline without being part of
// one — the breakers and the stateful operators. That is the boundary: below it
// everything is a stateless BatchOp and safe to run N-ways; at and above it lives
// the mutable state that step 5 deliberately leaves serial.
func planPipelined(ctx context.Context, n plan.Node, opts Options) (Operator, error) {
	op, err := Plan(ctx, n, opts)
	if err != nil {
		return nil, err
	}
	return parallelise(op, opts), nil
}

// Plan lowers a resolved, optimized logical plan into an operator tree.
//
// It may assume the plan is resolved: every Scan has its source schema, every
// Project holds single-output expressions, and every Filter predicate is Boolean.
// Resolve guarantees all three, so violations here are ursus bugs, not user errors.
func Plan(ctx context.Context, n plan.Node, opts Options) (Operator, error) {
	switch t := n.(type) {
	case *plan.Scan:
		return planScan(ctx, t, opts)

	case *plan.Filter:
		child, err := Plan(ctx, t.Input, opts)
		if err != nil {
			return nil, err
		}
		s, err := t.Schema()
		if err != nil {
			return nil, err
		}
		return &stage{child: child, op: &filterOp{schema: s, preds: t.Preds}}, nil

	case *plan.Project:
		child, err := Plan(ctx, t.Input, opts)
		if err != nil {
			return nil, err
		}
		s, err := t.Schema()
		if err != nil {
			return nil, err
		}
		return &stage{child: child, op: &projectOp{schema: s, exprs: t.Exprs}}, nil

	case *plan.Limit:
		child, err := planPipelined(ctx, t.Input, opts)
		if err != nil {
			return nil, err
		}
		return &limitOp{child: child, remaining: t.N}, nil

	case *plan.WithColumns:
		return planWithColumns(ctx, t, opts)

	case *plan.Explode:
		return planExplode(ctx, t, opts)

	case *plan.Aggregate:
		return planAggregate(ctx, t, opts)

	case *plan.TemporalGroup:
		return planTemporalGroup(ctx, t, opts)

	case *plan.AsOfJoin:
		return planAsOfJoin(ctx, t, opts)

	case *plan.MergeSorted:
		return planMergeSorted(ctx, t, opts)

	case *plan.Window:
		return planWindow(ctx, t, opts)

	case *plan.Union:
		return planUnion(ctx, t, opts)
	case *plan.HStack:
		return planHStack(ctx, t, opts)

	case *plan.Slice:
		return planSlice(ctx, t, opts)
	case *plan.Tail:
		return planTail(ctx, t, opts)
	case *plan.Reverse:
		return planReverse(ctx, t, opts)
	case *plan.RowIndex:
		return planRowIndex(ctx, t, opts)

	case *plan.Sort:
		return planSort(ctx, t, opts)

	case *plan.Distinct:
		return planDistinct(ctx, t, opts)

	case *plan.Join:
		return planJoin(ctx, t, opts)

	default:
		return nil, uerr.Internalf("physical: no operator for plan node %T", n)
	}
}

func planScan(ctx context.Context, s *plan.Scan, opts Options) (Operator, error) {
	op, ok := s.Src.(source.Openable)
	if !ok {
		return nil, uerr.New(uerr.KindUnsupported, "scan",
			"source %q cannot produce data", s.Src.Name()).
			Hint("it implements plan.Source but not source.Openable")
	}

	// Predicate and MaxRows are hints the source may ignore; the plan still holds
	// whatever enforces them (a Filter above an inexact scan, the Limit that
	// produced MaxRows). Passing them is what makes pushdown pay, not what makes
	// it correct — see source.ScanSpec.
	spec := source.ScanSpec{
		Projection: s.Projection,
		Predicate:  s.Predicate,
		MaxRows:    s.MaxRows,
		BatchSize:  opts.BatchSize,
	}
	src, err := op.Open(ctx, spec)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "scan", "opening %s source", s.Src.Name())
	}

	want, err := s.Schema()
	if err != nil {
		return nil, err
	}

	// If the source could not honour the projection itself, do it here. The
	// logical plan always records the projection regardless of source capability,
	// so Explain shows the optimizer's reasoning even for a source that ignores it.
	got := src.Schema()
	if !got.Equal(want) {
		return &stage{
			child: &scanOp{src: src, schema: got},
			op:    newTrimOp(want),
		}, nil
	}
	return &scanOp{src: src, schema: want}, nil
}

// trimOp reorders and subsets a batch's columns to match a target schema.
type trimOp struct {
	target *dtype.Schema
}

func newTrimOp(target *dtype.Schema) BatchOp { return &trimOp{target: target} }

func (t *trimOp) Schema() *dtype.Schema { return t.target }
func (t *trimOp) Close() error          { return nil }

func (t *trimOp) Apply(_ context.Context, in *data.Batch) (*data.Batch, error) {
	cols := make([]*data.Column, t.target.Len())
	for i, f := range t.target.All() {
		c, ok := in.ByName(f.Name)
		if !ok {
			return nil, uerr.UnknownColumn("scan", f.Name, in.Schema().Names())
		}
		cols[i] = c
	}
	return data.NewBatch(t.target, cols)
}
