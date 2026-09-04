package physical

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// parallelOp runs a pure pipeline — a source plus a chain of stateless BatchOps —
// across N workers, and emits the results in INPUT ORDER.
//
// # Why order is preserved rather than abandoned
//
// The serial driver has always supplied an implicit total ordering: exec.Collect's
// `append` is what makes output batch order equal input batch order. Five things
// depend on it and none of them said so — sortSink's stability, positionAcc's
// First and Last, distinctOp's "keep the first row per key", limitOp's "which rows
// survive", and hashAggSink's first-appearance group ids. Abandoning order here
// would break all five at once, silently, and would change the observable answer
// of every query in a library whose tests compare rendered frames.
//
// So this is an ORDERED worker pool, and the ordering is structural rather than
// enforced by a sort at the end.
//
// # How the ordering is guaranteed
//
// One dispatcher pulls source batches serially and hands them to workers
// ROUND-ROBIN; the consumer reads results back round-robin in the same order.
// Batch i therefore goes to worker i%N and is read at position i. No sequence
// numbers, no reorder buffer, no heap — the order falls out of the topology.
//
// It also self-bounds: each worker has a fixed-capacity queue, so a slow morsel
// stalls its own lane rather than letting the scan run ahead and materialise the
// whole input. That is the difference between "parallel" and "parallel until it
// runs out of memory".
//
// # What is parallel and what is not
//
// The BatchOps are. Pulling from the source is not — it happens in the dispatcher,
// under whatever lock the source already holds. That is the right split: reading a
// batch is I/O or a slice bump, and evaluating expressions over it is the CPU work.
// It also means this does not actually depend on sources being concurrency-safe,
// though they all are.
type parallelOp struct {
	schema *dtype.Schema
	base   Operator  // the source end of the pipeline
	ops    []BatchOp // applied in order, per batch, in a worker
	n      int

	start   sync.Once
	results []chan parResult
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	turn int // next lane to read from; round-robin is what makes this ordered
	done bool
}

// parQueueDepth is how many batches may be in flight per worker.
//
// Small on purpose. The whole point of bounding it is that a skewed workload
// cannot turn into "materialise the entire scan"; two deep gives a worker
// something to start on while its previous result is being consumed, and no more.
const parQueueDepth = 2

type parJob struct {
	b   *data.Batch
	err error // a source error, delivered in sequence rather than out of band
}

type parResult struct {
	b   *data.Batch // nil when the batch filtered away to nothing
	err error
}

func newParallelOp(schema *dtype.Schema, base Operator, ops []BatchOp, threads int) *parallelOp {
	return &parallelOp{schema: schema, base: base, ops: ops, n: threads}
}

func (p *parallelOp) Schema() *dtype.Schema { return p.schema }

func (p *parallelOp) Next(ctx context.Context) (*data.Batch, error) {
	p.start.Do(func() { p.launch(ctx) })

	for {
		if p.done {
			return nil, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		r, ok := <-p.results[p.turn]
		p.turn = (p.turn + 1) % p.n

		// Round-robin dispatch is monotone: worker i receives batches i, i+N,
		// i+2N… so once the lane whose turn it is has closed, every later lane in
		// this round has too. That is why one closed lane means end of stream.
		if !ok {
			p.done = true
			return nil, io.EOF
		}
		if r.err != nil {
			p.done = true
			return nil, r.err
		}
		if r.b == nil {
			continue // filtered to nothing; not end of stream
		}
		return r.b, nil
	}
}

func (p *parallelOp) launch(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel

	jobs := make([]chan parJob, p.n)
	p.results = make([]chan parResult, p.n)
	for i := range p.n {
		jobs[i] = make(chan parJob, parQueueDepth)
		p.results[i] = make(chan parResult, parQueueDepth)
	}

	// Dispatcher: pull serially, hand out round-robin.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			for i := range jobs {
				close(jobs[i])
			}
		}()

		for seq := 0; ; seq++ {
			b, err := p.base.Next(ctx)
			job := parJob{b: b}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				// Deliver the error at its position in the sequence rather than out
				// of band, so it cannot overtake results already in flight.
				job = parJob{err: err}
			}
			select {
			case jobs[seq%p.n] <- job:
			case <-ctx.Done():
				return
			}
			if job.err != nil {
				return
			}
		}
	}()

	// Workers: apply the BatchOp chain. Every job produces exactly one result, so
	// the lanes stay aligned with the dispatch order even when a batch filters away
	// to nothing.
	for i := range p.n {
		p.wg.Add(1)
		go func(i int) {
			defer p.wg.Done()
			defer close(p.results[i])

			for job := range jobs[i] {
				var res parResult
				if job.err != nil {
					res = parResult{err: job.err}
				} else {
					res = p.apply(ctx, job.b)
				}
				select {
				case p.results[i] <- res:
				case <-ctx.Done():
					return
				}
				if res.err != nil {
					return
				}
			}
		}(i)
	}
}

// apply runs the BatchOp chain over one batch.
//
// It mirrors stage.Next's rule that a batch filtered down to nothing is not
// end-of-stream: the result is reported as empty and the consumer skips it, rather
// than being confused for EOF.
func (p *parallelOp) apply(ctx context.Context, b *data.Batch) parResult {
	out := b
	for _, op := range p.ops {
		next, err := op.Apply(ctx, out)
		if err != nil {
			return parResult{err: err}
		}
		out = next
		if out.Rows() == 0 {
			return parResult{}
		}
	}
	if out.Rows() == 0 {
		return parResult{}
	}
	return parResult{b: out}
}

// Close tears the workers down and then the pipeline.
//
// Cancelling first is what makes an early break from CollectBatches safe: a worker
// blocked sending into a full lane wakes on ctx.Done rather than leaking. The wait
// is unconditional, so no goroutine outlives the operator.
func (p *parallelOp) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()

	var errs []error
	for _, op := range p.ops {
		errs = append(errs, op.Close())
	}
	return errors.Join(append(errs, p.base.Close())...)
}

// --- deciding what to parallelise ---------------------------------------------------

// parallelise wraps op in a worker pool if it is a pure pipeline.
//
// A "pure pipeline" is an operator chain of stages over some base — every stage's
// BatchOp is stateless and order-independent by the standing rule that everything
// which CAN be a BatchOp MUST be. Anything else is returned untouched, so pipeline
// breakers and their stateful consumers (limitOp, distinctOp, breaker,
// joinBreaker, batchOperator) keep running exactly as they did.
//
// That restriction is not a compromise. Those five carry unguarded mutable state —
// distinctOp's `seen` is a bare map, whose concurrent write is a runtime throw, not
// a race — and parallelising them needs a per-worker construction path that Sink
// does not have and a group-id remap that hashAggSink documents as absent.
func parallelise(op Operator, opts Options) Operator {
	if opts.Threads <= 1 {
		return op
	}
	base, ops, ok := pipelineOf(op)
	if !ok || len(ops) == 0 {
		// A bare source with no work to do gains nothing from workers and would only
		// pay for the channels.
		return op
	}
	return newParallelOp(op.Schema(), base, ops, opts.Threads)
}

// pipelineOf decomposes a chain of stages into its base and its BatchOps, in
// application order. ok is false if the chain is not rooted in something that can
// be pulled from serially.
func pipelineOf(op Operator) (base Operator, ops []BatchOp, ok bool) {
	for {
		s, isStage := op.(*stage)
		if !isStage {
			// Reverse into application order: the walk above went outermost-in.
			for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
				ops[i], ops[j] = ops[j], ops[i]
			}
			return op, ops, true
		}
		ops = append(ops, s.op)
		op = s.child
	}
}
