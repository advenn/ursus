package physical

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
)

// endsAtCancel is a source that ends, rather than failing, when the query is
// cancelled while it waits: the shape of a cancelled query whose lanes close.
type endsAtCancel struct {
	schema  *dtype.Schema
	waiting chan struct{}
	once    sync.Once
}

func (s *endsAtCancel) Schema() *dtype.Schema { return s.schema }
func (s *endsAtCancel) Close() error          { return nil }
func (s *endsAtCancel) Next(ctx context.Context) (*data.Batch, error) {
	s.once.Do(func() { close(s.waiting) })
	<-ctx.Done()
	return nil, io.EOF
}

// TestAClosedLaneUnderACancelIsTheCancel (step 434): a cancelled query closes the
// parallel operator's lanes, and a closed lane otherwise means the end of the input.
// Next, waiting on a lane when the cancel closes it, returns the cancel; read as the
// end, a cancelled query answered part of its input as the whole (step 394). The
// drivers' own check behind it kept that from reaching a caller, so it was recorded
// silent. 200 runs, each cancelled while Next waits.
func TestAClosedLaneUnderACancelIsTheCancel(t *testing.T) {
	s := int64Schema()
	for run := range 200 {
		ctx, cancel := context.WithCancel(t.Context())
		src := &endsAtCancel{schema: s, waiting: make(chan struct{})}
		op := newParallelOp(s, src, nil, 4)
		go func() {
			<-src.waiting
			time.Sleep(200 * time.Microsecond)
			cancel()
		}()
		_, err := op.Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: %v, want the cancel", run, err)
		}
		if err := op.Close(); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
}

// TestAClosedProbeLaneUnderACancelIsTheCancel: the parallel probe's lanes, the same
// (parjoin.go).
func TestAClosedProbeLaneUnderACancelIsTheCancel(t *testing.T) {
	for run := range 200 {
		sink := joinFixture(t, plan.JoinInner)
		sink.threads = 4
		if err := sink.Consume(t.Context(), buildBatch(t, sink.right, []*int64{i64p(1)}, []int64{10})); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		src := &endsAtCancel{schema: sink.left, waiting: make(chan struct{})}
		op, err := sink.Probe(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := op.(*parProbeOp); !ok {
			t.Fatalf("the fixture is wrong: the probe is a %T, not the parallel one", op)
		}
		go func() {
			<-src.waiting
			time.Sleep(200 * time.Microsecond)
			cancel()
		}()
		_, err = op.Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: %v, want the cancel", run, err)
		}
		if err := op.Close(); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
}
