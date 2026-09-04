package arrowx_test

// The Phase 0 risk gate.
//
// The decision to build ursus's storage layer on arrow-go rests on three
// empirical claims. They were established by reading arrow-go's source; these
// tests assert them against the actual linked version, so that an arrow-go
// upgrade that invalidates one fails loudly here rather than silently corrupting
// the memory model.
//
//  1. Buffers ursus allocates are 64-byte aligned.          TestRiskGateAlignment
//  2. Un-Released buffers are reclaimed by the GC.          TestRiskGateGCReclaims
//  3. Reading a column through arrow.GetValues[T] costs
//     nothing versus a plain Go slice.                      BenchmarkRiskGate*
//
// If (2) fails, the ownership model in alloc.go is wrong and every public API
// would need a Release method. If (3) fails, kernels should not read through
// Arrow at all. Both would be foundational rewrites, which is why they are gated
// before any engine code is written.

import (
	"runtime"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/internal/arrowx"
)

// TestRiskGateAlignment asserts claim (1) across the size classes where Go's
// allocator does NOT naturally give 64-byte alignment. Sizes below 512 bytes are
// the interesting ones: a bare make([]byte, 72) is routinely misaligned, so this
// is really a test that arrowx goes through the Arrow allocator rather than make.
func TestRiskGateAlignment(t *testing.T) {
	sizes := []int{1, 8, 33, 64, 72, 80, 100, 136, 200, 264, 392, 480, 511, 512, 4096, 1 << 20}
	for _, n := range sizes {
		for range 64 { // repeat: misalignment is probabilistic within a size class
			buf := arrowx.NewBuffer(n)
			if !arrowx.IsAligned(buf.Bytes()) {
				t.Fatalf("NewBuffer(%d): buffer not %d-byte aligned", n, arrowx.Alignment)
			}
			if got := len(buf.Bytes()); got != n {
				t.Fatalf("NewBuffer(%d): len = %d", n, got)
			}
		}
	}
}

// TestRiskGateGCReclaims asserts claim (2): allocating and dropping a large number
// of buffers WITHOUT calling Release must not grow the heap. This is the claim the
// entire "no Release() in the public API" decision depends on.
//
// It measures HeapAlloc rather than RSS: RSS is confounded by the allocator's
// scavenging policy, whereas HeapAlloc directly answers "did the GC reclaim it".
func TestRiskGateGCReclaims(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~4 GiB of transient buffers")
	}

	const (
		bufSize = 64 << 10 // 64 KiB
		rounds  = 64_000   // ~4 GiB total, ~500x any plausible steady state
	)

	settle := func() uint64 {
		runtime.GC()
		runtime.GC() // second cycle finalizes anything the first only queued
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}

	baseline := settle()

	for range rounds {
		b := arrowx.NewBuffer(bufSize)
		// Touch it so the write is not optimized away, then drop it on the floor.
		// Deliberately NO b.Release() — that is the whole point of the test.
		b.Bytes()[0] = 1
		_ = b
	}

	after := settle()

	// Allow generous slack for heap fragmentation and test-framework noise, while
	// still being ~3 orders of magnitude below the ~4 GiB that would be retained
	// if buffers leaked.
	const slack = 64 << 20 // 64 MiB
	if after > baseline+slack {
		t.Fatalf("heap grew by %d bytes after %d unreleased %d-byte buffers "+
			"(baseline %d, after %d); arrow-go buffers are NOT being reclaimed by the GC, "+
			"so the ownership model in alloc.go is invalid",
			after-baseline, rounds, bufSize, baseline, after)
	}
	t.Logf("heap delta after %d unreleased buffers (%d MiB allocated): %+d bytes",
		rounds, rounds*bufSize>>20, int64(after)-int64(baseline))
}

// TestRiskGateZeroCopyIsWritable asserts the property kernels depend on: the slice
// returned by arrow.GetData[T] over a buffer aliases the buffer's memory, so a
// kernel can write its output directly into it. This is the documented bypass
// around the sealed array.Builder interface.
func TestRiskGateZeroCopyIsWritable(t *testing.T) {
	const n = 1024
	buf := arrowx.NewBuffer(n * 8)
	vals := arrow.GetData[float64](buf.Bytes())
	if len(vals) != n {
		t.Fatalf("GetData len = %d, want %d", len(vals), n)
	}

	for i := range vals {
		vals[i] = float64(i) * 1.5
	}

	// Build an array over the same buffer and read it back through the Arrow API.
	d := array.NewData(arrow.PrimitiveTypes.Float64, n, []*memory.Buffer{nil, buf}, nil, 0, 0)
	arr := array.MakeFromData(d).(*array.Float64)

	for i := range n {
		if got, want := arr.Value(i), float64(i)*1.5; got != want {
			t.Fatalf("row %d: array sees %v, kernel wrote %v — GetData is not aliasing", i, got, want)
		}
	}
	// And the accessor is the same memory, not a copy.
	if &arr.Float64Values()[0] != &vals[0] {
		t.Fatal("Float64Values() returned a copy; kernels cannot write in place")
	}
}

// --- claim (3): zero-copy read costs nothing ---------------------------------

const benchN = 1 << 16

//go:noinline
func sumSlice(xs []float64) float64 {
	var s float64
	for _, v := range xs {
		s += v
	}
	return s
}

var benchSink float64

// BenchmarkRiskGateNativeSlice is the control: a plain Go slice.
func BenchmarkRiskGateNativeSlice(b *testing.B) {
	xs := make([]float64, benchN)
	for i := range xs {
		xs[i] = float64(i)
	}
	b.SetBytes(benchN * 8)
	b.ResetTimer()
	for b.Loop() {
		benchSink = sumSlice(xs)
	}
}

// BenchmarkRiskGateArrowValues is the same loop reading through an Arrow array's
// typed accessor. Must be within noise of the control; if it is not, kernels
// should copy out of Arrow rather than read through it.
func BenchmarkRiskGateArrowValues(b *testing.B) {
	buf := arrowx.NewBuffer(benchN * 8)
	vals := arrow.GetData[float64](buf.Bytes())
	for i := range vals {
		vals[i] = float64(i)
	}
	d := array.NewData(arrow.PrimitiveTypes.Float64, benchN, []*memory.Buffer{nil, buf}, nil, 0, 0)
	arr := array.MakeFromData(d).(*array.Float64)

	b.SetBytes(benchN * 8)
	b.ResetTimer()
	for b.Loop() {
		benchSink = sumSlice(arr.Float64Values())
	}
}

// BenchmarkRiskGateArrowGetValues reads via the generic accessor, which is the
// form ursus kernels will actually use (it is dtype-parametric).
func BenchmarkRiskGateArrowGetValues(b *testing.B) {
	buf := arrowx.NewBuffer(benchN * 8)
	vals := arrow.GetData[float64](buf.Bytes())
	for i := range vals {
		vals[i] = float64(i)
	}
	d := array.NewData(arrow.PrimitiveTypes.Float64, benchN, []*memory.Buffer{nil, buf}, nil, 0, 0)

	b.SetBytes(benchN * 8)
	b.ResetTimer()
	for b.Loop() {
		benchSink = sumSlice(arrow.GetValues[float64](d, 1))
	}
}
