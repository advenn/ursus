package ursus_test

// Step 130: under the default budget Collect counts its result, and refuses a
// result too large to hold rather than growing until the kernel kills the process.
//
// Each test lowers the default budget, which is process-wide, so none of them is
// parallel: Go starts the parallel tests only once every sequential one is done.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// countingSource is an in-memory source that counts the batches read from it.
type countingSource struct {
	*memsrc.Source
	read *atomic.Int64
}

func (s countingSource) Open(ctx context.Context, spec source.ScanSpec) (source.BatchSource, error) {
	r, err := s.Source.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return countingReader{r, s.read}, nil
}

type countingReader struct {
	source.BatchSource
	read *atomic.Int64
}

func (r countingReader) Next(ctx context.Context) (*data.Batch, error) {
	b, err := r.BatchSource.Next(ctx)
	if err == nil {
		r.read.Add(1)
	}
	return b, err
}

// separateBatches is n batches of rows Int64 values, each its own allocation, so
// a result holding them holds rows*8 bytes more with each.
func separateBatches(t *testing.T, n, rows int) countingSource {
	t.Helper()
	schema, err := dtype.NewSchema(dtype.NotNull("v", dtype.Int64))
	if err != nil {
		t.Fatal(err)
	}
	batches := make([]*data.Batch, n)
	for i := range batches {
		vals := make([]int64, rows)
		for j := range vals {
			vals[j] = int64(i*rows + j)
		}
		if batches[i], err = data.NewBatch(schema, []*data.Column{
			data.NewFixed("v", dtype.Int64, vals, bitmap.View{}),
		}); err != nil {
			t.Fatal(err)
		}
	}
	src, err := memsrc.New(schema, batches...)
	if err != nil {
		t.Fatal(err)
	}
	return countingSource{src, new(atomic.Int64)}
}

// TestCollectRefusesAResultTooLargeToHold: 64 batches of 32 KiB, 2 MiB in all,
// under a default budget of 256 KiB. A result may take, with what the operators
// hold, three quarters of the ceiling, which the default budget is half of: 384
// KiB, or twelve batches.
//
// Collect refuses, naming itself and what streams instead, and it refuses once the
// result is too large, not once the stream has ended: refusing at the end would be
// after the memory had filled. The same query streams, and Collect holds it under a
// limit the caller gave, or none.
func TestCollectRefusesAResultTooLargeToHold(t *testing.T) {
	defer ursus.SetDefaultMemoryLimit(256 << 10)()
	src := separateBatches(t, 64, 4096)
	q := ursus.Scan(src)
	opts := []ursus.CollectOption{ursus.WithThreads(1), ursus.WithBatchSize(4096)}

	_, err := q.Collect(t.Context(), opts...)
	if err == nil {
		t.Fatal("a 2 MiB result under a 256 KiB default budget was collected")
	}
	if !errors.Is(err, ursus.ErrResource) {
		t.Errorf("want a resource error, got %v", err)
	}
	for _, want := range []string{"collect", "CollectBatches", "SinkParquet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	t.Log(err)
	if n := src.read.Load(); n > 16 {
		t.Errorf("refused after reading %d of 64 batches; past the twelfth, the result was "+
			"already too large", n)
	}

	src.read.Store(0)
	rows := 0
	for df, err := range q.CollectBatches(t.Context(), opts...) {
		if err != nil {
			t.Fatalf("streamed, the same result was refused: %v", err)
		}
		rows += df.Height()
	}
	if rows != 64*4096 {
		t.Errorf("streamed %d rows, want %d", rows, 64*4096)
	}

	for _, o := range []struct {
		name string
		opt  ursus.CollectOption
	}{
		{"a limit the caller gave", ursus.WithMemoryLimit(256 << 10)},
		{"no limit", ursus.WithMemoryLimit(0)},
	} {
		df, err := q.Collect(t.Context(), append(opts, o.opt)...)
		if err != nil {
			t.Errorf("under %s, Collect refused: %v", o.name, err)
			continue
		}
		if df.Height() != 64*4096 {
			t.Errorf("under %s, %d rows, want %d", o.name, df.Height(), 64*4096)
		}
	}

	// A result that fits is collected under the default budget too.
	df, err := q.Head(10*4096).Collect(t.Context(), opts...)
	if err != nil {
		t.Fatalf("ten batches, under a limit of twelve: %v", err)
	}
	if df.Height() != 10*4096 {
		t.Errorf("%d rows, want %d", df.Height(), 10*4096)
	}
}

// TestCollectSpillsAsTheStreamDoes: the result Collect holds is kept off what the
// operators see, so they spill exactly as they would if it were streamed.
//
// A union of a 240 KB frame and a sort of a 48 KB one, under a default budget of
// 256 KiB. The frame's rows reach Collect before the sort reads anything. The sort
// alone fits; with the frame's rows charged to it, it would be over, and spill or
// refuse. A spilled join or group-by replaying its partitions is the case this
// stands for: each partition's sub-sink would start over budget, and partition
// again until it reached maxSpillDepth.
func TestCollectSpillsAsTheStreamDoes(t *testing.T) {
	defer ursus.SetDefaultMemoryLimit(256 << 10)()
	first := make([]int64, 30000)
	for i := range first {
		first[i] = int64(i)
	}
	second := make([]int64, 6000)
	for i := range second {
		second[i] = int64(len(first) + len(second) - 1 - i)
	}
	q := ursus.Concat([]*ursus.LazyFrame{
		ursus.Frame(ursus.Values("v", first)),
		ursus.Frame(ursus.Values("v", second)).Sort(ursus.Asc(ursus.Col("v"))),
	})
	run := func(name string, collect func(...ursus.CollectOption) ([]int64, error)) {
		t.Helper()
		var st ursus.MemoryStats
		got, err := collect(ursus.WithThreads(1), ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Spills != 0 {
			t.Errorf("%s: the sort spilled %d runs; alone it fits", name, st.Spills)
		}
		if len(got) != len(first)+len(second) || !slices.IsSorted(got) {
			t.Errorf("%s: %d rows, sorted %v; want %d in order", name, len(got),
				slices.IsSorted(got), len(first)+len(second))
		}
	}
	run("collected", func(opts ...ursus.CollectOption) ([]int64, error) {
		df, err := q.Collect(t.Context(), opts...)
		if err != nil {
			return nil, err
		}
		return readAll[int64](df, "v"), nil
	})
	run("streamed", func(opts ...ursus.CollectOption) ([]int64, error) {
		var all []int64
		for df, err := range q.CollectBatches(t.Context(), opts...) {
			if err != nil {
				return nil, err
			}
			all = append(all, readAll[int64](df, "v")...)
		}
		return all, nil
	})
}
