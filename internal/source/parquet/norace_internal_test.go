//go:build !race

package parquet

// raceDetector is whether the race detector is built in: see race_internal_test.go.
const raceDetector = false
