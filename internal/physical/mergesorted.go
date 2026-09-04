package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// mergeSortedOp interleaves two frames already sorted on one column.
//
// # It buffers both sides, and that is the honest v1
//
// A true streaming merge would hold one batch per side and pull whichever is behind —
// which is what kernel/merge.go's Merger does for the external sort's runs. Doing it
// here means threading a second pull source through the breaker protocol, which has
// no two-input streaming shape: joinBreaker drains one side and streams the other.
//
// So this materialises both, merges the row indices, and gathers once. Memory is
// O(both inputs), which is the same as a Union under Collect and is stated rather
// than implied. The streaming version is a follow-up, not a correction.
type mergeSortedOp struct {
	schema *dtype.Schema
	left   Operator
	right  Operator
	key    string
	batch  int
	mem    *execopt.Account

	out  *data.Batch
	pos  int
	done bool
}

func (m *mergeSortedOp) Schema() *dtype.Schema { return m.schema }

// Close closes both children unconditionally — the flat rule concatOp and
// joinBreaker use, because a conditional close is a leak waiting for an early break.
func (m *mergeSortedOp) Close() error {
	m.mem.Release()
	return errors.Join(m.left.Close(), m.right.Close())
}

func (m *mergeSortedOp) Next(ctx context.Context) (*data.Batch, error) {
	if m.out == nil && !m.done {
		if err := m.build(ctx); err != nil {
			return nil, err
		}
	}
	if m.out == nil || m.pos >= m.out.Rows() {
		return nil, io.EOF
	}
	end := min(m.pos+m.batch, m.out.Rows())
	b := m.out.Slice(m.pos, end-m.pos)
	m.pos = end
	return b, nil
}

func (m *mergeSortedOp) build(ctx context.Context) error {
	m.done = true
	left, lk, err := m.drain(ctx, m.left, "left")
	if err != nil {
		return err
	}
	right, rk, err := m.drain(ctx, m.right, "right")
	if err != nil {
		return err
	}

	// One selection vector per side, built by walking both in order. rowRef's src
	// field is exactly the "which input" tag gatherRows already understands.
	pick := make([]rowRef, 0, len(lk)+len(rk))
	i, j := 0, 0
	for i < len(lk) || j < len(rk) {
		// Ties take the LEFT row first, which is what makes the merge stable and the
		// operator's order a function of the inputs rather than of their sizes.
		if j >= len(rk) || (i < len(lk) && !keyAfter(lk[i], rk[j])) {
			pick = append(pick, rowRef{src: 0, row: int32(i)})
			i++
			continue
		}
		pick = append(pick, rowRef{src: 1, row: int32(j)})
		j++
	}

	out, err := gatherRows(m.schema, []*data.Batch{left, right}, pick)
	if err != nil {
		return err
	}
	m.out = out
	m.mem.Retain(out)
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
	return a.v > b.v
}

type mergeKey struct {
	v    int64
	null bool
}

// drain materialises one side and reads its key column, verifying sortedness.
func (m *mergeSortedOp) drain(ctx context.Context, op Operator, side string) (*data.Batch, []mergeKey, error) {
	var parts []*data.Batch
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		parts = append(parts, b)
		m.mem.Retain(b)
	}
	all, err := kernel.Concat(m.schema, parts)
	if err != nil {
		return nil, nil, err
	}
	col, ok := all.ByName(m.key)
	if !ok {
		return nil, nil, uerr.Internalf("physical: merge_sorted lost column %q", m.key)
	}
	ticks, err := temporalTicks(col)
	if err != nil {
		return nil, nil, uerr.New(uerr.KindUnsupported, "merge_sorted",
			"cannot merge on a %s key", col.DType()).
			Hint("merge_sorted compares with <, so the key must be numeric or temporal")
	}
	keys := make([]mergeKey, len(ticks))
	for i := range ticks {
		keys[i] = mergeKey{v: ticks[i], null: !col.IsValid(i)}
	}
	for i := 1; i < len(keys); i++ {
		if keyAfter(keys[i-1], keys[i]) {
			return nil, nil, uerr.New(uerr.KindValue, "merge_sorted",
				"the %s side is not sorted on %q: row %d goes backwards",
				side, m.key, i).
				Hint("merge_sorted interleaves two ordered runs, so both must be ordered").
				Hint("sort first, e.g. .Sort(ursus.Asc(ursus.Col(%q)))", m.key)
		}
	}
	if err := m.mem.Check(); err != nil {
		return nil, nil, err
	}
	return all, keys, nil
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
		schema: schema, left: left, right: right, key: m.Key,
		batch: opts.batchSize(), mem: opts.Budget.Account("merge_sorted"),
	}, nil
}
