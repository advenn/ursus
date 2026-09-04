// Package micro holds the ursus-only microbenchmarks.
//
// These do not compare ursus to anything; they measure it against itself so a
// regression shows up as a number. They are separate from the ones in the root
// package for two reasons.
//
// First, coverage. The root bench_test.go builds every fixture in memory with
// internal/source/memsrc and never touches a file, so CSV and Parquet reading —
// the first thing any real workload does — is not measured anywhere. Neither is
// the batch-size knob, streaming collection, window functions, string kernels,
// or the as-of join.
//
// Second, altitude. This module reaches ursus through a replace directive, so
// it can only use the public API. That is a feature: what it measures is what a
// user would get, and it cannot accidentally benchmark an internal shortcut no
// caller can take.
//
// Fixtures are written once into a temp directory and reused across benchmarks
// in the same run, so the cost of building them is not charged to anything.
//
//	make micro                # -benchmem -count=10, into results/micro.txt
//	make micro-baseline       # record it as the comparison point
//	make micro-diff           # benchstat current vs baseline
package micro
