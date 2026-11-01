package ursus_test

// Step 424: the memory ledger counts a sliced buffer as the allocation it holds,
// where a slice from its start, seen first, hid its parent, and one from within it
// counted it twice.

import (
	"testing"

	"github.com/advenn/ursus"
)

// TestASlicedBufferIsItsAllocation: a sort of a frame's head of ten rows and the
// frame, 8 MB of Int64, retained a ledger peak of nothing and, under a 4 MB limit,
// held the 8 MB and spilled nothing; it retains the 8 MB, and spills. The frame from
// its second row beside the frame counted 16 MB, the one allocation twice; it counts
// 8. The frame alone is 8 MB, as it was.
func TestASlicedBufferIsItsAllocation(t *testing.T) {
	const n = 1 << 20
	x := make([]int64, n)
	for i := range x {
		x[i] = int64((i * 7919) % n)
	}
	df := ursus.Frame(ursus.Values("x", x))
	sort := func(lf *ursus.LazyFrame, opts ...ursus.CollectOption) ursus.MemoryStats {
		var st ursus.MemoryStats
		opts = append(opts, ursus.WithMemoryStats(&st), ursus.WithBatchSize(n), ursus.WithThreads(1))
		if _, err := lf.Sort(ursus.Asc(ursus.Col("x"))).Collect(t.Context(), opts...); err != nil {
			t.Fatal(err)
		}
		return st
	}
	const mb = 1 << 20
	for _, c := range []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"the frame", df},
		{"its head, then the frame", ursus.Concat([]*ursus.LazyFrame{df.Head(10), df})},
		{"the frame, then its head", ursus.Concat([]*ursus.LazyFrame{df, df.Head(10)})},
		{"the frame from its second row, then the frame", ursus.Concat([]*ursus.LazyFrame{df.Slice(1, n-1), df})},
	} {
		if p := sort(c.lf).Peak; p < 8*mb || p > 9*mb {
			t.Errorf("%s: peak %.1f MB, want the one 8 MB allocation", c.name, float64(p)/mb)
		}
	}
	st := sort(ursus.Concat([]*ursus.LazyFrame{df.Head(10), df}),
		ursus.WithMemoryLimit(4*mb), ursus.WithSpillDir(t.TempDir()))
	if st.Spills == 0 {
		t.Errorf("its head, then the frame, under 4 MB: peak %.1f MB and no spill", float64(st.Peak)/mb)
	}
}
