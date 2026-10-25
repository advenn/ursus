//go:build !(goexperiment.simd && amd64)

package kernel

import "unicode/utf8"

// utf8Vector is false where the simd experiment is off or the CPU is not amd64:
// ValidUTF8 is utf8.Valid.
var utf8Vector = false

func validUTF8Vector(b []byte) bool { return utf8.Valid(b) }
