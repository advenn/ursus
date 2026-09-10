package physical

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// unpivotOp turns k value columns into a variable/value pair, k rows per input row.
//
// # It is a BatchOp, for explode's reason
//
// "BatchOp is 'a stateless, order-independent transform of one batch into one
// batch' — and a row count that differs from the input is already inside that
// contract, because Filter is a BatchOp and drops rows." Changing the COLUMN count
// is inside it too, which unnestOp says projectOp established. So this needs no new
// operator shape and rides parallelOp unchanged.
//
// # Two gathers and one build, which is explodeOp's shape
//
// Output row i*k+j holds input row i's index columns, the name On[j], and On[j]'s
// value at row i. Everything follows from two selection vectors:
//
//	idxSel[i*k+j] = i        one entry per output row: which input row
//	valSel[i*k+j] = j*n + i  one entry per output row: which cell of the k columns
//	                         laid end to end
//
// valSel is what makes the value column one gather rather than an interleave: the k
// columns are concatenated (they are already the same promoted type by then), which
// puts On[j]'s row i at position j*n+i, and the Take permutes that into row-major.
type unpivotOp struct {
	schema *dtype.Schema
	idx    []int    // input column indices carried through, in output order
	on     []int    // input column indices being melted
	names  []string // On, in the same order as `on`
	valDT  dtype.DataType
}

func (u *unpivotOp) Schema() *dtype.Schema { return u.schema }
func (u *unpivotOp) Close() error          { return nil }

func (u *unpivotOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := in.Rows()
	k := len(u.on)
	if n == 0 {
		// An empty batch keeps its shape rather than being rebuilt: the output
		// schema differs from the input's, so the caller still needs a batch of the
		// right shape, and Take over an empty selection gives one.
		return u.build(in, nil, nil, 0)
	}

	out := n * k
	idxSel := make([]int32, out)
	valSel := make([]int32, out)
	for i := range n {
		for j := range k {
			idxSel[i*k+j] = int32(i)
			// j*n+i, NOT i*k+j: this indexes the CONCATENATION of the k columns,
			// where column j occupies [j*n, (j+1)*n).
			valSel[i*k+j] = int32(j*n + i)
		}
	}
	return u.build(in, idxSel, valSel, out)
}

func (u *unpivotOp) build(in *data.Batch, idxSel, valSel []int32, out int) (*data.Batch, error) {
	cols := make([]*data.Column, 0, len(u.idx)+2)

	for _, ci := range u.idx {
		c, err := kernel.Take(in.Column(ci), idxSel)
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}

	// The variable column: the k names cycling, n times. Built rather than gathered
	// because there is nothing to gather from — it is the only column in the engine
	// whose values come from the QUERY rather than from the data.
	vars := make([]string, out)
	k := len(u.on)
	for i := range out {
		vars[i] = u.names[i%k]
	}
	cols = append(cols,
		data.NewString(u.schema.Field(len(u.idx)).Name, vars, bitmap.AllSet(out)))

	// The value column: cast each melted column to the promoted type, concatenate,
	// then permute into row-major.
	parts := make([]*data.Column, len(u.on))
	for i, ci := range u.on {
		c := in.Column(ci)
		if c.DType() != u.valDT {
			var err error
			// Strict: the promotion the schema computed is lossless by
			// construction, so a value that will not fit is a bug here rather than
			// a data problem, and a silent truncation would be the worst outcome.
			if c, err = kernel.Cast(c.Name(), u.valDT, true, c); err != nil {
				return nil, err
			}
		}
		parts[i] = c
	}
	valName := u.schema.Field(len(u.idx) + 1).Name
	val, err := kernel.ConcatColumns(valName, parts)
	if err != nil {
		return nil, err
	}
	if val, err = kernel.Take(val, valSel); err != nil {
		return nil, err
	}
	cols = append(cols, val.Rename(valName))

	return data.NewBatchRows(u.schema, cols, out), nil
}

// planUnpivot lowers plan.Unpivot.
//
// The node's Schema has already resolved every name, refused an overlap and
// promoted the value type, so this only finds indices — and it publishes the
// schema the node computed, so the two cannot drift.
func planUnpivot(ctx context.Context, u *plan.Unpivot, opts Options) (Operator, error) {
	child, err := planPipelined(ctx, u.Input, opts)
	if err != nil {
		return nil, err
	}
	out, err := u.Schema()
	if err != nil {
		return nil, err
	}
	in, err := u.Input.Schema()
	if err != nil {
		return nil, err
	}

	find := func(name string) (int, error) {
		i := in.IndexOf(name)
		if i < 0 {
			return 0, uerr.Internalf("physical: unpivot column %q vanished after "+
				"the plan resolved it", name)
		}
		return i, nil
	}

	index := u.IndexColumns(in)
	op := &unpivotOp{
		schema: out,
		idx:    make([]int, len(index)),
		on:     make([]int, len(u.On)),
		names:  u.On,
		valDT:  out.Field(out.Len() - 1).Type,
	}
	for i, name := range index {
		if op.idx[i], err = find(name); err != nil {
			return nil, err
		}
	}
	for i, name := range u.On {
		if op.on[i], err = find(name); err != nil {
			return nil, err
		}
	}
	return &stage{child: child, op: op}, nil
}
