package physical

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// unnestOp replaces each named struct column with its fields, in place.
//
// # It is a BatchOp, and a small one
//
// The row count does not change and nothing is gathered: a struct's fields are
// parallel to it, already the right length, so the operation is a splice. Changing
// the COLUMN count is already inside the BatchOp contract — projectOp does it — which
// is the half of explode's argument that carries over without needing to be made
// again.
//
// # The null struct is the one decision
//
// A field of an absent struct is absent, so the struct's validity is ANDed into each
// field on the way out. Today every struct comes from the Parquet reader, which has
// already marked the fields null wherever the group is absent — a definition level
// below the group's is below the field's too — so the mask changes nothing for them.
// It is here because NOTHING ENFORCES that: data.NewStruct does not check, and Slice,
// takeStruct and concatStruct carry whatever they are handed. This is where the rule
// is applied rather than assumed, and TestUnnestMasksFieldsOfANullStruct builds the
// inconsistent column by hand so the mask is not dead code.
type unnestOp struct {
	schema *dtype.Schema
	cols   []int // indices of the struct columns, in input schema order
}

func (u *unnestOp) Schema() *dtype.Schema { return u.schema }
func (u *unnestOp) Close() error          { return nil }

func (u *unnestOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	unnested := make(map[int]bool, len(u.cols))
	for _, ci := range u.cols {
		unnested[ci] = true
	}

	cols := make([]*data.Column, 0, u.schema.Len())
	for ci := range in.NumCols() {
		c := in.Column(ci)
		if !unnested[ci] {
			cols = append(cols, c)
			continue
		}
		if c.DType().ID() != dtype.TypeStruct {
			return nil, uerr.Internalf("physical: unnest column %d is a %s", ci, c.DType())
		}
		for _, f := range c.Fields() {
			cols = append(cols, maskBy(f, c.Validity()))
		}
	}
	if len(cols) != u.schema.Len() {
		return nil, uerr.Internalf("physical: unnest produced %d columns, schema has %d",
			len(cols), u.schema.Len())
	}
	return data.NewBatchRows(u.schema, cols, in.Rows()), nil
}

// maskBy nulls a field everywhere the struct it belongs to is null.
//
// The AND is skipped when the struct has no nulls, which is the common case and
// which also keeps a field's own no-storage validity — the form that makes
// downstream kernels take their fast path — from being replaced by a full bitmap
// for nothing.
func maskBy(f *data.Column, valid bitmap.View) *data.Column {
	if valid.CountUnset() == 0 {
		return f
	}
	return f.WithValidity(bitmap.And(f.Validity(), valid))
}

// planUnnest lowers plan.Unnest.
//
// The node's Schema has already resolved the names, refused a non-struct and refused
// a name collision, so this only finds indices — and it publishes the schema the node
// computed, so the plan and the operator cannot drift.
func planUnnest(ctx context.Context, u *plan.Unnest, opts Options) (Operator, error) {
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

	cols := make([]int, len(u.Columns))
	for i, name := range u.Columns {
		cols[i] = in.IndexOf(name)
		if cols[i] < 0 {
			return nil, uerr.Internalf("physical: unnest column %q vanished after "+
				"the plan resolved it", name)
		}
	}
	return &stage{child: child, op: &unnestOp{schema: out, cols: cols}}, nil
}
