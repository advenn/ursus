// Command opbench times ursus operations with no file IO anywhere.
//
// The suite proper always includes reading, because that is what a real query
// does. It also means a slow Parquet reader can hide a slow engine — PDS-H q6
// puts ursus within 15% of hand-written arrow-go kernels, which says more about
// arrow-go's reader than about ursus. This asks the other question: given data
// that is already in memory, how fast is the operation itself?
//
//	GOEXPERIMENT=simd go run ./cmd/opbench          # this file
//	uv run python scripts/opbench_polars.py         # the polars twin
//
// Both build the same 5M-row frame from the same deterministic formulas — no
// RNG, so Go and Python produce identical values — materialise it once outside
// the timed region, then time only the operation. Warm-up plus three
// iterations, median reported.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"time"

	"github.com/advenn/ursus"
)

const (
	rows       = 5_000_000
	joinRows   = 100_000
	iterations = 3

	// leftChunks splits the probe side into that many source batches.
	//
	// It has to be more than one, and that is the whole point. ursus.Frame hands its
	// batches to memsrc, which returns them as given and ignores WithBatchSize — so a
	// frame built from one set of slices is ONE batch however small the batch size.
	// A join whose probe side is one batch has exactly one unit of work to hand out,
	// so no number of threads can help it.
	//
	// That is what step 27 measured when it reported the join as "1,098 ms on one
	// thread, 1,253 ms on eight": the extra threads had nothing to do and only added
	// dispatch cost. The negative result was real; its cause was the FIXTURE, not the
	// join. A real query's probe side comes from a scan, which yields a batch every
	// few thousand rows, so this is the more representative shape as well as the
	// measurable one.
	leftChunks = 64
)

// The flags exist because this file is the only place an ursus operation can be
// profiled without a Parquet reader in the way.
//
// bench/micro reaches every operation through a file, so a profile of it is
// mostly arrow-go: BenchmarkJoinInner spends 87% of its samples outside the join.
// The suite proper has the same property by design — it measures what a real
// query does, IO included. Neither can answer "where does the JOIN go", which is
// the question that matters once an operation is known to be slow.
//
//	GOEXPERIMENT=simd go run ./cmd/opbench -only join -cpuprofile /tmp/j.cpu
//	go tool pprof -top /tmp/j.cpu
var (
	cpuProfile = flag.String("cpuprofile", "", "write a CPU profile here")
	memProfile = flag.String("memprofile", "", "write an allocation profile here")
	only       = flag.String("only", "", "run only the named operation")
	threads    = flag.Int("threads", 0, "ursus worker threads; 0 leaves the default")
)

func main() {
	flag.Parse()
	ctx := context.Background()

	// The fixture is built BEFORE the profile starts, so materialising 5M rows
	// does not appear in it. That is the same reason the timed region excludes it.
	left, right := build(ctx)
	// Only the join uses the chunked probe side; the other five operations keep the
	// single-batch frame so their numbers stay comparable with earlier runs.
	chunked := chunkLeft(ctx, left)

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cpuprofile:", err)
			os.Exit(1)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "cpuprofile:", err)
			os.Exit(1)
		}
		defer pprof.StopCPUProfile()
	}
	if *memProfile != "" {
		defer writeMemProfile(*memProfile)
	}

	run("filter", func() { collect(ctx, left.Lazy().Filter(ursus.Col("qty").Gt(int64(50)))) })

	run("project_arith", func() {
		collect(ctx, left.Lazy().Select(
			ursus.Col("val").Mul(ursus.Col("qty")).Alias("total")))
	})

	run("groupby_100", func() {
		collect(ctx, left.Lazy().
			GroupBy(ursus.Col("grp")).
			Agg(ursus.Col("val").Sum().Alias("s")))
	})

	run("groupby_100k", func() {
		collect(ctx, left.Lazy().
			GroupBy(ursus.Col("key")).
			Agg(ursus.Col("val").Sum().Alias("s")))
	})

	run("groupby_string", func() {
		collect(ctx, left.Lazy().
			GroupBy(ursus.Col("label")).
			Agg(ursus.Col("val").Sum().Alias("s")))
	})

	run("sort", func() {
		collect(ctx, left.Lazy().
			Select(ursus.Col("id"), ursus.Col("val")).
			Sort(ursus.Desc(ursus.Col("val"))))
	})

	run("join", func() {
		collect(ctx, chunked.
			Select(ursus.Col("key"), ursus.Col("val")).
			Join(right.Lazy(), ursus.JoinOn(ursus.Col("key"))))
	})
}

