package physical

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/plan"
)

// Profiler measures each node of a plan as it runs: LazyFrame.Profile, Polars'
// LazyFrame.profile (step 369, P1 of v0.6.8-scope.md). Options.Profiler sets it;
// without one, Plan builds no wrapper and a query pays nothing.
//
// # What is wrapped
//
// Plan wraps what it lowers each node to, and the wrappers are of two kinds:
//
//   - a node's batch transforms, the BatchOps of its stages, in a profBatchOp each,
//     which times Apply on whichever worker runs it: a stage the parallel pool runs
//     is found by its type (pipelineOf), so it is the BatchOp inside that is wrapped,
//     and the pipeline runs as it would;
//   - a node's operators, in a profOp each: the one its consumer pulls, its edge,
//     timing how long the consumer waited on it, and one of its own beneath its
//     stages, its base, as a scan's source under the stage that trims its columns.
//
// A stage chain's edge is a profOp pipelineOf sees through, and where the chain is
// parallelised, planPipelined and PlanRoot time the parallel operator in its place.
//
// # Own time
//
// A node's own time is its batch transforms' time, summed over the workers that ran
// them, and its operators' time less the time they waited on its inputs. Where an
// operator's work runs on workers beside its input's, as a parallel aggregation's
// does, only what it did beyond its wait is its own; it is never below zero.
type Profiler struct {
	start     time.Time
	optimized int64 // ns from the start to the optimized plan

	mu    sync.Mutex
	roots []*ProfNode
	cur   *ProfNode // the node being planned, which an account opened now is charged to
	last  *ProfNode // the node Plan last planned, which planPipelined's edge is timed for
}

// NewProfiler starts a profile at start, the moment its query began, and has
// budget tell it of each account an operator opens.
func NewProfiler(start time.Time, budget *execopt.Budget) *Profiler {
	p := &Profiler{start: start}
	budget.OnAccount(func(a *execopt.Account) {
		p.mu.Lock()
		if p.cur != nil {
			p.cur.accounts = append(p.cur.accounts, a)
		}
		p.mu.Unlock()
	})
	return p
}

// MarkOptimized notes that the plan is resolved and optimized, now: the profile's
// first row, "optimization", as Polars' is.
func (p *Profiler) MarkOptimized() {
	if p != nil {
		p.optimized = p.since()
	}
}

// ProfNode is one plan node's measurements.
type ProfNode struct {
	label    string
	depth    int
	kids     []*ProfNode
	accounts []*execopt.Account
	hasBase  bool // it has operators of its own, whose time less its inputs' is its own

	rows, batches atomic.Int64
	incl          atomic.Int64 // ns its consumers waited on it
	base          atomic.Int64 // ns in its own operators, its inputs' wait included
	apply         atomic.Int64 // ns in its batch transforms, summed over workers
	first, last   atomic.Int64 // ns since the start, of its first work and its last; -1 none
}

// enter opens n's node, a child of the node being planned.
func (p *Profiler) enter(n plan.Node) *ProfNode {
	p.mu.Lock()
	defer p.mu.Unlock()
	pn := &ProfNode{label: n.Label()}
	pn.first.Store(-1)
	pn.last.Store(-1)
	if p.cur == nil {
		p.roots = append(p.roots, pn)
	} else {
		pn.depth = p.cur.depth + 1
		p.cur.kids = append(p.cur.kids, pn)
	}
	parent := p.cur
	p.cur = pn
	return parent
}

// leave closes the node being planned, returning to parent.
func (p *Profiler) leave(parent *ProfNode) *ProfNode {
	p.mu.Lock()
	defer p.mu.Unlock()
	pn := p.cur
	p.cur = parent
	p.last = pn
	return pn
}

// wrap wraps op, what Plan lowered pn's node to.
func (p *Profiler) wrap(op Operator, pn *ProfNode) Operator {
	s, ok := op.(*stage)
	if !ok {
		pn.hasBase = true
		return &profOp{inner: op, p: p, node: pn, edge: true, base: true, count: true}
	}
	// Its own stages, from the top, and an operator of its own beneath them.
	top := true
	for {
		if _, theirs := s.op.(*profBatchOp); theirs {
			break
		}
		s.op = &profBatchOp{inner: s.op, p: p, node: pn, count: top}
		top = false
		if c, ok := s.child.(*stage); ok {
			s = c
			continue
		}
		if _, theirs := s.child.(*profOp); !theirs {
			s.child = &profOp{inner: s.child, p: p, node: pn, base: true}
			pn.hasBase = true
		}
		break
	}
	return &profOp{inner: op, p: p, node: pn, edge: true, stageEdge: true}
}

