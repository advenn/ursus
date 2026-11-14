package ursus_test

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// Batches of one row, found by qcheck's self check (step 512) as allocations past
// fuzzcheck's bound at WithBatchSize(1): three operators did work, for each batch,
// as long as every batch or group they held, quadratic in the batches. Each test is a
// differential: the answer in batches of one row, on one thread and on four, is the
// reference's, one thread and batches of 8192; and what it allocates is bounded.
// Neither is parallel: the allocation is read from the process's count.

// tinyBatchesBound is four to fifteen times what each query below allocates in
// batches of one row since the fixes, 8 to 30 MB, their batches' own headers; before
// them, 0.8 to 8 GB.
const tinyBatchesBound = 128 << 20

// tinyBatches is a record of n rows drawn from seed: an Int64 key of a thousand
// values, and text of 0 to 49 bytes.
func tinyBatches(t *testing.T, seed uint64, n int) arrow.RecordBatch {
	r := rand.New(rand.NewPCG(seed, 1))
	b := array.NewRecordBuilder(memory.NewGoAllocator(), arrow.NewSchema([]arrow.Field{
		{Name: "k", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "v", Type: arrow.BinaryTypes.String, Nullable: true},
	}, nil))
	defer b.Release()
	for range n {
		b.Field(0).(*array.Int64Builder).Append(int64(r.IntN(1000)))
		b.Field(1).(*array.StringBuilder).Append(strings.Repeat("x", r.IntN(50)))
	}
	rec := b.NewRecordBatch()
	t.Cleanup(rec.Release)
	return rec
}

// inOneRowBatches checks q in batches of one row against the reference.
func inOneRowBatches(t *testing.T, q func() *ursus.LazyFrame) {
	t.Helper()
	want, err := q().Collect(t.Context(), ursus.WithThreads(1), ursus.WithBatchSize(8192), ursus.WithMemoryLimit(0))
	if err != nil {
		t.Fatal(err)
	}
	for _, threads := range []int{1, 4} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := q().Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(1), ursus.WithMemoryLimit(0))
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > tinyBatchesBound {
			t.Errorf("threads %d: in batches of one row it allocated %.1f MiB, over %d MiB",
				threads, float64(alloc)/(1<<20), tinyBatchesBound>>20)
		}
	}
}

// TestASortOfOneRowBatchesAllocatesLinearly. A sort keeps every input batch and
// gathers each output batch out of them. gatherRows made two arrays as long as the
// kept batches for every gather, so a sort of n rows in batches of one row made n
// gathers of n-long arrays: 2.3 GB allocated, and 400 ms, to sort 8,191 rows,
// against 0.6 MB at the default batch size.
func TestASortOfOneRowBatchesAllocatesLinearly(t *testing.T) {
	rec := tinyBatches(t, 512, 8191)
	inOneRowBatches(t, func() *ursus.LazyFrame {
		return ursus.ScanArrowRecords(rec).Sort(ursus.Asc(ursus.Col("k")))
	})
}

// TestANUniqueOfOneRowBatchesAllocatesLinearly. An accumulator reserves its group
// count before each batch, and NUnique's table of (group, value) pairs grew its keys
// to that count exactly, so a group-by finding a new group each batch copied every
// pair each batch: a NUnique by 8,193 distinct keys in batches of one row allocated
// 842 MB, against 2 MB at the default batch size.
func TestANUniqueOfOneRowBatchesAllocatesLinearly(t *testing.T) {
	r := rand.New(rand.NewPCG(514, 1))
	b := array.NewRecordBuilder(memory.NewGoAllocator(), arrow.NewSchema([]arrow.Field{
		{Name: "k", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "v", Type: arrow.PrimitiveTypes.Int32, Nullable: true},
	}, nil))
	defer b.Release()
	for range 8193 {
		b.Field(0).(*array.Int64Builder).Append(int64(r.Uint64())) // distinct, almost surely
		b.Field(1).(*array.Int32Builder).Append(int32(r.IntN(10)))
	}
	rec := b.NewRecordBatch()
	defer rec.Release()
	inOneRowBatches(t, func() *ursus.LazyFrame {
		return ursus.ScanArrowRecords(rec).GroupBy(ursus.Col("k")).MaintainOrder().
			Agg(ursus.Col("v").NUnique().Alias("n"))
	})
}

// TestASliceFromTheEndOfOneRowBatchesAllocatesLinearly. A slice from the end holds
// the last rows in a ring of batches, and recounted every batch it held in its
// memory account after each batch it read: the last 4,000 of 8,193 rows in batches
// of one row allocated 6 GB in 12 s, and a slice from before the start, which holds
// them all, 8 GB in 13 s.
func TestASliceFromTheEndOfOneRowBatchesAllocatesLinearly(t *testing.T) {
	rec := tinyBatches(t, 513, 8193)
	for _, offset := range []int{-4000, -9000, -100} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			inOneRowBatches(t, func() *ursus.LazyFrame {
				return ursus.ScanArrowRecords(rec).Slice(offset, 5000)
			})
		})
	}
}
