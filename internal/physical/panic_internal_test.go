package physical

// A panic in any goroutine this package starts is an error, one site at a time.
//
// The drivers pull through Pull and guard what each worker runs. Each test here
// puts a fake that panics exactly where one site's recover has to catch it, and
// asserts an internal error and that every goroutine the driver started has exited.
// Without the recover these do not fail, they crash the test binary — which is the
// behaviour being fixed, and why the public cases run in a child process.

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// panicOp panics in Next: a source, or any operator below a dispatcher.
type panicOp struct{ schema *dtype.Schema }

func (p panicOp) Schema() *dtype.Schema                     { return p.schema }
func (p panicOp) Next(context.Context) (*data.Batch, error) { panic("source boom") }
func (p panicOp) Close() error                              { return nil }

// panicBatchOp panics in Apply: the work a parallelOp worker runs.
type panicBatchOp struct{ schema *dtype.Schema }

func (p panicBatchOp) Schema() *dtype.Schema { return p.schema }
func (p panicBatchOp) Apply(context.Context, *data.Batch) (*data.Batch, error) {
	panic("apply boom")
}
func (p panicBatchOp) Close() error { return nil }

func wantRecovered(t *testing.T, err error, value string) {
	t.Helper()
	if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), value) {
		t.Fatalf("want an internal error carrying %q, got %v", value, err)
	}
	var p *uerr.PanicError
	if !errors.As(err, &p) {
		t.Fatalf("want a recovered panic as the cause, got %v", err)
	}
}

// settles waits for the goroutine count to fall back to base, and fails if it
// does not: a driver that recovered its panic but left a worker blocked has leaked.
func settles(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines still running", runtime.NumGoroutine()-base)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func int64Schema() *dtype.Schema { return dtype.MustSchema(dtype.NotNull("v", dtype.Int64)) }

// stream yields n one-row batches of v = 0..n-1, and stops with ctx's error once
// ctx is done, as an operator must.
type stream struct {
	schema *dtype.Schema
	i, n   int
	onNext func(i int) // called with the index about to be returned
}

func (s *stream) Schema() *dtype.Schema { return s.schema }
func (s *stream) Close() error          { return nil }
func (s *stream) Next(ctx context.Context) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.i >= s.n {
		return nil, io.EOF
	}
	if s.onNext != nil {
		s.onNext(s.i)
	}
	c := data.NewFixed("v", dtype.Int64, []int64{int64(s.i)}, bitmap.AllSet(1))
	s.i++
	return data.NewBatch(s.schema, []*data.Column{c})
}

func TestParallelDispatcherPanicIsAnError(t *testing.T) {
	base := runtime.NumGoroutine()
	s := int64Schema()
	op := newParallelOp(s, panicOp{s}, []BatchOp{panicBatchOp{s}}, 4)
	_, err := op.Next(t.Context())
	wantRecovered(t, err, "source boom")
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}
	settles(t, base)
}

func TestParallelWorkerPanicIsAnError(t *testing.T) {
	base := runtime.NumGoroutine()
	s := int64Schema()
	op := newParallelOp(s, &stream{schema: s, n: 8}, []BatchOp{panicBatchOp{s}}, 4)
	_, err := op.Next(t.Context())
	wantRecovered(t, err, "apply boom")
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}
	settles(t, base)
}

// TestParProbeDispatcherPanicIsAnError: the probe child panics under the parallel
// probe's dispatcher. Its workers' own recovers are reached through the public
// raw-udf join case in panic_test.go.
func TestParProbeDispatcherPanicIsAnError(t *testing.T) {
	base := runtime.NumGoroutine()
	sink := joinFixture(t, plan.JoinInner)
	sink.threads = 4
	if err := sink.Consume(t.Context(), buildBatch(t, sink.right, []*int64{i64p(1)}, []int64{10})); err != nil {
		t.Fatal(err)
	}
	op, err := sink.Probe(t.Context(), panicOp{sink.left})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := op.(*parProbeOp); !ok {
		t.Fatalf("the fixture is wrong: the probe is a %T, not the parallel one", op)
	}
	_, err = op.Next(t.Context())
	wantRecovered(t, err, "source boom")
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}
	settles(t, base)
}

func TestParallelSinkDispatcherPanicIsAnError(t *testing.T) {
	base := runtime.NumGoroutine()
	s := int64Schema()
	sinks := []Sink{&countSink{schema: s}, &countSink{schema: s}}
	op := newParallelSink(panicOp{s}, sinks)
	_, err := op.Next(t.Context())
	wantRecovered(t, err, "source boom")
	settles(t, base)
}

// countSink panics in its first Consume once release closes, and counts every
// Consume that starts after that panic.
type countSink struct {
	schema  *dtype.Schema
	release chan struct{} // nil: never panics
	after   *atomic.Int64 // Consumes begun after the panic
	panics  *atomic.Bool  // set just before it
	once    sync.Once
}

func (c *countSink) Schema() *dtype.Schema { return c.schema }
func (c *countSink) Consume(context.Context, *data.Batch) error {
	if c.panics != nil && c.panics.Load() {
		c.after.Add(1)
	}
	if c.release == nil {
		return nil
	}
	first := false
	c.once.Do(func() { first = true })
	if first {
		<-c.release
		c.panics.Store(true)
		panic("consume boom")
	}
	return nil
}
func (c *countSink) Merge(Sink) error                         { return nil }
func (c *countSink) Finish(context.Context) (Operator, error) { return nil, errors.New("unreached") }
func (c *countSink) Close() error                             { return nil }

// TestParallelSinkWorkerPanicStopsConsuming is the drain rule.
//
// One worker, so its lane is the only one. Its first Consume waits until the
// dispatcher has queued two more batches behind it, then panics. Those two are
// then certainly in the lane, and the worker must drain them WITHOUT consuming:
// after a panic, that sink's state is not something anyone should feed.
//
// And the dispatcher, stopped by the cancellation that failure causes, must not
// add a "context canceled" of its own to the error — it is the failure's echo.
func TestParallelSinkWorkerPanicStopsConsuming(t *testing.T) {
	base := runtime.NumGoroutine()
	s := int64Schema()
	release := make(chan struct{})
	var after atomic.Int64
	var panics atomic.Bool
	sink := &countSink{schema: s, release: release, after: &after, panics: &panics}

	// parQueueDepth batches fit in the lane while the worker holds batch 0; being
	// asked for the next one means they are all queued.
	src := &stream{schema: s, n: 100, onNext: func(i int) {
		if i == parQueueDepth+1 {
			close(release)
		}
	}}
	op := newParallelSink(src, []Sink{sink})
	_, err := op.Next(t.Context())
	wantRecovered(t, err, "consume boom")
	if n := after.Load(); n != 0 {
		t.Errorf("%d batches were consumed after the panic", n)
	}
	if strings.Contains(err.Error(), "context canceled") {
		t.Errorf("the dispatcher's cancellation was reported beside the failure: %v", err)
	}
	settles(t, base)
}
