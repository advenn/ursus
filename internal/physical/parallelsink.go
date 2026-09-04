package physical

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// SinkFactory builds one worker's Sink. Every sink a factory returns must be
// independently usable and mutually mergeable — same schema, same aggregates, same
// numbering rules.
type SinkFactory func() (Sink, error)

// parallelSink drains a pipeline breaker on N workers.
//
// # This is the shape sink.go was written for
//
// Sink's own doc has said since step 1 that "Consume/Merge/Finish is the shape a
// parallel scheduler needs. N workers each get their own Sink, consume morsels
// independently, and the partial results are merged", and until this step nothing
// called Merge at all — the only live `.Merge(` in the module was the
// Accumulator.Merge inside the uncalled hashAggSink.Merge. This is that scheduler,
// for the one breaker whose merge is well-defined.
//
// # Why only the aggregate
//
// Checked against the other two rather than assumed:
//
//	sort   sortSink.Consume evaluates key columns and RETAINS; the sort itself is
//	       in Finish, so parallelising Consume buys almost nothing. Worse, its
//	       Merge concatenates in input order precisely to keep the sort stable, and
//	       round-robin dispatch scrambles exactly that.
//
//	join   joinBuildSink.Merge is already complete and even catches cross-sink
//	       duplicate keys that Validate would otherwise miss. But its doc says
//	       appending works because "other consumed a LATER portion of the input …
//	       which is what makes the emitted match order agree with a single-sink
//	       run", and round-robin breaks that premise. The build side is also the
//	       smaller input, so it is the least certain payoff.
//
// # The ordering contract, and why the gate is a predicate rather than a hope
//
// Sink.Merge's contract is that `other` consumed a LATER portion of the input.
// Round-robin dispatch does NOT satisfy it: worker 0 holds batches 0, N, 2N…, so
// worker 1's batch 1 precedes worker 0's batch N and neither sink holds anything
// contiguous. Rather than weaken the contract, this driver is only used where the
// merge does not depend on it — see aggCanParallelise, which declines
// MaintainOrder and the four order-dependent aggregates outright.
type parallelSink struct {
	child Operator
	sinks []Sink

	out  Operator
	done bool
}

func newParallelSink(child Operator, sinks []Sink) *parallelSink {
	return &parallelSink{child: child, sinks: sinks}
}

func (p *parallelSink) Schema() *dtype.Schema { return p.sinks[0].Schema() }

func (p *parallelSink) Next(ctx context.Context) (*data.Batch, error) {
	if !p.done {
		if err := p.drain(ctx); err != nil {
			return nil, err
		}
		p.done = true
	}
	return p.out.Next(ctx)
}

// drain consumes the whole child on N workers, folds the partials together and
// finishes the survivor.
func (p *parallelSink) drain(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	n := len(p.sinks)
	jobs := make([]chan *data.Batch, n)
	for i := range n {
		jobs[i] = make(chan *data.Batch, parQueueDepth)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
		cancel() // stop the dispatcher and the other workers promptly
	}

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for b := range jobs[i] {
				if err := p.sinks[i].Consume(ctx, b); err != nil {
					fail(err)
					// Keep draining rather than returning: the dispatcher may already
					// be blocked sending into this lane, and abandoning it would
					// deadlock until ctx cancellation reached it.
					continue
				}
			}
		}(i)
	}

	// Dispatcher: pull serially, hand out round-robin. Pulling is deliberately not
	// parallel — the same split parallelOp makes, for the same reason: reading a
	// batch is I/O, and the CPU work is what the workers do with it.
	var dispatchErr error
	for seq := 0; ; seq++ {
		b, err := p.child.Next(ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				dispatchErr = err
			}
			break
		}
		select {
		case jobs[seq%n] <- b:
		case <-ctx.Done():
			dispatchErr = ctx.Err()
		}
		if dispatchErr != nil {
			break
		}
	}
	for i := range jobs {
		close(jobs[i])
	}
	wg.Wait()

	if dispatchErr != nil {
		errs = append(errs, dispatchErr)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if err := parent.Err(); err != nil {
		return err
	}

	// Fold in worker order. Which order is immaterial here BY CONSTRUCTION —
	// aggCanParallelise has already established that the merge is commutative —
	// but it is fixed rather than arbitrary so that a given input produces a given
	// group ordering every run.
	for i := 1; i < n; i++ {
		if err := p.sinks[0].Merge(p.sinks[i]); err != nil {
			return err
		}
	}

	out, err := p.sinks[0].Finish(ctx)
	if err != nil {
		return err
	}
	p.out = out
	return nil
}

func (p *parallelSink) Close() error {
	errs := make([]error, 0, len(p.sinks)+2)
	if p.out != nil {
		errs = append(errs, p.out.Close())
	}
	for _, s := range p.sinks {
		errs = append(errs, s.Close())
	}
	errs = append(errs, p.child.Close())
	return errors.Join(errs...)
}
