package ursus_test

// The Parquet reader's fast paths (step 123): a column stored as it is written is
// read straight into its buffer, and its nulls spread into place from the back;
// validity is built only once a null arrives. Random nulls, in runs and alone, at
// the edges of batches and row groups, must come back exactly.

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

func TestParquetReaderRoundTripsNulls(t *testing.T) {
	c := ursus.Col
	// Each pattern is a validity: none null; a few, at random, and in runs; most;
	// and nulls only where a batch's first 64 rows are all valid, which is where
	// validity starts being built part-way through a batch. Batches are 97 rows.
	patterns := []func(rng *rand.Rand, i int) bool{
		func(*rand.Rand, int) bool { return true },
		func(rng *rand.Rand, _ int) bool { return rng.Float64() >= 0.01 },
		func(rng *rand.Rand, _ int) bool { return rng.Float64() >= 0.3 },
		func(rng *rand.Rand, _ int) bool { return rng.Float64() >= 0.9 },
		func(_ *rand.Rand, i int) bool { return i%97 != 80 },
		func(_ *rand.Rand, i int) bool { return i%700%97 < 64 || i%3 != 0 },
	}
	for seed, pattern := range patterns {
		rng := rand.New(rand.NewPCG(123, uint64(seed)))
		n := 500 + rng.IntN(3000)
		valid := make([]bool, n)
		for i := range valid {
			valid[i] = pattern(rng, i)
		}
		if seed == 1 {
			for range 5 { // runs of nulls, across batch and row-group edges
				at := rng.IntN(n)
				for k := at; k < min(n, at+rng.IntN(200)); k++ {
					valid[k] = false
				}
			}
		}
		i64 := make([]int64, n)
		f64 := make([]float64, n)
		i32 := make([]int32, n)
		u64 := make([]uint64, n)
		i8 := make([]int8, n)
		s := make([]string, n)
		for i := range n {
			i64[i] = rng.Int64() - rng.Int64()
			f64[i] = rng.NormFloat64() * 1e6
			i32[i] = rng.Int32N(1 << 20)
			u64[i] = rng.Uint64()
			i8[i] = int8(rng.IntN(256) - 128)
			s[i] = string(rune('a' + i%26))
		}
		want, err := ursus.Frame(
			ursus.ValuesNullable("i64", i64, valid),
			ursus.ValuesNullable("f64", f64, valid),
			ursus.ValuesNullable("i32", i32, valid),
			ursus.ValuesNullable("u64", u64, valid),
			ursus.ValuesNullable("i8", i8, valid),
			ursus.ValuesNullable("s", s, valid),
		).WithColumns(
			c("i32").Cast(ursus.Date).Alias("date"),
			c("i64").Cast(dtype.Datetime(dtype.Micro, "")).Alias("ts"),
		).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := want.Lazy().WriteParquet(t.Context(), &buf, ursus.WithRowGroupRows(700)); err != nil {
			t.Fatal(err)
		}
		for _, threads := range []int{1, 4} {
			got, err := ursus.ScanParquetBytes(buf.Bytes(), "roundtrip.parquet").
				Collect(t.Context(), ursus.WithBatchSize(97), ursus.WithThreads(threads))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
	}
}

// TestStringsAcrossManySmallPages: a byte-array column is read a page at a time,
// its values aliasing the page until they are copied (step 124). A batch here
// spans dozens of 256-byte pages, dictionary-encoded and plain, with nulls, so a
// value copied after its page was replaced would read another page's bytes.
func TestStringsAcrossManySmallPages(t *testing.T) {
	for _, threads := range []int{1, 4} {
		df, err := ursus.ScanParquet("testdata/parquet/pyarrow_small_pages.parquet").
			Collect(t.Context(), ursus.WithBatchSize(97), ursus.WithThreads(threads))
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 3000 {
			t.Fatalf("%d rows, want 3000", df.Height())
		}
		for _, name := range []string{"dict", "plain"} {
			col, err := df.Column[string](name)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 3000 {
				got, ok := col.Get(i)
				if i%13 == 0 {
					if ok {
						t.Fatalf("%s row %d: %q, want null", name, i, got)
					}
					continue
				}
				if want := fmt.Sprintf("v%04d", i*7%1000); !ok || got != want {
					t.Fatalf("%s row %d: %q (%v), want %q", name, i, got, ok, want)
				}
			}
		}
	}
}
