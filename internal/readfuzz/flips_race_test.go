//go:build race

package readfuzz_test

// flipStride is how far apart the byte-flip sweep's offsets are: every eighth under
// the race detector, which slows each read some tenfold.
const flipStride = 8
