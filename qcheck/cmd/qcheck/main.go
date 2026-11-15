// Command qcheck runs ursus's random queries.
//
//	qcheck self -seed N -duration D [-cases K] [-variants V] [-alloc MiB] [-v] [-shrink [-shrink-dir DIR]]
//	qcheck duck -seed N -duration D [-cases K] [-alloc MiB] [-v] [-shrink [-shrink-dir DIR]]
//	qcheck shrink (-case FILE | -seed N [-duck] [-variants V]) [-out DIR] [-corpus DIR] [-open TEXT] [-note TEXT] [-runs R] [-time T] [-v]
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
//
// shrink shrinks one finding to the smallest case that fails the same way (package
// shrink, step 514): a case's file, or the first finding of the case a seed draws,
// run by self, or by duck with -duck. It writes the shrunk case's file, its Go test
// in package ursus_test, DuckDB's SQL for a DuckDB case and a Polars snippet into
// -out, and exits 1 when the case does not fail; -corpus writes the case's file into
// the corpus too, and -open marks it a known open finding, naming the step that
// fixes it. With -shrink, self and duck shrink each finding of the run, at most
// three, into -shrink-dir, which ci.yml uploads.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/duck"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/self"
	"github.com/advenn/ursus/qcheck/shrink"
)

const usage = `usage: qcheck self -seed N -duration D [-cases K] [-variants V] [-alloc MiB] [-v] [-shrink [-shrink-dir DIR]]
       qcheck duck -seed N -duration D [-cases K] [-alloc MiB] [-v] [-shrink [-shrink-dir DIR]]
       qcheck shrink (-case FILE | -seed N [-duck] [-variants V]) [-out DIR] [-corpus DIR] [-open TEXT] [-note TEXT] [-runs R] [-time T] [-v]`

// maxShrunk is how many of a run's findings -shrink shrinks: plan step 455's three.
const maxShrunk = 3

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
	case "shrink":
		os.Exit(runShrink(os.Args[2:]))
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
	doShrink := fs.Bool("shrink", false, "shrink each finding, at most three, into -shrink-dir")
	shrinkDir := fs.String("shrink-dir", "qcheck-findings", "where -shrink writes each shrunk case and its test")
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
	if *doShrink {
		var cs []*shrink.Case
		for _, f := range rep.Findings {
			if c, err := shrink.FromSelf(f); err == nil {
				cs = append(cs, c)
			}
		}
		shrinkAll(cs, *shrinkDir, nil, opts.Limits.Alloc)
	}
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
	doShrink := fs.Bool("shrink", false, "shrink each finding, at most three, into -shrink-dir")
	shrinkDir := fs.String("shrink-dir", "qcheck-findings", "where -shrink writes each shrunk case and its test")
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
	if *doShrink {
		var cs []*shrink.Case
		for _, f := range rep.Findings {
			if c, err := shrink.FromDuck(f); err == nil {
				cs = append(cs, c)
			}
		}
		shrinkAll(cs, *shrinkDir, db, opts.Limits.Alloc)
	}
	if rep.Failed() {
		return 1
	}
	return 0
}

// shrinkAll shrinks the first maxShrunk cases into dir.
func shrinkAll(cs []*shrink.Case, dir string, engine compare.Engine, alloc int64) {
	if len(cs) > maxShrunk {
		fmt.Printf("shrinking %d of %d findings\n", maxShrunk, len(cs))
		cs = cs[:maxShrunk]
	}
	for _, c := range cs {
		opts := shrink.Options{Engine: engine}
		opts.Limits.Alloc = alloc
		shrinkOne(c, dir, opts)
	}
}

