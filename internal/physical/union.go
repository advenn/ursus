package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// concatOp emits every child's rows, in order.
//
// # It is not a breaker, and that is the whole difference from a join
//
// joinBreaker exists because a join cuts a DAG: the build side must reach EOF
// before the probe side is pulled at all, and that asymmetry has to live
// somewhere. A concat has no asymmetry — each child is drained in turn and nothing
// is buffered — so it is a plain streaming Operator with a cursor.
//
// By the time it runs, resolveUnion has made every child produce the output schema
// exactly, so this does no reconciliation of its own. That matters: kernel.Concat
// pairs columns positionally and never checks the schema it is given, so a mismatch
// reaching this far would splice payloads rather than fail.
type concatOp struct {
	children []Operator
	schema   *dtype.Schema
	i        int
}

func (c *concatOp) Schema() *dtype.Schema { return c.schema }

// Close closes every child, including the ones already exhausted and the ones never
// reached — the flat rule joinBreaker uses, for the same reason: a conditional
// close is a leak waiting for an early break.
func (c *concatOp) Close() error {
	errs := make([]error, 0, len(c.children))
	for _, ch := range c.children {
		errs = append(errs, ch.Close())
	}
	return errors.Join(errs...)
}

func (c *concatOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if c.i >= len(c.children) {
			return nil, io.EOF
		}
		b, err := c.children[c.i].Next(ctx)
		if errors.Is(err, io.EOF) {
			c.i++
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.Rows() == 0 {
			continue // an empty batch is not end-of-stream
		}
		if b.Schema().Equal(c.schema) {
			return b, nil
		}
		// Nullability is allowed to differ, and only nullability. The output is
		// nullable wherever ANY input can contribute a null — including a frame that
		// simply lacks the column — while a child that happens to have no nulls
		// still declares itself non-null. Relabelling to the output schema is the
		// right resolution: a non-nullable column is a valid nullable one, and
		// NewBatch still checks the names and types, so a genuine mismatch is caught
		// here rather than spliced by kernel.Concat downstream.
		out, err := data.NewBatch(c.schema, b.Columns())
		if err != nil {
			return nil, uerr.Internalf(
				"physical: concat child %d produced schema %s, want %s — resolveUnion "+
					"should have adapted it: %v", c.i, b.Schema(), c.schema, err)
		}
		return out, nil
	}
}

func planUnion(ctx context.Context, u *plan.Union, opts Options) (Operator, error) {
	out, err := u.Schema()
	if err != nil {
		return nil, err
	}
	kids := make([]Operator, len(u.Inputs))
	for i, in := range u.Inputs {
		// planPipelined, not Plan: each child is its own pipeline and parallelises
		// independently. The concat itself stays serial, which is what preserves
		// the row order the output promises.
		op, err := planPipelined(ctx, in, opts)
		if err != nil {
			return nil, err
		}
		kids[i] = op
	}
	return &concatOp{children: kids, schema: out}, nil
}

// hstackOp places frames side by side.
//
// # In lockstep (v0.6.3 step 218)
//
// Nothing aligns batch boundaries across independent pipelines: one child may
// deliver 8192 rows while another delivers 100. Until step 218 every child was
// drained and concatenated, so memory was O(total rows), and past the budget it
// refused. Pairing row i of each needs only row i of each in hand: hstackOp keeps one
// batch per child and where it is in it, and each output batch is the fewest rows any
// child has left, sliced from each. Column.Slice shares the buffers, fixed-width
// included, so a boundary costs a view, not a copy.
//
// A child that ends before the others is a height mismatch, refused as it always
// was, naming the first frame whose height differs from frame 0's: the others are
// read to their ends to count them. Rows paired before it have already been given
// out, as mergeSortedOp's out-of-order refusal does.
type hstackOp struct {
	children []Operator
	schema   *dtype.Schema

	cur     []*data.Batch // each child's batch in hand, nil once it has ended
	off     []int         // the next row of it
	rows    []int         // rows each child has delivered
	ended   []bool
	emitted bool // whether a batch has been given out
	done    bool
	mem     *execopt.Account // the batches in hand
}

