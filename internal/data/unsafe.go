package data

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

// unsafeString reinterprets a byte slice as a string without copying.
//
// Safe here because every character buffer a StringAccessor reads from is
// immutable for the life of the Column: buffers are written once during
// construction and Columns are shared read-only from then on.
func unsafeString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// unsafeSizeof returns the size of a value's type in bytes.
func unsafeSizeof[T any](v T) uintptr { return unsafe.Sizeof(v) }

// unsafeData reinterprets a byte buffer as a typed slice, without copying.
//
// This replaces arrow.GetData, whose constraint enumerates arrow's own fixed-width
// types and therefore excludes ursus's Int128. The mechanics are identical.
//
// Alignment: a fixed-width payload is an arrowx buffer (64-byte aligned) or a
// whole-element slice of one (Column.Slice), so it is aligned for its own T — which is
// all this needs. It used to say every buffer is 64-byte aligned, which a slice is
// not; nothing depends on 64. Arrow import copies into arrowx buffers rather than
// wrapping foreign memory, so no payload arrives with a producer's alignment.
func unsafeData[T Fixed](b []byte) []T {
	if len(b) == 0 {
		return nil
	}
	var z T
	sz := int(unsafe.Sizeof(z))
	return unsafe.Slice((*T)(unsafe.Pointer(&b[0])), len(b)/sz)
}

// ownedBuffer wraps s's elements as a buffer without copying. The buffer's capacity
// is s's, so it reports the allocation it holds, as Buffers measures by capacity.
//
// The memory is Go heap, kept alive by the buffer's slice like any arrowx buffer's;
// it is 8-byte aligned rather than 64, which arrowx.Alignment allows a buffer ursus
// observes and every kernel treats as a hint.
func ownedBuffer[T Fixed](s []T) *memory.Buffer {
	if cap(s) == 0 {
		return memory.NewBufferBytes(nil)
	}
	var z T
	sz := int(unsafe.Sizeof(z))
	all := unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(s))), cap(s)*sz)
	// The whole allocation, so Cap reports it; then the length in use. A resize
	// within the capacity only sets the length: a wrapped buffer has no allocator,
	// and nothing here reaches one.
	b := memory.NewBufferBytes(all)
	b.ResizeNoShrink(len(s) * sz)
	return b
}