// shrinkOne shrinks c into dir and prints what it did, and returns the shrunk case,
// or nil when it could not.
func shrinkOne(c *shrink.Case, dir string, opts shrink.Options) *shrink.Case {
	fmt.Printf("qcheck shrink: %s, seed %d, %s, under %v\n", c.Source, c.Seed, c.Mode, c.Config)
	res, err := shrink.Shrink(c, opts)
	if err != nil {
		fmt.Printf("  not shrunk: %v\n", err)
		return nil
	}
	p := res.Case.Shrunk
	spent := ""
	if res.Spent {
		spent = ", the budget spent"
	}
	fmt.Printf("  signature: %v\n  from %v\n  to   %v\n  %d runs, %d queries, %v%s; it failed alike %d of 3 times again\n",
		res.Case.Signature, p.From, p.To, res.Runs, res.Queries, res.Elapsed.Round(time.Millisecond), spent,
		res.Reproduced)
	fmt.Printf("  steps kept: %v\n", res.Steps)
	paths, notes, err := shrink.WriteArtifacts(dir, res.Case, res.Detail)
	for _, path := range paths {
		fmt.Printf("  wrote %s\n", path)
	}
	for _, n := range notes {
		fmt.Printf("  %s\n", n)
	}
	if err != nil {
		fmt.Printf("  writing: %v\n", err)
		return nil
	}
	return res.Case
}

// runShrink runs qcheck shrink and returns the exit code.
func runShrink(args []string) int {
	fs := flag.NewFlagSet("shrink", flag.ExitOnError)
	file := fs.String("case", "", "a case's file to shrink")
	seed := fs.Uint64("seed", 0, "shrink the first finding of the case this seed draws")
	useDuck := fs.Bool("duck", false, "with -seed: the finding DuckDB makes, not the self check's")
	variants := fs.Int("variants", 3, "with -seed: variants the self check runs beside the reference")
	out := fs.String("out", "qcheck-shrunk", "where the shrunk case and its test are written")
	runs := fs.Int("runs", shrink.DefaultRuns, "the most candidates run")
	limit := fs.Duration("time", shrink.DefaultTime, "the longest a shrink runs for")
	verbose := fs.Bool("v", false, "print each step kept")
	open := fs.String("open", "", "mark the shrunk case a known open finding: the step that fixes it, and what it is")
	corpus := fs.String("corpus", "", "also write the shrunk case's file into this directory: testdata/corpus")
	note := fs.String("note", "", "a note the shrunk case's file keeps: where the finding was found")
	_ = fs.Parse(args)

	opts := shrink.Options{MaxRuns: *runs, MaxTime: *limit}
	if *verbose {
		opts.Log = func(format string, a ...any) { fmt.Printf("  "+format+"\n", a...) }
	}
	var c *shrink.Case
	needDuck := *useDuck
	switch {
	case *file != "":
		var err error
		if c, err = shrink.Load(*file); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		needDuck = c.Mode == shrink.ModeDuck
	case *seed == 0:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	var db *duck.DB
	if needDuck {
		var err error
		if db, err = duck.Open(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer db.Close()
		opts.Engine = db
	}
	if c == nil {
		var err error
		if c, err = findingOf(*seed, db, *variants); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if *open != "" {
		c.Open = *open
	}
	if *note != "" {
		c.Note = *note
	}
	shrunk := shrinkOne(c, *out, opts)
	if shrunk == nil {
		return 1
	}
	if *corpus != "" {
		path := filepath.Join(*corpus, shrunk.Name()+".json")
		if err := shrink.Save(shrunk, path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("  wrote %s\n", path)
	}
	return 0
}

// findingOf is the first finding of seed's case that is a case to shrink: the self
// check's, or DuckDB's where db is set.
func findingOf(seed uint64, db *duck.DB, variants int) (*shrink.Case, error) {
	if db != nil {
		rep := compare.Run(compare.Options{Seed: seed, Cases: 1, Engine: db})
		for _, f := range rep.Findings {
			if c, err := shrink.FromDuck(f); err == nil {
				return c, nil
			}
		}
		return nil, fmt.Errorf("seed %d: DuckDB finds nothing to shrink", seed)
	}
	rep := self.Run(self.Options{Seed: seed, Cases: 1, Variants: variants})
	for _, f := range rep.Findings {
		if c, err := shrink.FromSelf(f); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("seed %d: the self check finds nothing to shrink", seed)
}
