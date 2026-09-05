package physical

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// explodeOp turns each row's list into one row per element.
//
// # It is a BatchOp, and that is most of the design
//
// BatchOp is "a stateless, order-independent transform of one batch into one
// batch" — and a row count that differs from the input is already inside that
// contract, because Filter is a BatchOp and drops rows. So explode needs no new
// operator shape, no Sink and no breaker, and it rides parallelOp unchanged.
//
// # Two gathers and one index build
//
// Everything the operation does is expressed as two kernel.Take calls over index
// lists built in one pass:
//
//	rowSel    one entry per OUTPUT row, holding the input row it came from
//	childSel  one entry per output row, holding the element it came from
//
// Take already maps NullIndex to a null, which is what makes an empty list and a
// null list cost nothing extra: both append NullIndex and the null falls out. The
// only place those two cases are handled at all is the `s == e` test below.
type explodeOp struct {
	schema *dtype.Schema
	cols   []int // indices of the columns to explode, in schema order
}

func (e *explodeOp) Schema() *dtype.Schema { return e.schema }
func (e *explodeOp) Close() error          { return nil }

func (e *explodeOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := in.Rows()
	if n == 0 {
		return in, nil
	}

	// The first exploded column decides the shape; every other one must agree.
	accs := make([]data.ListAccessor, len(e.cols))
	for i, ci := range e.cols {
		accs[i] = in.Column(ci).Lists()
	}

	rowSel := make([]int32, 0, n)
	childSel := make([][]int32, len(e.cols))

	for row := range n {
		start, end, ok := accs[0].Get(row)
		count := int(end - start)
		if !ok || count == 0 {
			// An empty or null list still produces ONE row, holding a null.
			// Dropping it instead would silently lower the height, and would make
			// two columns disagree about how many rows a pair of empty lists gives.
			rowSel = append(rowSel, int32(row))
			for i := range e.cols {
				if err := e.checkAgrees(in, accs, i, row, 0); err != nil {
					return nil, err
				}
				childSel[i] = append(childSel[i], kernel.NullIndex)
			}
			continue
		}
		for i := range e.cols {
			if err := e.checkAgrees(in, accs, i, row, count); err != nil {
				return nil, err
			}
		}
		for k := start; k < end; k++ {
			rowSel = append(rowSel, int32(row))
		}
		for i := range e.cols {
			s, _, _ := accs[i].Get(row)
			for k := range int32(count) {
				childSel[i] = append(childSel[i], s+k)
			}
		}
	}
	cols := make([]*data.Column, e.schema.Len())
	exploded := make(map[int]int, len(e.cols)) // schema index -> position in e.cols
	for i, ci := range e.cols {
		exploded[ci] = i
	}
	for ci := range e.schema.Len() {
		var err error
		if i, ok := exploded[ci]; ok {
			cols[ci], err = kernel.Take(accs[i].Child(), childSel[i])
			if err == nil {
				cols[ci] = cols[ci].Rename(e.schema.Field(ci).Name)
			}
		} else {
			cols[ci], err = kernel.Take(in.Column(ci), rowSel)
		}
		if err != nil {
			return nil, err
		}
	}
	return data.NewBatchRows(e.schema, cols, len(rowSel)), nil
}

// checkAgrees refuses a row where two exploded columns hold different lengths.
//
// Exploding several columns is a ZIP, not a cross product — doing them one after
// another would give the product, which is why they are one operation. That only
// means anything if the lengths line up, and a mismatch is a question the caller
// has to answer rather than one this can guess at: truncating loses data and
// padding invents it.
func (e *explodeOp) checkAgrees(in *data.Batch, accs []data.ListAccessor,
	i, row, want int) error {

	if i == 0 {
		return nil
	}
	s, end, ok := accs[i].Get(row)
	got := int(end - s)
	if !ok {
		got = 0
	}
	if got != want {
		return uerr.New(uerr.KindValue, "explode",
			"row %d has %d elements in %q but %d in %q",
			row, want, in.Schema().Field(e.cols[0]).Name,
			got, in.Schema().Field(e.cols[i]).Name).
			Hint("columns exploded together are zipped, so their lists must be " +
				"the same length in every row")
	}
	return nil
}

// planExplode lowers plan.Explode.
//
// The node's Schema has already resolved the names and refused a non-List column,
// so this only has to find the indices — and the schema it computed is the one the
// operator publishes, so the two cannot drift.
func planExplode(ctx context.Context, e *plan.Explode, opts Options) (Operator, error) {
	child, err := planPipelined(ctx, e.Input, opts)
	if err != nil {
		return nil, err
	}
	out, err := e.Schema()
	if err != nil {
		return nil, err
	}
	in, err := e.Input.Schema()
	if err != nil {
		return nil, err
	}

	cols := make([]int, len(e.Columns))
	for i, name := range e.Columns {
		cols[i] = in.IndexOf(name)
		if cols[i] < 0 {
			return nil, uerr.Internalf("physical: explode column %q vanished after "+
				"the plan resolved it", name)
		}
	}
	return &stage{child: child, op: &explodeOp{schema: out, cols: cols}}, nil
}
