package ursus_test

// Step 429: a rolling window of rows streamed past a memory limit carries its
// kernel's state from one batch to the next, so its answer is the one in memory, to
// the bit.

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestStreamedRollingIsTheInMemoryBits: a rolling sum, mean, var, std, skew,
// kurtosis, cov and corr over 20,000 rows of sin(i)·1000 with nulls, NaN and ±Inf,
// and over a Float32 copy, in batches of 64 under a 16 KiB limit, over the whole
// frame, over a key holding nine rows in ten, and in an order of its own, which a
// sorted pass walks (step 235). Each window was rebuilt from the
// rows before each batch, so its refreshes fell at other rows, and 35 to 52% of a
// var's rows differed in their last bits; each is the answer without a limit, bit for
// bit.
func TestStreamedRollingIsTheInMemoryBits(t *testing.T) {
	const n = 20_000
	x, xok, y, k := make([]float64, n), make([]bool, n), make([]float64, n), make([]int64, n)
	x32 := make([]float32, n)
	for i := range n {
		x[i], xok[i] = math.Sin(float64(i))*1000, i%13 != 0
		switch {
		case i%997 == 5:
			x[i] = math.NaN()
		case i%1999 == 7:
			x[i] = math.Inf(1)
		case i%2003 == 11:
			x[i] = math.Inf(-1)
		}
		x32[i] = float32(x[i])
		y[i] = math.Cos(float64(i)*0.7)*500 + float64(i%17)
		if i%10 != 0 {
			k[i] = 0
		} else {
			k[i] = int64(1 + i%7)
		}
	}
	frame := ursus.Frame(ursus.ValuesNullable("x", x, xok), ursus.ValuesNullable("x32", x32, xok),
		ursus.Values("y", y), ursus.Values("k", k))
	c := ursus.Col
	var cols []ursus.Expr
	for _, size := range []int{2, 7, 64, 100} {
		min := ursus.MinSamples(size/2 + 1)
		for _, in := range []string{"x", "x32"} {
			at := func(f string) string { return fmt.Sprintf("%s %s %d", f, in, size) }
			cols = append(cols,
				c(in).RollingSum(size, min).Alias(at("sum")),
				c(in).RollingMean(size).Alias(at("mean")),
				c(in).RollingVar(size, min).Alias(at("var")),
				c(in).RollingStd(size, ursus.Ddof(0)).Alias(at("std")),
				c(in).RollingSkew(size, false, min).Alias(at("skew")),
				c(in).RollingKurtosis(size, true, true).Alias(at("kurt")),
				ursus.RollingCov(c(in), c("y"), size, min).Alias(at("cov")),
				ursus.RollingCorr(c(in), c("y"), size).Alias(at("corr")),
			)
		}
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

// overAll is each expression over by.
func overAll(cols []ursus.Expr, by ursus.Expr) []ursus.Expr {
	out := make([]ursus.Expr, len(cols))
	for i, e := range cols {
		out[i] = e.Over(by)
	}
	return out
}

// orderedAll is each expression in key's order, over the whole frame.
func orderedAll(cols []ursus.Expr, key ursus.SortKey) []ursus.Expr {
	out := make([]ursus.Expr, len(cols))
	for i, e := range cols {
		out[i] = e.OverWith(ursus.WindowSpec{OrderBy: []ursus.SortKey{key}})
	}
	return out
}

// assertSameFloatBits holds got's answers, the columns named with a space, to
// want's, bit for bit, and counts the rows that differ in each.
func assertSameFloatBits(t *testing.T, name string, got, want *ursus.DataFrame) {
	t.Helper()
	if got.Height() != want.Height() {
		t.Fatalf("%s: %d rows, want %d", name, got.Height(), want.Height())
	}
	for _, col := range want.Columns() {
		if !strings.Contains(col, " ") {
			continue
		}
		bits := func(df *ursus.DataFrame) ([]uint64, []bool) {
			out, ok := make([]uint64, df.Height()), make([]bool, df.Height())
			if s, err := df.Column[float64](col); err == nil {
				for i := range out {
					v, has := s.Get(i)
					out[i], ok[i] = math.Float64bits(v), has
				}
				return out, ok
			}
			s, err := df.Column[float32](col)
			if err != nil {
				t.Fatalf("%s: %s is no float: %v", name, col, err)
			}
			for i := range out {
				v, has := s.Get(i)
				out[i], ok[i] = uint64(math.Float32bits(v)), has
			}
			return out, ok
		}
		g, gok := bits(got)
		w, wok := bits(want)
		differ, first := 0, -1
		for i := range w {
			if gok[i] != wok[i] || wok[i] && g[i] != w[i] {
				if differ++; first < 0 {
					first = i
				}
			}
		}
		if differ > 0 {
			t.Errorf("%s: %s differs in %d of %d rows, the first %d", name, col, differ, len(w), first)
		}
	}
}
