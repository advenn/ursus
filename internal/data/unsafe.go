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
// types and therefore excludes ursus's Int128. The mechanics are identical; every
// buffer comes from arrowx and is 64-byte aligned, which satisfies the alignment
// requirement of every T here.
func unsafeData[T Fixed](b []byte) []T {
	if len(b) == 0 {
		return nil
	}
	var z T
	sz := int(unsafe.Sizeof(z))
	return unsafe.Slice((*T)(unsafe.Pointer(&b[0])), len(b)/sz)
}
