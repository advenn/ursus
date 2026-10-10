//go:build race

package parquet

// raceDetector is whether the race detector is built in. Under it, arrow-go's
// byte-array decoding allocates more than it does in any other build (step 163),
// so an allocation bound measures the instrumentation, not the reader.
const raceDetector = true
