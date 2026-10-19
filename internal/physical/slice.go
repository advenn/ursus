package physical

import (
	"context"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
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
	return &tailOp{child: child, n: t.N, limited: t.Limited, take: t.Len,
		mem: opts.Budget.Account("tail"), budget: opts.Budget}, nil
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
