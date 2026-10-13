package ursus_test

import (
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// sortedFrame is n rows for windows in orders of their own: v an integer with runs of
// nulls longer than two batches, f a float with nulls, NaN, −0 and +0 among few values, x
// an integer whose value mod 7 ties across every batch, s a string, k a key that is
// 0 for the first 12,000 rows and one of 500 others after.
func sortedFrame(chunks, rows int) *ursus.LazyFrame {
	var frames []*ursus.LazyFrame
	for ch := range chunks {
		k, v, x := make([]int64, rows), make([]int64, rows), make([]int64, rows)
		vok, fok := make([]bool, rows), make([]bool, rows)
		f := make([]float64, rows)
		s := make([]string, rows)
		for j := range rows {
			i := ch*rows + j
			if i >= 12_000 && i%2 == 1 {
				k[j] = int64(1 + i%500)
			}
			v[j], vok[j] = int64(i%977)-400, i%1000 >= 130 && i%7 != 0
			x[j] = int64(i * 7919 % 100_003)
			f[j] = []float64{1.5, math.NaN(), math.Copysign(0, -1), 0, -2, 3.25}[i*31%6]
			fok[j] = i%11 != 0
			s[j] = fmt.Sprintf("s%03d", i*13%211)
		}
		frames = append(frames, ursus.Frame(ursus.Values("k", k), ursus.ValuesNullable("v", v, vok),
			ursus.Values("x", x), ursus.ValuesNullable("f", f, fok), ursus.Values("s", s)))
	}
	return ursus.Concat(frames)
}

// sortedWindows is four distinct orders, two of them the same keys in opposite
// directions, a function in the file's order and an aggregate, partitioned by part.
func sortedWindows(part ...ursus.Expr) []ursus.Expr {
	c := ursus.Col
	byF := ursus.WindowSpec{PartitionBy: part,
		OrderBy: []ursus.SortKey{ursus.Desc(c("f")).NullsLast(), ursus.Asc(c("x"))}}
	byFAsc := ursus.WindowSpec{PartitionBy: part,
		OrderBy: []ursus.SortKey{ursus.Asc(c("f")), ursus.Asc(c("x"))}}
	byFDesc := ursus.WindowSpec{PartitionBy: part,
		OrderBy: []ursus.SortKey{ursus.Desc(c("f")), ursus.Asc(c("x"))}}
	byX := ursus.WindowSpec{PartitionBy: part, OrderBy: []ursus.SortKey{ursus.Asc(c("x").Mod(7))}}
	return []ursus.Expr{
		c("v").CumSum(false).OverWith(byF).Alias("cs_f"),
		c("v").Shift(2).OverWith(byF).Alias("lag_f"),
		c("v").CumCount(false).OverWith(byF).Alias("cc_f"),
		c("v").ForwardFill(0).OverWith(byF).Alias("ff_f"),
		c("f").RollingMean(4, ursus.MinSamples(1)).OverWith(byF).Alias("rm_f"),
		c("s").CumMax(false).OverWith(byF).Alias("smax_f"),
		c("x").EwmMean(ursus.EwmAlpha(0.3)).OverWith(byF).Alias("ewm_f"),
		c("v").CumSum(false).OverWith(byFAsc).Alias("cs_fasc"),
		c("v").CumSum(false).OverWith(byFDesc).Alias("cs_fdesc"),
		c("v").CumSum(false).OverWith(byX).Alias("cs_x"),
		c("v").ShiftFill(-3, int64(-1)).OverWith(byX).Alias("lead_x"),
		c("v").CumCount(true).OverWith(byX).Alias("ccr_x"),
		c("v").CumSum(false).Over(part...).Alias("cs_file"),
		// Backward (step 236): in the file's order, and in an order of their own.
		c("v").CumSum(true).Over(part...).Alias("csr_file"),
		c("s").CumMin(true).Over(part...).Alias("sminr_file"),
		c("v").BackwardFill(0).Over(part...).Alias("bf_file"),
		c("v").CumProd(true).OverWith(byX).Alias("cpr_x"),
		c("x").CumMax(true).OverWith(byF).Alias("cmaxr_f"),
		c("v").BackwardFill(0).OverWith(byF).Alias("bf_f"),
		c("x").Sum().Over(part...).Alias("total"),
	}
}

// TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit: windows with an order_by over one
// huge partition (step 235), with no partition_by and over one hot key, where they
// refused. Four orders — a float descending with nulls last, NaN and −0 among its few
// values and an integer to break its ties; the same ascending, nulls first, and
// descending, nulls first; and x mod 7, an expression whose seven values tie across
// every batch — a function in the file's order, and an aggregate.
// Cumulative sums, a shift, cum_count either way, a forward fill over runs of nulls
// longer than two batches, a rolling mean, a cumulative max of strings, an ewm and a
// shift forward with a fill. And, walked backward (step 236), reversed cumulative
// sums, minima of strings, products and maxima, and backward fills with no limit,
// over runs of nulls longer than two batches, in the file's order and in an order of
// their own. Under a tenth of the input, on one thread and four, the answer is the
// in-memory one, row for row, and the spill directory is empty after.
func TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit(t *testing.T) {
	frame := sortedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	for name, q := range map[string]*ursus.LazyFrame{
		"no partition_by": frame.WithColumns(sortedWindows()...),
		"one hot key":     frame.WithColumns(sortedWindows(c("k"))...),
	} {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
		if err != nil {
			t.Fatal(err)
		}
		for _, threads := range []int{1, 4} {
			var stats ursus.MemoryStats
			dir := t.TempDir()
			got, err := q.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithThreads(threads),
				ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
			if err != nil {
				t.Fatalf("%s on %d threads, under %d bytes: %v", name, threads, limit, err)
			}
			if stats.Spills == 0 {
				t.Fatalf("%s on %d threads: nothing spilled", name, threads)
			}
			ursustest.AssertFrameEqual(t, got, want)
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("%s on %d threads: %d entries left in the spill directory", name, threads, len(left))
			}
		}
	}
}
