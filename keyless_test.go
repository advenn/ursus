package ursus_test

import (
	"fmt"
	"testing"

	"github.com/advenn/ursus"
)

// TestAFramesAggregateAnswersAsAGroupsDoes: an aggregate over the whole frame, which
// takes its columns whole since step 372, answers as the same aggregate of one group
// does, a group-by of a constant key: counts, sums and means of integers, floats and
// Booleans, past 64 bits too, minima and maxima, firsts and lasts, a filtered sum, an
// aggregate that takes no column whole beside ones that do; over nulls, an empty
// frame, on one thread and on four, to the bit.
func TestAFramesAggregateAnswersAsAGroupsDoes(t *testing.T) {
	c := ursus.Col
	ctx := t.Context()
	const n = 5000
	x := make([]int64, n)
	big := make([]int64, n)
	f := make([]float64, n)
	b := make([]bool, n)
	valid := make([]bool, n)
	for i := range n {
		x[i] = int64(i*7919%2003) - 1000
		big[i] = 1<<62 + int64(i)
		f[i] = float64(x[i]) * 0.37
		b[i] = i%3 == 0
		valid[i] = i%13 != 7
	}
	lf := ursus.Frame(ursus.ValuesNullable("x", x, valid), ursus.Values("big", big),
		ursus.ValuesNullable("f", f, valid), ursus.Values("b", b))
	aggs := []ursus.Expr{
		ursus.Len().Alias("len"), c("x").Count().Alias("count"), c("x").NullCount().Alias("nulls"),
		c("x").Sum().Alias("sum"), c("x").Mean().Alias("mean"), c("big").Sum().Alias("bigsum"),
		c("big").Mean().Alias("bigmean"), c("f").Sum().Alias("fsum"), c("f").Mean().Alias("fmean"),
		c("b").Sum().Alias("bsum"), c("x").Min().Alias("min"), c("x").Max().Alias("max"),
		c("f").Min().Alias("fmin"), c("f").Max().Alias("fmax"), c("x").First().Alias("first"),
		c("f").Last().Alias("last"), c("x").Filter(c("x").Gt(0)).Sum().Alias("possum"),
	}
	// A median takes no column whole, nor a Decimal's sum, which keeps its carry a row.
	mixed := []ursus.Expr{c("x").Sum().Alias("sum"), c("x").Median().Alias("median"), ursus.Len().Alias("len")}
	decimal := []ursus.Expr{c("x").Cast(ursus.Decimal(12, 2)).Sum().Alias("dsum"), ursus.Len().Alias("len")}
	for _, frame := range []struct {
		name string
		lf   *ursus.LazyFrame
	}{{"a frame", lf}, {"an empty frame", lf.Filter(c("x").Gt(1 << 20))}} {
		for _, set := range [][]ursus.Expr{aggs, mixed, decimal} {
			for _, threads := range []int{1, 4} {
				for _, size := range []int{0, 333} {
					opts := []ursus.CollectOption{ursus.WithThreads(threads)}
					if size > 0 {
						opts = append(opts, ursus.WithBatchSize(size))
					}
					what := fmt.Sprintf("%s of %d aggregates on %d threads in batches of %d", frame.name, len(set), threads, size)
					got, err := frame.lf.Select(set...).Collect(ctx, opts...)
					if err != nil {
						t.Fatalf("%s: %v", what, err)
					}
					want, err := frame.lf.WithColumns(ursus.Lit(int64(0)).Alias("__k")).GroupBy(c("__k")).
						Agg(set...).Drop("__k").Collect(ctx, opts...)
					if err != nil {
						t.Fatalf("%s: %v", what, err)
					}
					if frame.name == "an empty frame" {
						// A group-by of no rows has no group; a frame's aggregate has its row.
						if got.Height() != 1 {
							t.Errorf("%s: %d rows, want 1", what, got.Height())
						}
						continue
					}
					sameFrame(t, what, got, want)
				}
			}
		}
	}
}
