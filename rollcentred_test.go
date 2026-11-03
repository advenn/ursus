package ursus_test

// Step 430: a centred rolling window of rows streamed past a memory limit walks its
// lead rows ahead of the batch and nulls past the last, so its answer is the one in
// memory, to the bit.

import (
	"fmt"
	"math"
	"testing"

	"github.com/advenn/ursus"
)

// TestStreamedCentredRollingIsTheInMemoryBits: step 429's eight functions over a
// centred window of 2, 3, 8, 64, 101 and 200 rows, the last leading more rows than a
// batch holds, over 20,000 rows of sin(i)·1000 with nulls, NaN and ±Inf, in batches
// of 64 under a 16 KiB limit, over the whole frame, over a key holding nine rows in
// ten, and in an order of its own. Each window was rebuilt from the rows around each
// batch; each is the answer without a limit, bit for bit.
func TestStreamedCentredRollingIsTheInMemoryBits(t *testing.T) {
	const n = 20_000
	x, xok, y, k := make([]float64, n), make([]bool, n), make([]float64, n), make([]int64, n)
	for i := range n {
		x[i], xok[i] = math.Sin(float64(i))*1000, i%11 != 0
		switch {
		case i%997 == 5:
			x[i] = math.NaN()
		case i%1999 == 7:
			x[i] = math.Inf(1)
		}
		y[i] = math.Cos(float64(i)*0.7)*500 + float64(i%17)
		if i%10 == 0 {
			k[i] = int64(1 + i%7)
		}
	}
	frame := ursus.Frame(ursus.ValuesNullable("x", x, xok), ursus.Values("y", y), ursus.Values("k", k))
	c := ursus.Col
	var cols []ursus.Expr
	for _, size := range []int{2, 3, 8, 64, 101, 200} {
		at := func(f string) string { return fmt.Sprintf("%s %d", f, size) }
		ctr, min := ursus.RollingCenter(), ursus.MinSamples(size/2+1)
		cols = append(cols,
			c("x").RollingSum(size, ctr, min).Alias(at("sum")),
			c("x").RollingMean(size, ctr).Alias(at("mean")),
			c("x").RollingVar(size, ctr, min).Alias(at("var")),
			c("x").RollingStd(size, ctr, ursus.MinSamples(1)).Alias(at("std")),
			c("x").RollingSkew(size, true, ctr, min).Alias(at("skew")),
			c("x").RollingKurtosis(size, false, false, ctr, min).Alias(at("kurt")),
			ursus.RollingCov(c("x"), c("y"), size, ctr, min).Alias(at("cov")),
			ursus.RollingCorr(c("x"), c("y"), size, ctr).Alias(at("corr")),
		)
	}
	for name, lf := range map[string]*ursus.LazyFrame{
		"the whole frame":        frame.WithColumns(cols...),
		"over a key":             frame.WithColumns(overAll(cols, c("k"))...),
		"in an order of its own": frame.WithColumns(orderedAll(cols, ursus.Desc(c("y")))...),
	} {
		want, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s, under a 16 KiB limit: %v", name, err)
		}
		if st.Spills == 0 {
			t.Fatalf("%s answered without spilling, so it proves nothing", name)
		}
		assertSameFloatBits(t, name, got, want)
	}
}
