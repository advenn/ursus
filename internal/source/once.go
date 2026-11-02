package source

import (
	"context"
	"sync"
)

// Once runs a source's first read, its schema's, until an answer is kept (step 418).
// It is sync.Once but for two things a query's context asks of it:
//
//   - a run that fails because its caller's context ended is not kept, so the next
//     caller runs it again. sync.Once kept it, and one cancelled Collect made every
//     later Collect of the same LazyFrame fail with its context.Canceled;
//   - a caller waiting on another's run gives up when its own context ends, where it
//     waited as long as the run.
//
// One run goes at a time. What a run writes to its source is read by the callers
// after Do returns, ordered by Once's lock. The zero Once is ready.
type Once struct {
	mu      sync.Mutex
	done    bool
	err     error
	running chan struct{} // closed when the run in progress ends; nil when none is
}

// Do runs f under ctx where no run's answer is kept and none is in progress, and
// returns the kept answer, f's, or ctx's error if ctx ends while it waits.
func (o *Once) Do(ctx context.Context, f func(ctx context.Context) error) error {
	for {
		o.mu.Lock()
		if o.done {
			err := o.err
			o.mu.Unlock()
			return err
		}
		if wait := o.running; wait != nil {
			o.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		end := make(chan struct{})
		o.running = end
		o.mu.Unlock()
		return o.run(ctx, f, end)
	}
}

// run runs f and keeps its answer unless ctx ended under it. A panic passes through
// with nothing kept, and the next caller runs f again.
func (o *Once) run(ctx context.Context, f func(ctx context.Context) error, end chan struct{}) (err error) {
	keep := false
	defer func() {
		o.mu.Lock()
		o.running = nil
		if keep {
			o.done, o.err = true, err
		}
		o.mu.Unlock()
		close(end)
	}()
	err = f(ctx)
	keep = err == nil || ctx.Err() == nil
	return err
}