func build(ctx context.Context) (*ursus.DataFrame, *ursus.DataFrame) {
	id := make([]int64, rows)
	key := make([]int64, rows)
	grp := make([]int64, rows)
	qty := make([]int64, rows)
	val := make([]float64, rows)
	label := make([]string, rows)

	labels := make([]string, 100)
	for i := range labels {
		labels[i] = fmt.Sprintf("label%03d", i)
	}

	for i := range id {
		id[i] = int64(i)
		key[i] = int64(i) * 2654435761 % joinRows
		grp[i] = int64(i) * 7919 % 100
		qty[i] = int64(i % 101)
		val[i] = float64(int64(i)*2654435761%1000000) / 1000.0
		label[i] = labels[i%100]
	}

	left, err := ursus.Frame(
		ursus.Values("id", id),
		ursus.Values("key", key),
		ursus.Values("grp", grp),
		ursus.Values("qty", qty),
		ursus.Values("val", val),
		ursus.Values("label", label),
	).Collect(ctx)
	fatal(err)

	rkey := make([]int64, joinRows)
	rpay := make([]float64, joinRows)
	for i := range rkey {
		rkey[i] = int64(i)
		rpay[i] = float64(i) * 1.5
	}
	right, err := ursus.Frame(
		ursus.Values("key", rkey),
		ursus.Values("payload", rpay),
	).Collect(ctx)
	fatal(err)

	return left, right
}

func collect(ctx context.Context, lf *ursus.LazyFrame) {
	var opts []ursus.CollectOption
	if *threads > 0 {
		opts = append(opts, ursus.WithThreads(*threads))
	}
	df, err := lf.Collect(ctx, opts...)
	fatal(err)
	if df.Height() < 0 {
		os.Exit(1)
	}
}

// writeMemProfile writes the live heap after a GC, which is what makes the
// numbers comparable between runs.
func writeMemProfile(path string) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "memprofile:", err)
		return
	}
	defer f.Close()
	runtime.GC()
	if err := pprof.WriteHeapProfile(f); err != nil {
		fmt.Fprintln(os.Stderr, "memprofile:", err)
	}
}

func run(name string, fn func()) {
	if *only != "" && *only != name {
		return
	}
	fn() // warm-up

	timings := make([]float64, 0, iterations)
	for range iterations {
		started := time.Now()
		fn()
		timings = append(timings, time.Since(started).Seconds()*1000)
	}
	sort.Float64s(timings)
	fmt.Printf("%-16s %9.1f ms\n", name, timings[len(timings)/2])
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// chunkLeft re-expresses a frame as leftChunks separate frames concatenated, so the
// source yields that many batches instead of one. See leftChunks.
func chunkLeft(ctx context.Context, df *ursus.DataFrame) *ursus.LazyFrame {
	per := df.Height() / leftChunks
	parts := make([]*ursus.LazyFrame, 0, leftChunks)
	for c := range leftChunks {
		n := per
		if c == leftChunks-1 {
			n = df.Height() - c*per // the tail keeps the remainder
		}
		out, err := df.Lazy().Slice(c*per, n).Collect(ctx)
		fatal(err)
		parts = append(parts, out.Lazy())
	}
	return ursus.Concat(parts)
}
