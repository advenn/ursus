//go:build !race

package readfuzz_test

// flipStride is how far apart the byte-flip sweep's offsets are: every one, without
// the race detector (flips_race_test.go).
const flipStride = 1
