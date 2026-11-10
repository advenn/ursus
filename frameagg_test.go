package ursus_test

import (
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFrameAggregates is C3 of v0.6.7-scope.md (step 300): Polars' DataFrame.sum,
// mean, median, min, max, std, var, quantile, product and null_count. Every column
// is kept, a null where the aggregate does not take it. Every value and type is the
// frameagg group's, Polars 2.1.0's (oracle_frameagg_test.go), where feature-gaps.md §9
// names the Int128 sum, the Float64 product, the Uint64 count, the empty sum and
// product null and the variance's ulp; here the eager forms answer as the lazy ones.
func TestFrameAggregates(t *testing.T) {
	at := func(d, h int) time.Time { return time.Date(2026, 1, d, h, 0, 0, 0, time.UTC) }
	ok := []bool{true, true, false, true}
	lf := ursus.Frame(
		ursus.ValuesNullable("i", []int64{1, 2, 0, 4}, ok),
		ursus.Values("i8", []int8{1, 2, 3, 4}),
		ursus.ValuesNullable("f", []float64{1.5, 0, 2.5, 4}, []bool{true, false, true, true}),
		ursus.ValuesNullable("b", []bool{true, false, true, false}, []bool{true, true, true, false}),
		ursus.ValuesNullable("s", []string{"p", "q", "", "r"}, ok),
		ursus.ValuesNullable("d", []time.Time{at(1, 0), at(3, 0), {}, at(8, 0)}, ok),
		ursus.ValuesNullable("t", []time.Time{at(1, 6), {}, at(2, 6), at(3, 6)}, []bool{true, false, true, true}),
		ursus.ValuesNullable("u", []time.Duration{time.Hour, 2 * time.Hour, 0, 4 * time.Hour}, ok),
		ursus.Values("l", []string{"1", "2,3", "4", "5"}),
	).WithColumns(ursus.Col("d").Cast(ursus.Date), ursus.Col("t").Cast(ursus.Datetime(ursus.Micro, "")),
		ursus.Col("l").Str().Split(","))

	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		got  func() (*ursus.DataFrame, error)
		want *ursus.LazyFrame
	}{
		"Sum":       {func() (*ursus.DataFrame, error) { return df.Sum(t.Context()) }, lf.Sum()},
		"Mean":      {func() (*ursus.DataFrame, error) { return df.Mean(t.Context()) }, lf.Mean()},
		"Median":    {func() (*ursus.DataFrame, error) { return df.Median(t.Context()) }, lf.Median()},
		"Min":       {func() (*ursus.DataFrame, error) { return df.Min(t.Context()) }, lf.Min()},
		"Max":       {func() (*ursus.DataFrame, error) { return df.Max(t.Context()) }, lf.Max()},
		"Std":       {func() (*ursus.DataFrame, error) { return df.Std(t.Context(), 0) }, lf.Std(0)},
		"Var":       {func() (*ursus.DataFrame, error) { return df.Var(t.Context(), 1) }, lf.Var(1)},
		"Product":   {func() (*ursus.DataFrame, error) { return df.Product(t.Context()) }, lf.Product()},
		"NullCount": {func() (*ursus.DataFrame, error) { return df.NullCount(t.Context()) }, lf.NullCount()},
		"Quantile": {func() (*ursus.DataFrame, error) {
			return df.Quantile(t.Context(), 0.25, ursus.InterpLinear)
		}, lf.Quantile(0.25, ursus.InterpLinear)},
	} {
		got, err := tc.got()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, err := tc.want.Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}

// TestFrameAggregatesUnderAMemoryLimit holds the aggregates to their pieces: a Select
// of aggregates is a keyless group-by, which streams its input. A median and a
// quantile hold their values, as they do by key, and spill them. A float aggregate's
// last bits are the thread count's (feature-gaps.md §1), and a group-by under a limit
// runs on one, so the answer without a limit is one thread's.
func TestFrameAggregatesUnderAMemoryLimit(t *testing.T) {
	const n = 20_000
	a, s := make([]int64, n), make([]string, n)
	for i := range n {
		a[i], s[i] = int64(i*7919%10007), []string{"p", "q", "r"}[i%3]
	}
	lf := ursus.Frame(ursus.Values("a", a), ursus.Values("s", s))
	for name, q := range map[string]*ursus.LazyFrame{
		"sum": lf.Sum(), "mean": lf.Mean(), "min": lf.Min(), "std": lf.Std(1), "null count": lf.NullCount(),
		"median": lf.Median(), "quantile": lf.Quantile(0.9, ursus.InterpLinear),
	} {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(err)
		}
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(64<<10),
			ursus.WithSpillDir(t.TempDir()))
		if err != nil {
			t.Fatalf("%s under 64 KiB: %v", name, err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}
