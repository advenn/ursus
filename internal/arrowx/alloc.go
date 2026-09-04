// Package arrowx is the only package in ursus that imports arrow-go.
//
// Everything above it speaks ursus types (dtype.DataType, data.Column). Keeping
// the dependency in one package means the Arrow version can move, or the storage
// layer can be replaced wholesale, without touching the engine.
//
// # Ownership
//
// ursus hides Arrow's Retain/Release refcounting completely. No public ursus type
// has a Release method. This is safe, and it is safe for a specific, verified
// reason rather than by hope:
//
//   - memory.GoAllocator.Allocate is `make([]byte, size+64)` — plain Go heap.
//   - memory.GoAllocator.Free is a no-op.
//   - There is no runtime.SetFinalizer or runtime.AddCleanup anywhere in
//     arrow, arrow/array, or arrow/memory.
//
// So a *memory.Buffer that is never Released keeps its []byte alive exactly as
// long as the Buffer itself is reachable, and the GC reclaims both together.
// Refcounting is advisory. TestRiskGateGCReclaims asserts this empirically; if it
// ever fails, the ownership model — and this comment — must change.
//
// The one place refcounting is real is memory imported across the C Data
// Interface, where Release calls back into the producer. ursus does not import
// foreign Arrow memory in v0.1; when it does, Adopt must Retain and attach a
// runtime.AddCleanup.
package arrowx

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

// Alignment is the byte alignment every ursus-allocated buffer is guaranteed to
// have. It matches Arrow's recommended alignment and is the width that lets a
// SIMD loop start without a scalar prologue.
//
// Note this is a guarantee about buffers ursus allocates, NOT about every buffer
// ursus may observe: memory.NewBufferBytes over a caller's slice, memory.SliceBuffer,
// and buffers mmap'd from an IPC file written by another implementation may be
// aligned to only 8 bytes (which is all the IPC spec mandates). Kernels must treat
// 64-byte alignment as an optimization hint, never as a precondition.
const Alignment = 64

// allocator is the process-wide allocator for ursus-owned buffers.
//
// It is deliberately memory.NewGoAllocator() and NOT memory.DefaultAllocator.
// DefaultAllocator is selected by build tag: with `-tags mallocator` it becomes a
// cgo Mallocator whose Free calls C.free. Since ursus never calls Release, that
// combination would turn every buffer into a genuine leak. Pinning the Go
// allocator makes the ownership model above independent of build tags.
var allocator memory.Allocator = memory.NewGoAllocator()

// Allocator returns the allocator for ursus-owned buffers. Every buffer it
// produces is Alignment-aligned.
func Allocator() memory.Allocator { return allocator }

// NewBuffer returns a resizable buffer of exactly n bytes, zeroed, 64-byte aligned.
func NewBuffer(n int) *memory.Buffer {
	b := memory.NewResizableBuffer(allocator)
	b.Resize(n)
	return b
}

// IsAligned reports whether b starts on an Alignment boundary. Kernels use it to
// choose an aligned fast path; it is never required for correctness.
func IsAligned(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	return uintptr(unsafe.Pointer(&b[0]))&(Alignment-1) == 0
}
