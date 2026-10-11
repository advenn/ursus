package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// mergeSortedOp interleaves two frames already sorted on one column.
//
// # It streams (step 182)
//
// It holds one batch of each side, with its keys and a cursor, and fills an output
// batch from whichever side is behind, until the output is full or one side's batch
// runs out. Then it pulls that side's next batch. Memory is two input batches and one
// output batch however long the inputs, where it used to materialise both sides and
// merge their row indices once, O(both inputs).
//
// Both sides are ordinary pull operators, so one goroutine pulls them in turn; the
// breaker protocol, which drains one side and streams the other, is not needed.
//
// Sortedness is checked as rows arrive, across a side's batches too, so an unsorted
// input is refused when the merge reaches its first backward row, after the rows
// before it have been emitted. Collect fails either way.
type mergeSortedOp struct {
	schema *dtype.Schema
	left   Operator
	right  Operator
	key    string
	batch  int

	sides [2]mergeSide
}

// mergeSide is one input's current batch, its keys, and how far into it the merge
// has read.
type mergeSide struct {
	name string
	b    *data.Batch
	keys []mergeKey
	pos  int
	last mergeKey // the previous batch's last key, for sortedness across batches
	seen bool     // whether last is set
	done bool
}

func (m *mergeSortedOp) Schema() *dtype.Schema { return m.schema }

// Close closes both children unconditionally — the flat rule concatOp and
// joinBreaker use, because a conditional close is a leak waiting for an early break.
func (m *mergeSortedOp) Close() error {
	return errors.Join(m.left.Close(), m.right.Close())
}

func (m *mergeSortedOp) Next(ctx context.Context) (*data.Batch, error) {
	ops := [2]Operator{m.left, m.right}
	for i := range m.sides {
		if err := m.fill(ctx, i, ops[i]); err != nil {
			return nil, err
		}
	}
	l, r := &m.sides[0], &m.sides[1]
	if l.done && r.done {
		return nil, io.EOF
	}
	// One output batch, from the two current batches only: it stops where either
	// runs out, so the gather reads at most one batch a side.
	pick := make([]rowRef, 0, m.batch)
	for len(pick) < m.batch && (l.done || l.pos < len(l.keys)) && (r.done || r.pos < len(r.keys)) {
		// Ties take the LEFT row first, which is what makes the merge stable and the
		// operator's order a function of the inputs rather than of their sizes.
		if r.done || (!l.done && !keyAfter(l.keys[l.pos], r.keys[r.pos])) {
			pick = append(pick, rowRef{src: 0, row: int32(l.pos)})
			l.pos++
			continue
		}
		pick = append(pick, rowRef{src: 1, row: int32(r.pos)})
		r.pos++
	}
	return gatherRows(m.schema, []*data.Batch{l.b, r.b}, pick)
}

// fill gives side i a batch with rows left to read, pulling past empty batches, or
// marks it done.
func (m *mergeSortedOp) fill(ctx context.Context, i int, op Operator) error {
	sd := &m.sides[i]
	for !sd.done && sd.pos >= len(sd.keys) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(sd.keys) > 0 {
			sd.last, sd.seen = sd.keys[len(sd.keys)-1], true
		}
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			sd.done, sd.b, sd.keys, sd.pos = true, nil, nil, 0
			return nil
		}
		if err != nil {
			return err
		}
		keys, err := m.keysOf(b, sd)
		if err != nil {
			return err
		}
		sd.b, sd.keys, sd.pos = b, keys, 0
	}
	return nil
}

// keyAfter reports whether a sorts after b. Nulls sort first, matching the sort's own
// default null placement.
func keyAfter(a, b mergeKey) bool {
	if a.null != b.null {
		return !a.null // a non-null sorts after a null
	}
	if a.null {
		return false
	}
	if a.v != b.v {
		return a.v > b.v
	}
	return a.s > b.s
}

// mergeKey is one row's key: an order key (see orderKeys) for a numeric or temporal
// column, or the bytes of a String or Binary one, with v left zero.
type mergeKey struct {
	v    int64
	s    string
	null bool
}

// keysOf reads one batch's key column, refusing a row that goes backwards, within
// the batch or from the side's previous one.
func (m *mergeSortedOp) keysOf(b *data.Batch, sd *mergeSide) ([]mergeKey, error) {
	col, ok := b.ByName(m.key)
	if !ok {
		return nil, uerr.Internalf("physical: merge_sorted lost column %q", m.key)
	}
	keys := make([]mergeKey, col.Len())
	if col.DType().HasStringStorage() {
		acc := col.Strings()
		for i := range keys {
			keys[i] = mergeKey{null: !col.IsValid(i)}
			if !keys[i].null {
				keys[i].s = acc.Get(i)
			}
		}
	} else {
		// MergeSorted.Schema refused every other type while the query was planned.
		ord, err := orderKeys(col)
		if err != nil {
			return nil, err
		}
		for i := range keys {
			keys[i] = mergeKey{v: ord[i], null: !col.IsValid(i)}
		}
	}
	for i := range keys {
		prev, has := sd.last, sd.seen
		if i > 0 {
			prev, has = keys[i-1], true
		}
		if has && keyAfter(prev, keys[i]) {
			return nil, uerr.New(uerr.KindValue, "merge_sorted",
				"the %s side is not sorted on %q: a row goes backwards", sd.name, m.key).
				Hint("merge_sorted interleaves two ordered runs, so both must be ordered").
				Hint("sort first, e.g. .Sort(ursus.Asc(ursus.Col(%q)))", m.key)
		}
	}
	return keys, nil
}

func planMergeSorted(ctx context.Context, m *plan.MergeSorted, opts Options) (Operator, error) {
	left, err := planPipelined(ctx, m.Left, opts)
	if err != nil {
		return nil, err
	}
	right, err := planPipelined(ctx, m.Right, opts)
	if err != nil {
		return nil, err
	}
	schema, err := m.Schema()
	if err != nil {
		return nil, err
	}
	return &mergeSortedOp{
		schema: schema, left: left, right: right, key: m.Key, batch: opts.batchSize(),
		sides: [2]mergeSide{{name: "left"}, {name: "right"}},
	}, nil
}
