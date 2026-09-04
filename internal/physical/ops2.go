package physical

import (
	"context"
	"strconv"

	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/execopt"
	"ursus/internal/expr"
	"ursus/internal/kernel"
	"ursus/internal/plan"
	"ursus/internal/uerr"
)

// --- WithColumns -------------------------------------------------------------

// withColumnsOp adds or replaces columns.
//
// It is a BatchOp, not a Sink: it is stateless and one-batch-in-one-batch-out, and
// the standing rule is that every operator which CAN be a BatchOp MUST be, so the
// v0.2 morsel scheduler can run it unchanged.
//
// Each step carries the schema that holds AFTER it, mirroring the running-schema
// walk that resolveWithColumns and WithColumns.Schema both perform. Those three
// walks must agree; disagreement is what made a self-referencing WithColumns
// resolve successfully and then fail its own Schema().
type withColumnsOp struct {
	schema *dtype.Schema
	steps  []wcStep
}

type wcStep struct {
	expr  expr.Node
	name  string
	pos   int // index to replace, or -1 to append
	after *dtype.Schema
}

func (w *withColumnsOp) Schema() *dtype.Schema { return w.schema }
func (w *withColumnsOp) Close() error          { return nil }

func (w *withColumnsOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	cur := in
	for _, st := range w.steps {
		c, err := evalColumn(ctx, st.expr, cur)
		if err != nil {
			return nil, err
		}
		c = c.Rename(st.name)

		cols := make([]*data.Column, len(cur.Columns()), st.after.Len())
		copy(cols, cur.Columns())
		if st.pos >= 0 {
			cols[st.pos] = c // replace in place, preserving column position
		} else {
			cols = append(cols, c)
		}
		if cur, err = data.NewBatch(st.after, cols); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

func planWithColumns(ctx context.Context, w *plan.WithColumns, opts Options) (Operator, error) {
	child, err := Plan(ctx, w.Input, opts)
	if err != nil {
		return nil, err
	}
	in, err := w.Input.Schema()
	if err != nil {
		return nil, err
	}

	// The same walk the plan layer uses, so the operator's replace-in-place
	// positions cannot drift from the schema the planner promised.
	steps := make([]wcStep, 0, len(w.Exprs))
	out, err := plan.WalkWithColumns(in, w.Exprs,
		func(e expr.Node, f dtype.Field, pos int, after *dtype.Schema) error {
			steps = append(steps, wcStep{expr: e, name: f.Name, pos: pos, after: after})
			return nil
		})
	if err != nil {
		return nil, err
	}

	return &stage{child: child, op: &withColumnsOp{schema: out, steps: steps}}, nil
}

// --- Distinct ----------------------------------------------------------------

// distinctOp removes duplicate rows.
//
// # Not a pipeline breaker, and that is the interesting part
//
// Distinct can decide row by row whether it has seen a key before, so it emits its
// first output batch after reading its FIRST input batch. That is what makes
// `Distinct().Head(10)` stop after ten distinct rows rather than scanning
// everything, and its memory is O(distinct keys) either way.
//
// It is not a BatchOp either, because the seen-set carries across batches. It
// joins limitOp as a stateful streaming Operator — the third and last shape here.
//
// The row kept is the FIRST for each key, complete with the columns Subset does not
// compare, so `Distinct(Subset: "id")` over {id, ts} keeps the earliest ts per id
// rather than an arbitrary one.
type distinctOp struct {
	child  Operator
	schema *dtype.Schema
	subset []int
	seen   map[string]struct{}

	// mem accounts the seen-set, which grows with the number of DISTINCT ROWS and
	// is held across batches — the same shape as hashAggSink.ids, which has been
	// accounted since step 10 while this was not. Without it Scan(huge).Unique()
	// under a memory limit neither spilled nor failed; it grew until the operating
	// system stopped it, which is the outcome the whole budget exists to replace.
	mem      *execopt.Account
	seenSize int64
}

func (d *distinctOp) Schema() *dtype.Schema { return d.schema }

func (d *distinctOp) Close() error {
	d.mem.Release()
	return d.child.Close()
}

func (d *distinctOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		in, err := d.child.Next(ctx)
		if err != nil {
			return nil, err // includes io.EOF
		}

		cols := make([]*data.Column, len(d.subset))
		for i, ci := range d.subset {
			cols[i] = in.Column(ci)
		}
		// The same encoder the hash aggregation uses, for the same reasons: the tag
		// byte makes a null key structurally incapable of colliding with a real
		// value, and OrderKey canonicalisation makes NaN equal NaN. A distinct built
		// on IEEE equality would emit one row per NaN.
		enc, err := kernel.NewGroupKeyEncoder("unique", cols)
		if err != nil {
			return nil, err
		}

		keep := bitmap.NewBuilder(in.Rows())
		any := false
		for r := range in.Rows() {
			k := enc.Encode(r)
			if _, dup := d.seen[string(k)]; dup {
				keep.Append(false)
				continue
			}
			d.seen[string(k)] = struct{}{}
			// The key BYTES plus a per-entry constant for the string header and the
			// bucket slot, accumulated on insert rather than walked per batch — the
			// same trade nuniqueAcc's NBytes makes, done exactly here because this is
			// the one place a new key is known to be new.
			d.seenSize += int64(len(k)) + 48
			keep.Append(true)
			any = true
		}
		d.mem.RetainBytes(d.seenSize - d.memHeld())
		if err := d.mem.Check(); err != nil {
			return nil, err
		}
		if !any {
			continue // a batch of pure duplicates is not end-of-stream
		}

		mask := data.NewBool("__distinct", keep.Finish(), bitmap.AllSet(in.Rows()))
		out, err := kernel.FilterBatch(in, mask)
		if err != nil {
			return nil, err
		}
		if out.Rows() == 0 {
			continue
		}
		return out, nil
	}
}

// memHeld is what the account currently believes the seen-set costs.
//
// Account.Used includes retained buffers as well, but distinctOp retains none —
// it holds only plain Go state — so Used is exactly the running total.
func (d *distinctOp) memHeld() int64 { return d.mem.Used() }

func planDistinct(ctx context.Context, d *plan.Distinct, opts Options) (Operator, error) {
	child, err := planPipelined(ctx, d.Input, opts)
	if err != nil {
		return nil, err
	}
	s, err := d.Schema()
	if err != nil {
		return nil, err
	}

	subset := make([]int, 0, s.Len())
	if d.Subset == nil {
		for i := range s.Len() {
			subset = append(subset, i)
		}
	} else {
		for _, name := range d.Subset {
			i := s.IndexOf(name)
			if i < 0 {
				return nil, uerr.UnknownColumn("distinct", name, s.Names())
			}
			subset = append(subset, i)
		}
	}

	return &distinctOp{
		child: child, schema: s, subset: subset,
		seen: make(map[string]struct{}),
		mem:  opts.Budget.Account("unique"),
	}, nil
}

// --- shared helpers -----------------------------------------------------------

// emptyColumns builds one zero-row column per field.
//
// Through kernel.NullColumn rather than data.NewNull, because these reach the
// USER: every operator that can produce no rows returns them, and a payload-free
// column makes `df.Column[int64]("v")` fail with "has no fixed-width payload" on
// an empty result. A zero-row column costs nothing either way; being readable is
// the difference between an empty series and an error.
//
// NullColumn only fails for a type it cannot build at all, and for those the
// payload-free form is the best available answer anyway.
func emptyColumns(s *dtype.Schema) []*data.Column {
	cols := make([]*data.Column, s.Len())
	for i, f := range s.All() {
		c, err := kernel.NullColumn(f.Name, f.Type, 0)
		if err != nil {
			c = data.NewNull(f.Name, f.Type, 0)
		}
		cols[i] = c
	}
	return cols
}

func itoa(i int) string { return strconv.Itoa(i) }

func uerrInternal(what string, got any) error {
	return uerr.Internalf("physical: cannot merge %T into %s", got, what)
}
