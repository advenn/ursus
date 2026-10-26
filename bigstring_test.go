package ursus_test

// A result whose String column passes 2 GiB holds 64-bit offsets (step 368), where
// it was refused (audit.md S24, step 99): Collect concatenates every batch into one
// column, so thirty million rows of 100-byte strings reached it with nothing else
// unusual about the query, and so does every operator that gathers its input whole,
// a sort's, a join's build side, a window's. A batch a source reads keeps 32-bit
// offsets and refuses past them. The limit is lowered here so the test can reach it.

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/data"
)

func TestTextPastItsOffsets(t *testing.T) {
	c := ursus.Col
	ctx := t.Context()
	// One row holding a 5 KB string, as Parquet, written under the real limit.
	var pq bytes.Buffer
	if err := ursus.Frame(ursus.Values("s", []string{strings.Repeat("p", 5<<10)})).
		WriteParquet(ctx, &pq); err != nil {
		t.Fatal(err)
	}
	vals := make([]string, 200)
	for i := range vals {
		vals[i] = fmt.Sprintf("v%063d", (i*37)%200)
	}
	// 12.8 KB of characters, built under the real limit.
	lf := ursus.Frame(ursus.Values("s", vals), ursus.Values("n", spillInts(0, 200)))
	queries := []struct {
		name string
		q    *ursus.LazyFrame
	}{
		{"collected", lf.Select(c("s").Str().ToUpper(), c("n"))},
		{"sorted by another column", lf.Sort(ursus.Desc(c("n")))},
		{"sorted by it", lf.Sort(ursus.Desc(c("s")))},
		{"grouped by it", lf.GroupBy(c("s")).Agg(c("n").Sum()).Sort(ursus.Asc(c("s")))},
		{"joined on it", lf.Join(lf, ursus.JoinOn(c("s"))).Sort(ursus.Asc(c("n")))},
		{"its unique rows", lf.Unique("s").Sort(ursus.Asc(c("n")))},
		{"a window over it", lf.Select(c("s"), c("n").Sum().Over(c("s")))},
		{"imploded", lf.Select(c("s").Implode())},
		{"imploded and exploded", lf.Select(c("s").Implode()).Explode("s")},
		{"its maximum", lf.Select(c("s").Max())},
	}
	want := make([]*ursus.DataFrame, len(queries))
	for i, q := range queries {
		df, err := q.q.Collect(ctx, ursus.WithBatchSize(16))
		if err != nil {
			t.Fatal(q.name, err)
		}
		want[i] = df
	}

	limit := data.MaxStringBytes
	defer func() { data.MaxStringBytes = limit }()
	data.MaxStringBytes = 4 << 10

	t.Run("every operator answers as under the limit", func(t *testing.T) {
		for i, q := range queries {
			for _, threads := range []int{1, 4} {
				got, err := q.q.Collect(ctx, ursus.WithBatchSize(16), ursus.WithThreads(threads))
				if err != nil {
					t.Fatalf("%s on %d threads: %v", q.name, threads, err)
				}
				sameFrame(t, fmt.Sprintf("%s on %d threads", q.name, threads), got, want[i])
			}
		}
	})

	wide, err := queries[0].q.Collect(ctx, ursus.WithBatchSize(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("exported as a LargeString, and read back", func(t *testing.T) {
		rec, err := wide.Record()
		if err != nil {
			t.Fatal(err)
		}
		defer rec.Release()
		if got := rec.Schema().Field(0).Type.ID(); got != arrow.LARGE_STRING {
			t.Fatalf("exported as %s", rec.Schema().Field(0).Type)
		}
		back, err := ursus.ScanArrowRecords(rec).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sameFrame(t, "read back", back, wide)
	})

	// Written whole; read back under the real limit, since a CSV reader's batch is a
	// block of bytes, not of rows.
	t.Run("written and read back", func(t *testing.T) {
		dir := t.TempDir()
		var p, csv, nd bytes.Buffer
		if err := wide.Lazy().WriteParquet(ctx, &p); err != nil {
			t.Fatal(err)
		}
		if err := wide.Lazy().WriteCSV(ctx, &csv); err != nil {
			t.Fatal(err)
		}
		if err := wide.Lazy().WriteNDJSON(ctx, &nd); err != nil {
			t.Fatal(err)
		}
		ipcPath := filepath.Join(dir, "wide.ipc")
		if err := wide.Lazy().SinkIPC(ctx, ipcPath); err != nil {
			t.Fatal(err)
		}
		data.MaxStringBytes = limit
		defer func() { data.MaxStringBytes = 4 << 10 }()
		for name, back := range map[string]*ursus.LazyFrame{
			"Parquet": ursus.ScanParquetBytes(p.Bytes(), "wide.parquet"),
			"CSV":     ursus.ScanCSVReader(csv.Bytes(), "wide.csv"),
			"NDJSON":  ursus.ScanNDJSONBytes(nd.Bytes(), "wide.jsonl"),
			"IPC":     ursus.ScanIPC(ipcPath),
		} {
			got, err := back.Collect(ctx, ursus.WithBatchSize(16))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			sameFrame(t, name, got, wide)
		}
	})

	t.Run("sorted under a memory limit, spilling", func(t *testing.T) {
		var st ursus.MemoryStats
		got, err := wide.Lazy().Sort(ursus.Desc(c("n"))).Collect(ctx, ursus.WithBatchSize(16),
			ursus.WithMemoryLimit(4<<10), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatal(err)
		}
		if st.Spills == 0 {
			t.Error("no spill")
		}
		ref, err := wide.Lazy().Sort(ursus.Desc(c("n"))).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sameFrame(t, "spilled", got, ref)
	})

	t.Run("collecting it in batches works", func(t *testing.T) {
		rows := 0
		for b, err := range lf.Select(c("s").Str().ToUpper()).
			CollectBatches(ctx, ursus.WithBatchSize(16)) {
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
	// file; a batch past the limit must say what it is instead.
	t.Run("a Parquet batch past the limit is a resource error, not a corrupt file", func(t *testing.T) {
		_, err := ursus.ScanParquetBytes(pq.Bytes(), "big.parquet").Collect(ctx)
		if !errors.Is(err, ursus.ErrResource) {
			t.Fatalf("want a resource error, got %v", err)
		}
		if !strings.Contains(err.Error(), "smaller batches") {
			t.Errorf("the refusal does not say what to do: %v", err)
		}
	})
}
