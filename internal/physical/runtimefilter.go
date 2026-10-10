package physical

import (
	"context"
	"sync/atomic"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
)

// runtimeFilterOp is plan.RuntimeFilter (step 167): it keeps every row until the
// join its slot belongs to publishes its keys, and then the rows whose key is among
// them. A join publishes integer key tables (kernel.IntKeyParts) from a partitioned
// build alone; any other value in the slot keeps every row, as does a key column of
// another type, so a row is dropped only when it cannot join.
type runtimeFilterOp struct {
	schema *dtype.Schema
	key    string
	slot   *plan.RuntimeSlot

	// A filter that keeps most of what it sees costs a lookup a row and a copy of
	// every batch, and saves little: past retireAfter rows, keeping more than three
	// quarters, it keeps every row from then on. seen and kept are over every worker
	// that applies it.
	seen, kept atomic.Int64
	retired    atomic.Bool
}

// retireAfter is how many rows a runtime filter sees before it judges itself.
const retireAfter = 1 << 16

// runtimeFiltered counts the rows runtime filters dropped, and runtimeRetired the
// filters that kept too much to be worth their lookups, for a test to see each.
var runtimeFiltered, runtimeRetired atomic.Int64

func planRuntimeFilter(ctx context.Context, r *plan.RuntimeFilter, opts Options) (Operator, error) {
	child, err := Plan(ctx, r.Input, opts)
	if err != nil {
		return nil, err
	}
	s, err := r.Schema()
	if err != nil {
		return nil, err
	}
	return &stage{child: child, op: &runtimeFilterOp{schema: s, key: r.Key, slot: r.Slot}}, nil
}

func (r *runtimeFilterOp) Schema() *dtype.Schema { return r.schema }
func (r *runtimeFilterOp) Close() error          { return nil }

func (r *runtimeFilterOp) Apply(_ context.Context, in *data.Batch) (*data.Batch, error) {
	keys, _ := r.slot.Published().(*kernel.IntKeyParts)
	if keys == nil || in.Rows() == 0 || r.retired.Load() {
		return in, nil
	}
	c, ok := in.ByName(r.key)
	if !ok || !kernel.IsIntKey(c.DType()) {
		return in, nil
	}
	var scratch []int64
	vals := kernel.IntKeys(c, &scratch)
	valid := c.Validity()
	n := in.Rows()
	var (
		ids  [kernel.ManyChunk]int32
		keep []int32
	)
	for lo := 0; lo < n; lo += kernel.ManyChunk {
		hi := min(lo+kernel.ManyChunk, n)
		keys.GetMany(vals[lo:hi], ids[:hi-lo])
		for j := range hi - lo {
			// A null key matches nothing: the joins that publish are not NullsEqual.
			if ids[j] >= 0 && (valid.IsAllSet() || valid.Get(lo+j)) {
				keep = append(keep, int32(lo+j))
			}
		}
	}
	seen, kept := r.seen.Add(int64(n)), r.kept.Add(int64(len(keep)))
	if seen >= retireAfter && kept*4 > seen*3 {
		r.retired.Store(true)
		runtimeRetired.Add(1)
	}
	if len(keep) == n {
		return in, nil
	}
	runtimeFiltered.Add(int64(n - len(keep)))
	return takeBatch(in.Schema(), in, keep)
}
