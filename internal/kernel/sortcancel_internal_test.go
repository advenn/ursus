package kernel

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync/atomic"
	"testing"

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
// does no more passes. The countdown counts the sort's looks at its context: one a
// pass, and at the cancellation two more, halted's and the error's it returns; a sort
// that ran every pass first and answered the cancellation at its end would look once
// a pass to the end, as the whole sort does. Until step 495 this compared the two
// sorts' wall-clock times, under half, which a loaded CI runner failed at 52%.
func TestARadixSortStopsAtItsNextPass(t *testing.T) {
	const n = 1 << 16
	rng := rand.New(rand.NewPCG(394, 2))
	f := make([]float64, n)
	for i := range f {
		f[i] = rng.NormFloat64()
	}
	cols := []*data.Column{data.NewFixed("f", dtype.Float64, f, bitmap.View{})}
	looks := func(left int64) (int64, error) {
		ctx := &countdown{Context: t.Context()}
		ctx.left.Store(left)
		_, err := ArgSortColumnsN(ctx, cols, []SortSpec{{}}, n, 4)
		return left - ctx.left.Load(), err
	}
	whole, err := looks(1 << 50)
	if err != nil {
		t.Fatal(err)
	}
	// Looks: the entry's, the column's, then the first byte pass's, cancelled.
	cut, err := looks(2)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled at its third look: %v", err)
	}
	t.Logf("the whole sort looked %d times, the cancelled one %d", whole, cut)
	// Eight byte passes, so the whole sort looks at least ten times; the cancelled one
	// its three, the return's two more, and no pass's.
	if whole < 10 {
		t.Fatalf("the whole sort looked %d times, fewer than its eight byte passes and two entries", whole)
	}
	if cut > 5 {
		t.Fatalf("cancelled at its third look the sort looked %d times, so it went on past the cancellation", cut)
	}
}
