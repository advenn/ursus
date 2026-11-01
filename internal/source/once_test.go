package source

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestOnceKeepsAnAnswerButACancellation: a run's answer, a failure among them, is
// kept and f runs once however many call; a run cut short by its caller's
// cancellation is not, and the next caller runs f again.
func TestOnceKeepsAnAnswerButACancellation(t *testing.T) {
	var o Once
	var runs atomic.Int32
	broken := errors.New("broken file")
	f := func(context.Context) error { runs.Add(1); return broken }
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := o.Do(t.Context(), f); err != broken {
				t.Errorf("got %v, want the kept failure", err)
			}
		})
	}
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Errorf("f ran %d times, want 1", n)
	}

	var c Once
	ctx, cancel := context.WithCancel(t.Context())
	err := c.Do(ctx, func(ctx context.Context) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled run: %v", err)
	}
	ran := false
	if err := c.Do(t.Context(), func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("the next caller: ran %v, %v; want a run of its own", ran, err)
	}
}

// TestOnceWaitersKeepTheirDeadlines: while a run is held, a waiter under a 50 ms
// deadline returns its own context's error when it passes, and one with time to
// spare takes the run's answer.
func TestOnceWaitersKeepTheirDeadlines(t *testing.T) {
	var o Once
	hold, started := make(chan struct{}), make(chan struct{})
	go o.Do(context.Background(), func(context.Context) error { close(started); <-hold; return nil })
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if err := o.Do(ctx, func(context.Context) error { t.Error("a waiter ran f"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the waiter: got %v, want its deadline", err)
	}
	if waited := time.Since(t0); waited > 5*time.Second {
		t.Errorf("the waiter waited %v", waited)
	}
	patient := make(chan error, 1)
	go func() { patient <- o.Do(t.Context(), func(context.Context) error { return errors.New("ran again") }) }()
	close(hold)
	if err := <-patient; err != nil {
		t.Errorf("the patient waiter: %v, want the run's answer", err)
	}
}

// TestOncePanicKeepsNothing: a run's panic passes through to its caller, keeps
// nothing and releases its waiters, so the next caller runs f again.
func TestOncePanicKeepsNothing(t *testing.T) {
	var o Once
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not pass through")
			}
		}()
		o.Do(t.Context(), func(context.Context) error { panic("bug") })
	}()
	ran := false
	if err := o.Do(t.Context(), func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("after a panic: ran %v, %v", ran, err)
	}
}
