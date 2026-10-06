package engine

import (
	"runtime"
	"testing"
	"time"
)

// TestHeapSamplerSeesALiveAllocation: a buffer held across the sampler's ticks is in
// the peak it reports. The peak is the number REPORT.md prints as a Go engine's live
// heap, so a sampler reading the wrong metric would print a plausible, wrong column.
func TestHeapSamplerSeesALiveAllocation(t *testing.T) {
	const size = 64 << 20
	runtime.GC()
	h := startHeapSampler()
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i)
	}
	time.Sleep(50 * time.Millisecond) // ten ticks
	peak, _, _, _ := h.Stop()
	runtime.KeepAlive(buf)
	if peak < size {
		t.Errorf("peak live heap %d bytes, want at least the %d held", peak, size)
	}
}
