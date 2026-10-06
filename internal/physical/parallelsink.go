package physical

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
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

// errWantSerial is a worker's sink saying it has reached the memory budget and would
// freeze — which a sink that must still be merged cannot. The batch it was given is
// already consumed; the driver stops dispatching, folds the workers together, and
// finishes the input serially on the merged sink, which may then freeze and spill.
var errWantSerial = errors.New("physical: the budget is reached; finish serially")

// serialSink is a sink that can be told it is alone again.
type serialSink interface{ goSerial() }

// discarder is a sink whose state can be dropped once it has been merged.
type discarder interface{ discard() }

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
		wg     sync.WaitGroup
		mu     sync.Mutex
		errs   []error
		failed atomic.Bool
		serial atomic.Bool // a worker reached the budget: stop dispatching
	)
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
		failed.Store(true)
		cancel() // stop the dispatcher and the other workers promptly
	}

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// fail cancels the dispatcher, so a worker that Goexits — and so stops
			// draining its lane — cannot leave the dispatcher blocked sending into it.
			goexitGuard(fail, func() {
				for b := range jobs[i] {
					// Once anything has failed, drain without consuming. Draining
					// rather than returning is because the dispatcher may already be
					// blocked sending into this lane, and abandoning it would deadlock
					// until ctx cancellation reached it. Not consuming is because the
					// query has already failed — and after a panic, the sink that
					// panicked is in no state anyone should feed.
					if failed.Load() {
						continue
					}
					// Guarded: this is a worker goroutine, and a panic in it could be
					// recovered by nothing else.
					err := uerr.GuardErr("", func() error { return p.sinks[i].Consume(ctx, b) })
					if errors.Is(err, errWantSerial) {
						serial.Store(true)
						continue
					}
					if err != nil {
						fail(err)
					}
				}
			})
		}(i)
	}

	// Dispatcher: pull serially, hand out round-robin. Pulling is deliberately not
	// parallel — the same split parallelOp makes, for the same reason: reading a
	// batch is I/O, and the CPU work is what the workers do with it.
	var dispatchErr error
	for seq := 0; !serial.Load(); seq++ {
		b, err := Pull(ctx, p.child)
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

	// A worker's failure cancels ctx, and the dispatcher then stops with
	// context.Canceled — which is the failure's echo, not a second error. It is
	// only news when the caller's own context was cancelled.
	if dispatchErr != nil && !(len(errs) > 0 && errors.Is(dispatchErr, context.Canceled) && parent.Err() == nil) {
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
		if d, ok := p.sinks[i].(discarder); ok {
			d.discard()
		}
	}

	// A worker reached the budget: the rest of the input goes to the merged sink,
	// alone, where it may freeze and spill as the serial path always has.
	if serial.Load() {
		if s, ok := p.sinks[0].(serialSink); ok {
			s.goSerial()
		}
		for {
			b, err := Pull(ctx, p.child)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if err := p.sinks[0].Consume(ctx, b); err != nil {
				return err
			}
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
