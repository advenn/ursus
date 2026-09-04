// Command runner executes one benchmark query with one Go engine.
//
// It is the Go counterpart of `python -m engines.py.<engine>_runner`: same
// flags, same JSON output, same timing rules. The driver launches it once per
// (engine, query) pair.
//
//	runner --engine ursus --suite pdsh --query q1 --data DIR \
//	       --io parquet --iterations 3 --threads 8 \
//	       --out result.json --result answer.parquet
//
// Engines register themselves from init functions, so the ones behind build
// tags are simply absent when the tag is off:
//
//	go build -o bin/runner     ./cmd/runner              # pure Go engines
//	go build -tags duckdb,chdb -o bin/runner-cgo ./cmd/runner
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"ursusbench/engine"

	// Engines, registered by their init functions.
	_ "ursusbench/arrowengine"
	_ "ursusbench/chdbengine"
	_ "ursusbench/duckdbengine"
	_ "ursusbench/gotaengine"
	_ "ursusbench/qframeengine"
	_ "ursusbench/ursusengine"
)

// start is taken before anything else runs so `startup_s` covers process boot
// and package initialisation, the same span the Python harness measures.
var start = time.Now()

func main() {
	args := engine.ParseArgs()

	if args.Engine == "" {
		fmt.Fprintf(os.Stderr, "--engine is required; compiled in: %s\n",
			strings.Join(engine.Names(), ", "))
		os.Exit(2)
	}

	build, ok := engine.Lookup(args.Engine)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown engine %q; compiled in: %s\n",
			args.Engine, strings.Join(engine.Names(), ", "))
		os.Exit(2)
	}

	os.Exit(engine.Run(args, build, time.Since(start)))
}
