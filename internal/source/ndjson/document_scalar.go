//go:build !(goexperiment.simd && amd64)

package ndjson

// vectorScan is false where the simd experiment is off or the CPU is not amd64: an
// object is read a byte at a time.
var vectorScan = false

func (o *objectLines) objectVector(b []byte, r int) (int, bool) { return r, false }
