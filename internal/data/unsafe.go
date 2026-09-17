package data

import "unsafe"

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
