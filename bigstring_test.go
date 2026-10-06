package ursus_test

// A result whose String column would pass 2 GiB is refused, not wrapped: audit.md
// S24. Collect concatenates every batch into one column, so thirty million rows of
// 100-byte strings reached it with nothing else unusual about the query. The limit
// is lowered here so the test can reach it.

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/data"
)

func TestCollectRefusesAStringColumnPastItsOffsets(t *testing.T) {
	// One row holding a 5 KB string, as Parquet, written under the real limit.
	var pq bytes.Buffer
	if err := ursus.Frame(ursus.Values("s", []string{strings.Repeat("p", 5<<10)})).
		WriteParquet(t.Context(), &pq); err != nil {
		t.Fatal(err)
	}
	vals := make([]string, 200)
	for i := range vals {
		vals[i] = fmt.Sprintf("%064d", i)
	}
	// Built under the real limit: 12.8 KB of characters.
	lf := ursus.Frame(ursus.Values("s", vals), ursus.Values("n", spillInts(0, 200)))

	defer func(old int64) { data.MaxStringBytes = old }(data.MaxStringBytes)
	data.MaxStringBytes = 4 << 10

	t.Run("collecting it whole is refused", func(t *testing.T) {
		_, err := lf.Select(ursus.Col("s").Str().ToUpper()).Collect(t.Context(), ursus.WithBatchSize(16))
		if !errors.Is(err, ursus.ErrResource) {
			t.Fatalf("want a resource error, got %v", err)
		}
		if !strings.Contains(err.Error(), "CollectBatches") {
			t.Errorf("the refusal does not say what to do: %v", err)
		}
	})
	t.Run("collecting it in batches works", func(t *testing.T) {
		rows := 0
		for b, err := range lf.Select(ursus.Col("s").Str().ToUpper()).
			CollectBatches(t.Context(), ursus.WithBatchSize(16)) {
			if err != nil {
				t.Fatal(err)
			}
			rows += b.Height()
		}
		if rows != 200 {
			t.Errorf("%d rows, want 200", rows)
		}
	})
	// The Parquet reader recovers a panic in its own Next and calls it a corrupt
	// file; a column past the limit must say what it is instead.
	t.Run("a Parquet column past the limit is a resource error, not a corrupt file", func(t *testing.T) {
		_, err := ursus.ScanParquetBytes(pq.Bytes(), "big.parquet").Collect(t.Context())
		if !errors.Is(err, ursus.ErrResource) {
			t.Fatalf("want a resource error, got %v", err)
		}
	})
	t.Run("a result under the limit collects", func(t *testing.T) {
		df, err := lf.Head(50).Collect(t.Context(), ursus.WithBatchSize(16))
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 50 {
			t.Errorf("%d rows, want 50", df.Height())
		}
	})
}