func (h *hstackOp) Schema() *dtype.Schema { return h.schema }

func (h *hstackOp) Close() error {
	h.mem.Release()
	errs := make([]error, 0, len(h.children))
	for _, c := range h.children {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

func (h *hstackOp) Next(ctx context.Context) (*data.Batch, error) {
	if h.done {
		return nil, io.EOF
	}
	if h.cur == nil {
		k := len(h.children)
		h.cur, h.off, h.rows, h.ended = make([]*data.Batch, k), make([]int, k), make([]int, k), make([]bool, k)
	}
	n, anyEnded, allEnded := -1, false, true
	for i := range h.children {
		if err := h.fill(ctx, i); err != nil {
			return nil, err
		}
		if h.ended[i] {
			anyEnded = true
			continue
		}
		allEnded = false
		if left := h.cur[i].Rows() - h.off[i]; n < 0 || left < n {
			n = left
		}
	}
	switch {
	case allEnded:
		h.done = true
		if !h.emitted {
			// No row at all: one empty batch, so the schema is still delivered.
			h.emitted = true
			return data.NewBatch(h.schema, emptyColumns(h.schema))
		}
		return nil, io.EOF
	case anyEnded:
		h.done = true
		return nil, h.mismatch(ctx)
	}
	cols := make([]*data.Column, 0, h.schema.Len())
	for i, b := range h.cur {
		for _, c := range b.Columns() {
			cols = append(cols, c.Slice(h.off[i], n))
		}
		h.off[i] += n
	}
	h.emitted = true
	return data.NewBatchRows(h.schema, cols, n), nil
}

// fill gives child i a batch with rows left in hand, or marks it ended.
func (h *hstackOp) fill(ctx context.Context, i int) error {
	for !h.ended[i] && (h.cur[i] == nil || h.off[i] >= h.cur[i].Rows()) {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := h.children[i].Next(ctx)
		if errors.Is(err, io.EOF) {
			h.cur[i], h.ended[i] = nil, true
			break
		}
		if err != nil {
			return err
		}
		h.cur[i], h.off[i] = b, 0
		h.rows[i] += b.Rows()
	}
	h.recharge()
	return nil
}

// recharge charges the batches in hand.
func (h *hstackOp) recharge() {
	h.mem.Release()
	for _, b := range h.cur {
		if b != nil {
			h.mem.Retain(b)
		}
	}
}

// mismatch reads every child to its end, and names the first whose height differs
// from frame 0's.
func (h *hstackOp) mismatch(ctx context.Context) error {
	for i := range h.children {
		for !h.ended[i] {
			h.off[i] = h.cur[i].Rows() // counted already; read on
			if err := h.fill(ctx, i); err != nil {
				return err
			}
		}
	}
	for i, n := range h.rows {
		if n != h.rows[0] {
			return uerr.New(uerr.KindValue, "hstack",
				"frame %d has %d rows, frame 0 has %d", i, n, h.rows[0]).
				Hint("hstack pairs rows positionally, so every frame must be the " +
					"same height").
				Hint("use a join to combine frames of different heights by key")
		}
	}
	return uerr.Internalf("physical: hstack's frames ended apart with the same heights")
}

func planHStack(ctx context.Context, h *plan.HStack, opts Options) (Operator, error) {
	out, err := h.Schema()
	if err != nil {
		return nil, err
	}
	kids := make([]Operator, len(h.Inputs))
	for i, in := range h.Inputs {
		op, err := planPipelined(ctx, in, opts)
		if err != nil {
			return nil, err
		}
		kids[i] = op
	}
	return &hstackOp{children: kids, schema: out, mem: opts.Budget.Account("hstack")}, nil
}
