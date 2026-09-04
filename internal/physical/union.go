package physical

import (
	"context"
	"errors"
	"io"

	"ursus/dtype"
	"ursus/internal/data"
	"ursus/internal/execopt"
	"ursus/internal/kernel"
	"ursus/internal/plan"
	"ursus/internal/uerr"
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
// # It buffers, and there is no way around it
//
// Nothing aligns batch boundaries across independent pipelines: one child may
// deliver 8192 rows while another delivers 100, and pairing row i of each means
// having row i of each in hand. So every child is drained and concatenated, and
// memory is O(total rows) — the same shape as sortSink, for a much simpler reason.
//
// A re-chunking scheme would bound it, at the cost of slicing every child on every
// boundary — and Column.Slice copies for fixed-width types, so it would trade
// memory for a copy per column per boundary rather than removing the cost.
type hstackOp struct {
	children []Operator
	schema   *dtype.Schema
	out      Operator
	drawn    bool
	mem      *execopt.Account
}

func (h *hstackOp) Schema() *dtype.Schema { return h.schema }

func (h *hstackOp) Close() error {
	h.mem.Release()
	errs := make([]error, 0, len(h.children)+1)
	if h.out != nil {
		errs = append(errs, h.out.Close())
	}
	for _, c := range h.children {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

func (h *hstackOp) Next(ctx context.Context) (*data.Batch, error) {
	if !h.drawn {
		if err := h.draw(ctx); err != nil {
			return nil, err
		}
		h.drawn = true
	}
	return h.out.Next(ctx)
}

func (h *hstackOp) draw(ctx context.Context) error {
	cols := make([]*data.Column, 0, h.schema.Len())
	rows := -1
	for i, child := range h.children {
		var parts []*data.Batch
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			b, err := child.Next(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			parts = append(parts, b)
			h.mem.Retain(b)
			if err := h.mem.Check(); err != nil {
				return err
			}
		}
		cs := child.Schema()
		var one *data.Batch
		var err error
		if len(parts) == 0 {
			if one, err = data.NewBatch(cs, emptyColumns(cs)); err != nil {
				return err
			}
		} else if one, err = kernel.Concat(cs, parts); err != nil {
			return err
		}

		if rows < 0 {
			rows = one.Rows()
		} else if one.Rows() != rows {
			return uerr.New(uerr.KindValue, "hstack",
				"frame %d has %d rows, frame 0 has %d", i, one.Rows(), rows).
				Hint("hstack pairs rows positionally, so every frame must be the " +
					"same height").
				Hint("use a join to combine frames of different heights by key")
		}
		cols = append(cols, one.Columns()...)
	}

	out, err := data.NewBatchRows(h.schema, cols, max(rows, 0)), error(nil)
	if err != nil {
		return err
	}
	h.out = newBatchOperator(h.schema, out)
	return nil
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
