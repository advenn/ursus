// Package arrowx owns ursus's allocator and the few Arrow facts shared across packages.
//
// It used to say it was the only package importing arrow-go. That was never true once
// the column layer existed — bitmap and data hold *memory.Buffer, and arrowout,
// arrowin, arrowsrc and the root package speak arrow types at the boundary. The
// dependency is confined to storage and to the interop boundary, not to one package.
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
// The one place refcounting is real is foreign memory — the C Data Interface, where
// Release calls back into the producer, or an IPC reader that reuses a record's body
// on its next Next. ursus never holds any: import COPIES into this allocator
// (internal/arrowin, step 60). An Adopt that retained a record and attached a
// runtime.AddCleanup was designed and never built, and would have been unsafe:
// Batch.Slice and every rename build objects that never reference the batch the
// cleanup is attached to.
package arrowx

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

// FieldTypeKey is the Arrow field-metadata key under which ursus records a type
// Arrow cannot spell on its own, and FieldTypeInt128 is its one value today.
//
// Arrow has no 128-bit integer. Export writes an Int128 as Decimal128(38, 0), which
// is also exactly what a Decimal(38, 0) exports as, so without a mark an import
// cannot tell them apart and an integer Sum read back becomes a Decimal. Field
// metadata survives both IPC and the C Data Interface, at every nesting level.
//
// It lives here because export and import are both level 25 and neither may import
// the other; a key spelled twice is a key that drifts.
const (
	FieldTypeKey    = "ursus:type"
	FieldTypeInt128 = "Int128"
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

// IsAligned reports whether b starts on an Alignment boundary. It is available for
// a kernel to choose an aligned fast path, and is never required for correctness.
//
// NO KERNEL CALLS IT. The only caller in the module is this package's own test. The
// present tense here used to claim otherwise, and data/column.go repeated the claim
// — two comments asserting a property the code did not have, which is the single
// largest category of defect this project has recorded.
func IsAligned(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	return uintptr(unsafe.Pointer(&b[0]))&(Alignment-1) == 0
}
