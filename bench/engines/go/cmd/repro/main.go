// Command repro is a standalone reproducer for a correctness bug the h2o suite
// found in ursus. It is not part of the benchmark; it exists so the finding can
// be handed over without the benchmark harness attached.
//
//	GOEXPERIMENT=simd go run ./cmd/repro
//
// The bug: binary arithmetic between two columns produced by a group-by drops
// the validity of the last 8 rows of every 8192-row output batch. Both inputs
// are non-null and the result is arithmetically defined, but it comes back NULL.
//
//	case A: non-nullable literal columns, a - b
//	  rows=100000 unexpected nulls=0
//	case B: all-valid nullable literal columns, a - b
//	  rows=100000 unexpected nulls=0
//	case C: group-by Max/Min then subtract
//	  rows=100000 unexpected nulls=96 first=[16376..16383 24568 24569]
//	case D: group-by Mean (Float64) then subtract
//	  rows=100000 unexpected nulls=96 first=[16376..16383 24568 24569]
//	case E: group-by Max/Min, no arithmetic (control)
//	  rows=100000 unexpected nulls=0
//
// The null runs are exactly the last 8 rows before each multiple of 8192 — one
// byte of the validity bitmap per output batch. It reproduces at Int32 and
// Float64, at 1 and 8 threads, and under GODEBUG=simd=0 and URSUS_KERNELS=scalar,
// so it is not a vector-width bug in a SIMD kernel; the scalar path has it too.
//
// Where it shows up in the suite: h2o gb7 (`max(v1) - min(v2) by id3`), whose
// answer disagrees with the duckdb reference by 96 groups out of 100,000.
package main

import (
	"context"
	"fmt"

	"ursus"
)

const n = 100_000

func main() {
	ctx := context.Background()

	fmt.Println("case A: non-nullable literal columns, a - b")
	report(ctx, plainFrame().Select(
		ursus.Col("a").Sub(ursus.Col("b")).Alias("d"),
	))

	fmt.Println("case B: all-valid nullable literal columns, a - b")
	report(ctx, nullableFrame().Select(
		ursus.Col("a").Sub(ursus.Col("b")).Alias("d"),
	))

	fmt.Println("case C: group-by Max/Min then subtract")
	report(ctx, plainFrame().
		GroupBy(ursus.Col("g")).
		Agg(
			ursus.Col("a").Max().Alias("ma"),
			ursus.Col("b").Min().Alias("mb"),
		).
		Select(ursus.Col("ma").Sub(ursus.Col("mb")).Alias("d")))

	fmt.Println("case D: group-by Mean (Float64) then subtract")
	reportFloat(ctx, plainFrame().
		GroupBy(ursus.Col("g")).
		Agg(
			ursus.Col("a").Mean().Alias("ma"),
			ursus.Col("b").Mean().Alias("mb"),
		).
		Select(ursus.Col("ma").Sub(ursus.Col("mb")).Alias("d")))

	fmt.Println("case E: group-by Max/Min, no arithmetic (control)")
	report(ctx, plainFrame().
		GroupBy(ursus.Col("g")).
		Agg(
			ursus.Col("a").Max().Alias("d"),
		))
}

func plainFrame() *ursus.LazyFrame {
	a, b := make([]int32, n), make([]int32, n)
	g := make([]int64, n)
	for i := range a {
		a[i] = int32(i%5 + 1)
		b[i] = int32(i%15 + 1)
		g[i] = int64(i)
	}
	return ursus.Frame(
		ursus.Values("g", g), ursus.Values("a", a), ursus.Values("b", b),
	)
}

func nullableFrame() *ursus.LazyFrame {
	a, b := make([]int32, n), make([]int32, n)
	valid := make([]bool, n)
	for i := range a {
		a[i] = int32(i%5 + 1)
		b[i] = int32(i%15 + 1)
		valid[i] = true
	}
	return ursus.Frame(
		ursus.ValuesNullable("a", a, valid), ursus.ValuesNullable("b", b, valid),
	)
}

func reportFloat(ctx context.Context, lf *ursus.LazyFrame) {
	df, err := lf.Collect(ctx, ursus.WithThreads(1))
	if err != nil {
		fmt.Println("  error:", err)
		return
	}
	var nulls []int
	total := 0
	for i := 0; i < df.Height(); i++ {
		if _, ok, _ := df.At[float64](i, "d"); !ok {
			total++
			if len(nulls) < 10 {
				nulls = append(nulls, i)
			}
		}
	}
	fmt.Printf("  rows=%d unexpected nulls=%d first=%v\n", df.Height(), total, nulls)
}

func report(ctx context.Context, lf *ursus.LazyFrame) {
	df, err := lf.Collect(ctx, ursus.WithThreads(1))
	if err != nil {
		fmt.Println("  error:", err)
		return
	}
	var nulls []int
	total := 0
	for i := 0; i < df.Height(); i++ {
		if _, ok, _ := df.At[int32](i, "d"); !ok {
			total++
			if len(nulls) < 10 {
				nulls = append(nulls, i)
			}
		}
	}
	fmt.Printf("  rows=%d unexpected nulls=%d first=%v\n", df.Height(), total, nulls)
}
