package ursus_test

// A result whose List column would pass 2^31-1 elements is refused, not wrapped
// (step 99), as a String column past 2 GiB of characters is (bigstring_test). The
// limit is lowered here so the test can reach it.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/data"
)

func TestCollectRefusesAListColumnPastItsOffsets(t *testing.T) {
	// Twenty rows of ten elements each, built and written under the real limit.
	row := strings.TrimSuffix(strings.Repeat("x,", 10), ",")
	vals := make([]string, 20)
	for i := range vals {
		vals[i] = row
	}
	df, err := ursus.Frame(ursus.Values("s", vals)).
		Select(ursus.Col("s").Str().Split(",").Alias("l")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var pq bytes.Buffer
	if err := df.Lazy().WriteParquet(t.Context(), &pq); err != nil {
		t.Fatal(err)
	}
	flat := ursus.Frame(ursus.Values("k", make([]int64, 200)), ursus.Values("v", make([]int64, 200)))

	defer func(old int64) { data.MaxListElements = old }(data.MaxListElements)
	data.MaxListElements = 100

	refused := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, ursus.ErrResource) {
			t.Fatalf("want a resource error, got %v", err)
		}
		if !strings.Contains(err.Error(), "CollectBatches") {
			t.Errorf("the refusal does not say what to do: %v", err)
		}
	}
	t.Run("collecting 200 elements whole is refused", func(t *testing.T) {
		_, err := df.Lazy().Collect(t.Context(), ursus.WithBatchSize(4))
		refused(t, err)
	})
	t.Run("collecting them in batches works", func(t *testing.T) {
		rows := 0
		for b, err := range df.Lazy().CollectBatches(t.Context(), ursus.WithBatchSize(4)) {
			if err != nil {
				t.Fatal(err)
			}
			rows += b.Height()
		}
		if rows != 20 {
			t.Errorf("%d rows, want 20", rows)
		}
	})
	t.Run("imploding 200 rows into one list is refused", func(t *testing.T) {
		_, err := flat.GroupBy(ursus.Col("k")).Agg(ursus.Col("v").Implode().Alias("vs")).Collect(t.Context())
		refused(t, err)
	})
	// The Parquet reader recovers a panic in its own Next and calls it a corrupt
	// file; a column past the limit must say what it is instead.
	t.Run("a Parquet list column past the limit is a resource error, not a corrupt file", func(t *testing.T) {
		_, err := ursus.ScanParquetBytes(pq.Bytes(), "lists.parquet").Collect(t.Context())
		if !errors.Is(err, ursus.ErrResource) {
			t.Fatalf("want a resource error, got %v", err)
		}
	})
	t.Run("control: under the limit collects", func(t *testing.T) {
		got, err := df.Lazy().Head(10).Collect(t.Context(), ursus.WithBatchSize(4))
		if err != nil {
			t.Fatal(err)
		}
		if got.Height() != 10 {
			t.Errorf("%d rows, want 10", got.Height())
		}
	})
}
