// Command repro is a standalone regression test for the validity-bitmap bug the
// h2o suite found. It is not part of the benchmark; it exists so the finding can
// be reproduced, and now re-checked, without the harness attached.
//
//	GOEXPERIMENT=simd go run ./cmd/repro
//
// All five cases should print `unexpected nulls=0`. They do as of step 19.
//
// # What it found
//
// Binary arithmetic between two columns produced by a group-by dropped the
// validity of the last 8 rows of every 8192-row output batch. Both inputs were
// non-null and the result arithmetically defined, but it came back NULL:
//
//	case C: group-by Max/Min then subtract
//	  rows=100000 unexpected nulls=96 first=[16376..16383 24568 24569]
//	case D: group-by Mean (Float64) then subtract
//	  rows=100000 unexpected nulls=96 first=[16376..16383 24568 24569]
//
// The null runs were exactly the last 8 rows before each multiple of 8192 — one
// byte of the validity bitmap per output batch. Cases A, B and E stayed clean,
// which is what narrowed it: the inputs had to come from a group-by (so the
// column carried a non-zero offset) and arithmetic had to combine two bitmaps.
//
// # Where the bug actually was
//
// Not in ursus. `arrow-go v18.7.0`, `arrow/bitutil/bitmaps.go:535`, inside
// `alignedBitmapOp`:
//
//	endMask := (lOffset + length%8)     // parses as lOffset + (length % 8)
//
// It means `(lOffset + length) % 8` — does this range end mid-byte? With the
// typo, a range starting at a non-zero offset and ending ON a byte boundary gets
// a spuriously non-zero endMask, `lastByteMask` becomes `TrailingBitmask[0]` =
// 0xFF, and the final byte is masked out entirely. So BitmapAnd, BitmapOr and
// BitmapAndNot silently dropped the last eight bits of any result whose source
// offset was non-zero. At offset 0 the typo is harmless, which is why nothing
// else in the suite tripped it.
//
// Worth keeping: this is the shape of bug that unit tests miss and a
// cross-engine differential check catches. h2o gb7 was the only query in 37 that
// disagreed with duckdb, by 96 groups out of 100,000.
package main

import (
	"context"
	"fmt"

	"github.com/advenn/ursus"
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
