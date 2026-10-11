package physical

import (
	"context"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/plan"
)

// sliceOp skips rows and then keeps a bounded number of them.
//
// limitOp is this with the skip hard-wired to zero. They stay separate because
// limit pushdown matches a *plan.Limit structurally — the rule's whole safety
// argument is "match the node, do not descend" — so collapsing the two nodes would
// mean the rule had to start inspecting fields instead.
//
// Streaming and stateful: a cursor across batches, like limitOp and distinctOp.
type sliceOp struct {
	child     Operator
	skip      int
	remaining int // negative means unbounded
}

func (s *sliceOp) Schema() *dtype.Schema { return s.child.Schema() }
func (s *sliceOp) Close() error          { return s.child.Close() }

func (s *sliceOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if s.remaining == 0 {
			return nil, io.EOF
		}
		b, err := s.child.Next(ctx)
		if err != nil {
			return nil, err
		}

		if s.skip > 0 {
			if b.Rows() <= s.skip {
				s.skip -= b.Rows()
				continue // consumed entirely by the offset
			}
			b = b.Slice(s.skip, b.Rows()-s.skip)
			s.skip = 0
		}
		if s.remaining > 0 && b.Rows() > s.remaining {
			b = b.Slice(0, s.remaining)
		}
		if s.remaining > 0 {
			s.remaining -= b.Rows()
		}
		if b.Rows() == 0 {
			continue // an empty batch is not end-of-stream
		}
		return b, nil
	}
}

// tailOp keeps the last n rows.
//
// A ring of batches rather than a ring of rows: batches are immutable and already
// allocated, so dropping one from the front is free, whereas a row ring would copy
// every row twice. The cost is that it can hold up to n + (batchSize - 1) rows
// rather than exactly n — bounded either way, which is the property that matters.
type tailOp struct {
	child Operator
	n     int

	buf   []*data.Batch
	held  int
	out   Operator
	drawn bool

	// mem is accounted even though the ring is bounded by construction. A budget
	// that only tracks the operators it expects to be large is a budget that
	// reports a number nobody can check.
	mem *execopt.Account
}

func (t *tailOp) Schema() *dtype.Schema { return t.child.Schema() }

func (t *tailOp) Close() error {
	t.mem.Release()
	if t.out != nil {
		if err := t.out.Close(); err != nil {
			return err
		}
	}
	return t.child.Close()
}

func (t *tailOp) Next(ctx context.Context) (*data.Batch, error) {
	if !t.drawn {
		if err := t.drain(ctx); err != nil {
			return nil, err
		}
		t.drawn = true
	}
	return t.out.Next(ctx)
}

func (t *tailOp) drain(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := t.child.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		t.buf = append(t.buf, b)
		t.held += b.Rows()

		// Drop whole batches from the front while the remainder still covers n.
		for len(t.buf) > 0 && t.held-t.buf[0].Rows() >= t.n {
			t.held -= t.buf[0].Rows()
			t.buf = t.buf[1:]
		}

		// The ring EVICTS, so accounting is recomputed rather than accumulated.
		// It is bounded by n plus one batch, so this is a short loop.
		t.mem.Release()
		for _, held := range t.buf {
			t.mem.Retain(held)
		}
		if err := t.mem.Check(); err != nil {
			return err
		}
	}

	schema := t.child.Schema()
	if t.n <= 0 || len(t.buf) == 0 {
		empty, err := data.NewBatch(schema, emptyColumns(schema))
		if err != nil {
			return err
		}
		t.out = newBatchOperator(schema, empty)
		return nil
	}
	// The ring may hold more than n; trim the excess off the front batch.
	if excess := t.held - t.n; excess > 0 {
		first := t.buf[0]
		t.buf[0] = first.Slice(excess, first.Rows()-excess)
	}
	t.out = newBatchOperator(schema, t.buf...)
	return nil
}

// rowIndexOp prepends a counter.
//
// Stateful and streaming — the third operator shape. It is NOT a BatchOp, because
// the counter crosses batch boundaries, and it therefore joins the list of things
// that depend on the driver delivering batches in input order.
type rowIndexOp struct {
	child  Operator
	schema *dtype.Schema
	next   uint32
}

func (r *rowIndexOp) Schema() *dtype.Schema { return r.schema }
func (r *rowIndexOp) Close() error          { return r.child.Close() }

func (r *rowIndexOp) Next(ctx context.Context) (*data.Batch, error) {
	b, err := r.child.Next(ctx)
	if err != nil {
		return nil, err
	}
	n := b.Rows()
	idx := make([]uint32, n)
	for i := range n {
		idx[i] = r.next + uint32(i)
	}
	r.next += uint32(n)

	cols := make([]*data.Column, 0, b.NumCols()+1)
	cols = append(cols, data.NewFixed(r.schema.Field(0).Name, dtype.Uint32, idx,
		bitmap.AllSet(n)))
	cols = append(cols, b.Columns()...)
	return data.NewBatch(r.schema, cols)
}

// --- planning -------------------------------------------------------------------

func planSlice(ctx context.Context, s *plan.Slice, opts Options) (Operator, error) {
	child, err := planPipelined(ctx, s.Input, opts)
	if err != nil {
		return nil, err
	}
	return &sliceOp{child: child, skip: s.Offset, remaining: s.Len}, nil
}

func planTail(ctx context.Context, t *plan.Tail, opts Options) (Operator, error) {
	child, err := planPipelined(ctx, t.Input, opts)
	if err != nil {
		return nil, err
	}
	return &tailOp{child: child, n: t.N, mem: opts.Budget.Account("tail")}, nil
}

func planReverse(ctx context.Context, r *plan.Reverse, opts Options) (Operator, error) {
	in, err := r.Input.Schema()
	if err != nil {
		return nil, err
	}
	child, err := planPipelined(ctx, r.Input, opts)
	if err != nil {
		return nil, err
	}
	sink := &reverseSink{schema: in, mem: opts.Budget.Account("reverse"), budget: opts.Budget}
	return &breaker{child: child, sink: sink}, nil
}

func planRowIndex(ctx context.Context, r *plan.RowIndex, opts Options) (Operator, error) {
	out, err := r.Schema()
	if err != nil {
		return nil, err
	}
	child, err := planPipelined(ctx, r.Input, opts)
	if err != nil {
		return nil, err
	}
	return &rowIndexOp{child: child, schema: out, next: r.Offset}, nil
}
