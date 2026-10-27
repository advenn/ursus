package kernel

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// countdown is a context cancelled at its left'th look: Err answers nil until then,
// so a test says how far a sort gets before it is cancelled.
type countdown struct {
	context.Context
	left atomic.Int64
}

func (c *countdown) Err() error {
	if c.left.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

// TestASortStopsWhenCancelled (step 394): a sort over workers looks at its context
// between its passes, a radix sort's columns and bytes, a comparator sort's rounds of
// merges, and answers a cancellation as its error, where it sorted to the end.
func TestASortStopsWhenCancelled(t *testing.T) {
	const n = 1 << 18
	rng := rand.New(rand.NewPCG(394, 1))
	f, k, q := make([]float64, n), make([]int64, n), make([]i128.Int128, n)
	for i := range n {
		f[i], k[i], q[i] = rng.NormFloat64(), rng.Int64(), i128.FromInt64(rng.Int64())
	}
	radix := []*data.Column{data.NewFixed("f", dtype.Float64, f, bitmap.View{}), data.NewFixed("k", dtype.Int64, k, bitmap.View{})}
	cmp := []*data.Column{data.NewFixed("q", dtype.Int128, q, bitmap.View{}), data.NewFixed("k", dtype.Int64, k, bitmap.View{})}
	specs := []SortSpec{{}, {}}
	for name, cols := range map[string][]*data.Column{"radix": radix, "comparator": cmp} {
		// Uncancelled, it sorts; cancelled at its second look, past the entry's, it
		// stops.
		if _, err := ArgSortColumnsN(t.Context(), cols, specs, n, 4); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ctx := &countdown{Context: t.Context()}
		ctx.left.Store(1)
		if _, err := ArgSortColumnsN(ctx, cols, specs, n, 4); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: %v, want the cancellation", name, err)
		}
	}
}

// TestARadixSortStopsAtItsNextPass: cancelled at its first byte's pass, a radix sort
// of four million floats comes back in well under half the time it takes to sort
// them, where without a look a pass it ran every pass first, some tens of ms each.
// The two times are the same process's, the least of three each.
func TestARadixSortStopsAtItsNextPass(t *testing.T) {
	const n = 1 << 22
	rng := rand.New(rand.NewPCG(394, 2))
	f := make([]float64, n)
	for i := range f {
		f[i] = rng.NormFloat64()
	}
	cols := []*data.Column{data.NewFixed("f", dtype.Float64, f, bitmap.View{})}
	least := func(left int64) time.Duration {
		best := time.Duration(1 << 62)
		for range 3 {
			ctx := &countdown{Context: t.Context()}
			ctx.left.Store(left)
			t0 := time.Now()
			_, err := ArgSortColumnsN(ctx, cols, []SortSpec{{}}, n, 4)
			if el := time.Since(t0); el < best {
				best = el
			}
			if left < 1<<40 && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled at look %d: %v", left+1, err)
			}
		}
		return best
	}
	whole := least(1 << 50)
	// Looks: the entry's, the column's, then the first byte pass's, cancelled.
	cut := least(2)
	if cut > whole/2 {
		t.Fatalf("cancelled at its first pass it took %v, sorting whole %v", cut, whole)
	}
}
