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
	"fmt"
	"os"
	"sort"
	"time"

	"ursus"
)

const (
	rows       = 5_000_000
	joinRows   = 100_000
	iterations = 3
)

func main() {
	ctx := context.Background()

	left, right := build(ctx)

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
		collect(ctx, left.Lazy().
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
	df, err := lf.Collect(ctx)
	fatal(err)
	if df.Height() < 0 {
		os.Exit(1)
	}
}

func run(name string, fn func()) {
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
