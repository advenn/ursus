//go:build !chdb

// Package chdbengine runs the benchmark suites against chdb-go.
//
// chdb-go needs libchdb.so, which is a large download, so it is behind a build
// tag: `make setup-cgo` builds it, `make setup-go` does not. This stub keeps the
// engine name resolvable either way.
package chdbengine

import "ursusbench/engine"

func init() { engine.Register("chdbgo", Build) }

// Build reports that this binary was compiled without chdb-go.
func Build(a engine.Args) (engine.Once, error) {
	return nil, engine.Unsupported("chdb-go: built without -tags chdb (install libchdb.so, then `make setup-cgo`)")
}