// edge times a parallelised pipeline, par, in place of its top node's stage edge,
// which pipelineOf saw through.
func (p *Profiler) edge(par Operator) Operator {
	return &profOp{inner: par, p: p, node: p.last, edge: true}
}

// since is the time since the profile began, in ns.
func (p *Profiler) since() int64 { return int64(time.Since(p.start)) }

// span notes work from t0 to t1 on pn.
func (pn *ProfNode) span(t0, t1 int64) {
	for {
		f := pn.first.Load()
		if (f >= 0 && f <= t0) || pn.first.CompareAndSwap(f, t0) {
			break
		}
	}
	for {
		l := pn.last.Load()
		if l >= t1 || pn.last.CompareAndSwap(l, t1) {
			break
		}
	}
}

// profOp times an operator of a node: its edge, its base, or both.
type profOp struct {
	inner Operator
	p     *Profiler
	node  *ProfNode

	edge      bool // its time is how long the consumer waited on the node
	base      bool // its time is the node's own, its inputs' wait included
	count     bool // its batches are the node's output
	stageEdge bool // it is a stage chain's edge, which pipelineOf sees through
}

func (o *profOp) Schema() *dtype.Schema { return o.inner.Schema() }
func (o *profOp) Close() error          { return o.inner.Close() }

func (o *profOp) Next(ctx context.Context) (*data.Batch, error) {
	t0 := o.p.since()
	b, err := o.inner.Next(ctx)
	t1 := o.p.since()
	o.node.span(t0, t1)
	if o.edge {
		o.node.incl.Add(t1 - t0)
	}
	if o.base {
		o.node.base.Add(t1 - t0)
	}
	if o.count && b != nil {
		o.node.rows.Add(int64(b.Rows()))
		o.node.batches.Add(1)
	}
	return b, err
}

// profBatchOp times a node's batch transform, on the worker that runs it.
type profBatchOp struct {
	inner BatchOp
	p     *Profiler
	node  *ProfNode
	count bool // its batches are the node's output
}

func (o *profBatchOp) Schema() *dtype.Schema { return o.inner.Schema() }
func (o *profBatchOp) Close() error          { return o.inner.Close() }

func (o *profBatchOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	t0 := o.p.since()
	b, err := o.inner.Apply(ctx, in)
	t1 := o.p.since()
	o.node.span(t0, t1)
	o.node.apply.Add(t1 - t0)
	// A batch filtered to nothing is not handed on; stage drops it.
	if o.count && b != nil && b.Rows() > 0 {
		o.node.rows.Add(int64(b.Rows()))
		o.node.batches.Add(1)
	}
	return b, err
}

// ProfRow is one node's line of a profile, in the order Explain prints the plan.
type ProfRow struct {
	Label      string
	Depth      int
	Ran        bool  // whether it did any work, which Start and End are of
	Start, End int64 // ns since the query began
	Rows       int64
	Batches    int64
	Own        int64 // ns
	Peak       int64 // bytes: the most each of its operators held at once, summed
}

// Rows returns each node's line, once the query has run and closed.
func (p *Profiler) Rows() []ProfRow {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []ProfRow{{Label: "optimization", Ran: true, End: p.optimized, Own: p.optimized}}
	// A node's span covers its inputs': it began when it, or one of them, first
	// worked for it. A pipeline's transform works only once its input has, and on
	// workers it has no edge of its own whose wait would cover its input's.
	var spans func(pn *ProfNode) (int64, int64)
	spans = func(pn *ProfNode) (int64, int64) {
		f, l := pn.first.Load(), pn.last.Load()
		for _, k := range pn.kids {
			kf, kl := spans(k)
			if kf >= 0 && (f < 0 || kf < f) {
				f = kf
			}
			l = max(l, kl)
		}
		pn.first.Store(f)
		pn.last.Store(l)
		return f, l
	}
	for _, r := range p.roots {
		spans(r)
	}
	var walk func(pn *ProfNode)
	walk = func(pn *ProfNode) {
		own := pn.apply.Load()
		if pn.hasBase {
			waited := int64(0)
			for _, k := range pn.kids {
				waited += k.incl.Load()
			}
			own += max(pn.base.Load()-waited, 0)
		}
		r := ProfRow{
			Label: pn.label, Depth: pn.depth,
			Rows: pn.rows.Load(), Batches: pn.batches.Load(), Own: own,
		}
		if f := pn.first.Load(); f >= 0 {
			r.Ran, r.Start, r.End = true, f, pn.last.Load()
		}
		for _, a := range pn.accounts {
			r.Peak += a.Peak()
		}
		out = append(out, r)
		for _, k := range pn.kids {
			walk(k)
		}
	}
	for _, r := range p.roots {
		walk(r)
	}
	return out
}
