package physical

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// parProbeOp runs the join's probe side across N workers, in INPUT ORDER.
//
// # Why the probe and not the build
//
// The probe is where the time is: 95.5% of join samples, against a build side that
// runs once. And it is the half that can be shared without a lock — joinTable's own
// doc says "nothing mutates it after freeze, which is what would let a future morsel
// scheduler share one table across N probe workers with no lock", and KeyTable.Get
// performs no writes, which is what makes that true rather than aspirational.
//
// The build side stays serial here. joinBuildSink.Merge exists and is tested for it,
// and is still never called.
//
// # Order, and the one place this differs from parallelOp
//
// The topology is parallelOp's: one dispatcher pulls probe batches serially and
// hands them out ROUND-ROBIN, and the consumer reads lanes back round-robin. Batch i
// goes to worker i%N and is read at position i, so order is structural rather than
// restored by a sort.
//
// parallelOp can rely on "every job produces exactly one result", which keeps its
// lanes aligned by counting. A probe job produces ZERO, ONE OR MANY — one probe row
// can match thousands of build rows, and h2o's j5 turns 10M probe rows into 9M output
// rows split at batchSize. So each worker follows its outputs for job i with a
// TERMINATOR, and the consumer drains a lane until the terminator before advancing.
// Order still falls out of the topology, because lane i holds job i's outputs in
// order and is drained whole.
//
// That is also why parallel.go is untouched: its parResult documents the
// exactly-one invariant, and this needs a different one rather than a weaker version
// of that one.
//
// # Ownership
//
// ProbeBuilder.Probe's contract: the returned operator does NOT own the probe
// operator — joinBreaker owns and closes it. Close here cancels, waits, and releases
// only what this allocated. Closing the probe child as well would double-close it.
type parProbeOp struct {
	schema  *dtype.Schema
	probe   Operator
	workers []*joinProbeOp
	n       int

	start   sync.Once
	results []chan probeResult
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	turn int // the lane to drain; round-robin is what makes this ordered
	done bool
}

// probeResult is one message in a lane.
//
// last marks the end of one input batch's output. It is not "end of stream" — the
// lane stays open for this worker's next job — and it is not carried by b == nil,
// because a job that produces no rows at all still has to say so or the consumer
// would wait on a lane that has nothing more coming for this position.
type probeResult struct {
	b    *data.Batch
	err  error
	last bool
}

func newParProbeOp(schema *dtype.Schema, probe Operator, workers []*joinProbeOp) *parProbeOp {
	return &parProbeOp{schema: schema, probe: probe, workers: workers, n: len(workers)}
}

func (p *parProbeOp) Schema() *dtype.Schema { return p.schema }

func (p *parProbeOp) Next(ctx context.Context) (*data.Batch, error) {
	p.start.Do(func() { p.launch(ctx) })

	for {
		if p.done {
			return nil, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		r, ok := <-p.results[p.turn]
		if !ok {
			// Dispatch is monotone — worker i gets batches i, i+N, i+2N — so once the
			// lane whose turn it is has closed, every later lane in this round has too.
			p.done = true
			return nil, io.EOF
		}
		if r.err != nil {
			p.done = true
			return nil, r.err
		}
		if r.last {
			// This input batch is finished. ONLY here does the turn advance: moving on
			// after the first result would interleave two workers' output and silently
			// reorder rows, which is invisible on a join with fan-out 1.
			p.turn = (p.turn + 1) % p.n
			continue
		}
		if r.b == nil {
			continue // matched nothing; not end of stream
		}
		return r.b, nil
	}
}

func (p *parProbeOp) launch(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel

	jobs := make([]chan parJob, p.n)
	p.results = make([]chan probeResult, p.n)
	for i := range p.n {
		jobs[i] = make(chan parJob, parQueueDepth)
		p.results[i] = make(chan probeResult, parQueueDepth)
	}

	// Dispatcher: pull serially, hand out round-robin. Pulling is not parallel and
	// should not be — it is the probe child's own work, which may itself be a
	// parallelOp.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			for i := range jobs {
				close(jobs[i])
			}
		}()

		for seq := 0; ; seq++ {
			b, err := p.probe.Next(ctx)
			job := parJob{b: b}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				// Delivered at its position in the sequence rather than out of band, so
				// it cannot overtake results already in flight.
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

	for i := range p.n {
		p.wg.Add(1)
		go func(i int) {
			defer p.wg.Done()
			defer close(p.results[i])

			w := p.workers[i]
			for job := range jobs[i] {
				if err := p.runJob(ctx, w, job, i); err != nil {
					return
				}
			}
		}(i)
	}
}

// runJob probes one input batch and sends its output, terminator last.
//
// The probing itself is joinProbeOp.stepCurrent — the same code the serial path
// runs, including the hit/nHit resume that makes output batch-size invariant. A
// worker owns its cursor and its selection arrays; the only thing it shares is the
// frozen table.
func (p *parProbeOp) runJob(ctx context.Context, w *joinProbeOp, job parJob, lane int) error {
	send := func(r probeResult) bool {
		select {
		case p.results[lane] <- r:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if job.err != nil {
		send(probeResult{err: job.err})
		return job.err
	}
	if err := w.startBatch(ctx, job.b); err != nil {
		send(probeResult{err: err})
		return err
	}
	for w.cur != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, err := w.stepCurrent()
		if err != nil {
			send(probeResult{err: err})
			return err
		}
		if out != nil && !send(probeResult{b: out}) {
			return context.Canceled
		}
	}
	if !send(probeResult{last: true}) {
		return context.Canceled
	}
	return nil
}

// Close tears the workers down. It does NOT close the probe child — joinBreaker owns
// that, per ProbeBuilder.Probe's contract, and closing it here would double-close it.
//
// Cancelling before waiting is what makes an early break from CollectBatches safe: a
// worker blocked sending into a full lane wakes on ctx.Done rather than leaking.
func (p *parProbeOp) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()

	var errs []error
	for _, w := range p.workers {
		errs = append(errs, w.Close())
	}
	return errors.Join(errs...)
}
