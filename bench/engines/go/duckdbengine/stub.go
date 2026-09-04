//go:build !duckdb

// Package duckdbengine runs the benchmark suites against duckdb-go.
//
// duckdb-go is cgo and links a large prebuilt library, so it is behind a build
// tag: `make setup-cgo` builds it, `make setup-go` does not. This stub keeps the
// engine name resolvable either way, so a run that asks for it without the tag
// gets a clear `unsupported` reason instead of "unknown engine".
package duckdbengine

import "ursusbench/engine"

func init() { engine.Register("duckdbgo", Build) }

// Build reports that this binary was compiled without duckdb-go.
func Build(a engine.Args) (engine.Once, error) {
	return nil, engine.Unsupported("duckdb-go: built without -tags duckdb (run `make setup-cgo`)")
}
