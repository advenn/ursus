// Command qcheck runs ursus's random queries.
//
//	qcheck self -seed N -duration D [-cases K] [-variants V] [-alloc MiB] [-v]
//	qcheck duck -seed N -duration D [-cases K] [-alloc MiB] [-v]
//
// self checks each query against itself across threads, batch sizes, the optimizer
// and the records a table is read in (package self). It prints each finding: the
// case's seed, its tables, its query as Go, the reference and variant
// configurations, and the factors that cause it. Case i's seed is N+i, so
//
//	qcheck self -seed S -cases 1
//
// runs a reported case S alone. It exits 1 on any finding, a vacuous operation or a
// stale float-threads rule.
//
// duck checks each query against DuckDB (package compare, step 513): ursus under a
// configuration the case's seed picks, against sqlgen's SQL run in DuckDB. It prints
// each mismatch's seed, its tables and query as Go, the SQL and both answers, and
// exits 1 on any finding; qcheck duck -seed S -cases 1 runs case S alone. It needs
// a binary built with -tags duckdb,duckdb_arrow, which links DuckDB; without them it
// says so and exits 2.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/duck"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/self"
)

const usage = `usage: qcheck self -seed N -duration D [-cases K] [-variants V] [-alloc MiB] [-v]
       qcheck duck -seed N -duration D [-cases K] [-alloc MiB] [-v]`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "self":
		runSelf(os.Args[2:])
	case "duck":
		os.Exit(runDuck(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func runSelf(args []string) {
	fs := flag.NewFlagSet("self", flag.ExitOnError)
	seed := fs.Uint64("seed", 1, "the first case's seed; case i's is seed+i")
	duration := fs.Duration("duration", 20*time.Second, "how long to start cases for")
	cases := fs.Int("cases", 0, "stop after this many cases; 0 is no limit")
	variants := fs.Int("variants", 3, "variants a case runs beside the reference")
	verbose := fs.Bool("v", false, "print each case's seed and query as it finishes")
	alloc := fs.Int64("alloc", 1024, "MiB one run may allocate before fuzzcheck fails it")
	_ = fs.Parse(args)

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

// runDuck runs qcheck duck and returns the exit code.
func runDuck(args []string) int {
	fs := flag.NewFlagSet("duck", flag.ExitOnError)
	seed := fs.Uint64("seed", 1, "the first case's seed; case i's is seed+i")
	duration := fs.Duration("duration", 20*time.Second, "how long to start cases for")
	cases := fs.Int("cases", 0, "stop after this many cases; 0 is no limit")
	verbose := fs.Bool("v", false, "print each case's seed, query and SQL as it finishes")
	alloc := fs.Int64("alloc", 1024, "MiB one ursus run may allocate before fuzzcheck fails it")
	_ = fs.Parse(args)

	fmt.Printf("qcheck duck -seed %d -duration %v -cases %d\n", *seed, *duration, *cases)
	db, err := duck.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer db.Close()
	opts := compare.Options{Seed: *seed, Cases: *cases, Duration: *duration, Engine: db}
	opts.Limits.Alloc = *alloc << 20
	seen := 0
	opts.Progress = func(c *gen.Case, r *compare.Report) {
		for _, f := range r.Findings[seen:] {
			fmt.Println(f)
		}
		seen = len(r.Findings)
		if *verbose {
			fmt.Printf("case %d done\n%s\n", c.Seed, c)
		}
	}
	rep := compare.Run(opts)
	for _, f := range rep.Findings[seen:] {
		fmt.Println(f)
	}
	fmt.Print(rep.Summary())
	if rep.Failed() {
		return 1
	}
	return 0
}
