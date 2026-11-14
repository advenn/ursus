// Command qcheck runs ursus's random queries.
//
//	qcheck self -seed N -duration D [-cases K] [-variants V] [-alloc MiB] [-v]
//
// self checks each query against itself across threads, batch sizes, the optimizer
// and the records a table is read in (package self). It prints each finding: the
// case's seed, its tables, its query as Go, the reference and variant
// configurations, and the factors that cause it. Case i's seed is N+i, so
//
//	qcheck self -seed S -cases 1
//
// runs a reported case S alone. It exits 1 on any finding, a vacuous operation or a
// stale float-threads rule. Step 513 adds a duck subcommand, against DuckDB.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/self"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "self" {
		fmt.Fprintln(os.Stderr, "usage: qcheck self -seed N -duration D [-cases K] [-variants V] [-alloc MiB] [-v]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("self", flag.ExitOnError)
	seed := fs.Uint64("seed", 1, "the first case's seed; case i's is seed+i")
	duration := fs.Duration("duration", 20*time.Second, "how long to start cases for")
	cases := fs.Int("cases", 0, "stop after this many cases; 0 is no limit")
	variants := fs.Int("variants", 3, "variants a case runs beside the reference")
	verbose := fs.Bool("v", false, "print each case's seed and query as it finishes")
	alloc := fs.Int64("alloc", 1024, "MiB one run may allocate before fuzzcheck fails it")
	_ = fs.Parse(os.Args[2:])

	fmt.Printf("qcheck self -seed %d -duration %v -cases %d -variants %d\n", *seed, *duration, *cases, *variants)
	opts := self.Options{Seed: *seed, Cases: *cases, Duration: *duration, Variants: *variants}
	opts.Limits.Alloc = *alloc << 20
	seen := 0
	opts.Progress = func(c *gen.Case, r *self.Report) {
		for _, f := range r.Findings[seen:] {
			fmt.Println(f)
		}
		seen = len(r.Findings)
		if *verbose {
			fmt.Printf("case %d ok\n%s\n", c.Seed, c)
		}
	}
	rep := self.Run(opts)
	for _, f := range rep.Findings[seen:] {
		fmt.Println(f)
	}
	fmt.Print(rep.Summary())
	if rep.Failed() {
		os.Exit(1)
	}
}
