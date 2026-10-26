package ursus_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestACountReadsNoColumn: a query that reads no column, a count, scans none (step
// 370): its plan's scan has an empty projection, where it read every column, and
// every source answers it with its rows, under the operators that pass rows
// through, on one thread and on four, in batches of every size.
func TestACountReadsNoColumn(t *testing.T) {
	c := ursus.Col
	ctx := t.Context()
	const n = 1000
	g := make([]int64, n)
	s := make([]string, n)
	for i := range n {
		g[i], s[i] = int64(i%7), fmt.Sprintf("s%d", i)
	}
	lf := ursus.Frame(ursus.Values("g", g), ursus.Values("s", s))
	dir := t.TempDir()
	var pq, csv, nd bytes.Buffer
	if err := lf.WriteParquet(ctx, &pq); err != nil {
		t.Fatal(err)
	}
	if err := lf.WriteCSV(ctx, &csv); err != nil {
		t.Fatal(err)
	}
	if err := lf.WriteNDJSON(ctx, &nd); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"a.parquet": pq.Bytes(), "a.csv": csv.Bytes(), "a.jsonl": nd.Bytes()} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := lf.SinkIPC(ctx, filepath.Join(dir, "a.ipc")); err != nil {
		t.Fatal(err)
	}
	hive := filepath.Join(dir, "hive")
	if err := lf.SinkParquet(ctx, hive, ursus.WithPartitionBy("g")); err != nil {
		t.Fatal(err)
	}
	df, err := lf.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := df.Record()
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Release()

	plan, err := ursus.ScanParquet(filepath.Join(dir, "a.parquet")).Select(ursus.Len()).Explain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "projection: [] (0/2 cols)") {
		t.Errorf("a count's scan reads a column:\n%s", plan)
	}

	sources := []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"memory", lf},
		{"Parquet", ursus.ScanParquet(filepath.Join(dir, "a.parquet"))},
		{"CSV", ursus.ScanCSV(filepath.Join(dir, "a.csv"))},
		{"NDJSON", ursus.ScanNDJSON(filepath.Join(dir, "a.jsonl"))},
		{"IPC", ursus.ScanIPC(filepath.Join(dir, "a.ipc"))},
		{"Arrow", ursus.ScanArrowRecords(rec)},
		{"a hive dataset", ursus.ScanParquetGlob(filepath.Join(hive, "**", "*.parquet"), ursus.WithHivePartitioning(true))},
	}
	for _, src := range sources {
		f := src.lf
		queries := []struct {
			name string
			q    *ursus.LazyFrame
			want int64
		}{
			{"a count", f.Select(ursus.Len()), n},
			{"a group-by of no keys", f.GroupBy().Agg(ursus.Len()), n},
			{"a union", ursus.Concat([]*ursus.LazyFrame{f, f}).Select(ursus.Len()), 2 * n},
			{"a diagonal union", ursus.Concat([]*ursus.LazyFrame{f, f.Select(c("g"))},
				ursus.WithConcatMode(ursus.ConcatDiagonal)).Select(ursus.Len()), 2 * n},
			{"a head", f.Head(10).Select(ursus.Len()), 10},
			{"a slice", f.Slice(5, 20).Select(ursus.Len()), 20},
			{"a tail", f.Tail(10).Select(ursus.Len()), 10},
			{"a reverse", f.Reverse().Select(ursus.Len()), n},
			{"a shift", f.Shift(3).Select(ursus.Len()), n},
			{"every tenth row", f.GatherEvery(10, 0).Select(ursus.Len()), 100},
			{"a cache", f.Cache().Select(ursus.Len()), n},
			{"an hstack", f.Select(c("g")).HStack(f.Select(c("s").Alias("t"))).Select(ursus.Len()), n},
			{"a cross join", f.Head(10).Join(f.Head(7), ursus.JoinHow(ursus.JoinCross)).Select(ursus.Len()), 70},
			{"a filter", f.Filter(c("g").Eq(3)).Select(ursus.Len()), 143},
			{"a row index", f.WithRowIndex("i", 0).Select(c("i").Max()), n - 1},
		}
		for _, threads := range []int{1, 4} {
			for _, size := range []int{0, 64} {
				opts := []ursus.CollectOption{ursus.WithThreads(threads)}
				if size > 0 {
					opts = append(opts, ursus.WithBatchSize(size))
				}
				where := fmt.Sprintf("%s on %d threads in batches of %d", src.name, threads, size)
				for _, q := range queries {
					got, err := q.q.Collect(ctx, opts...)
					if err != nil {
						t.Fatalf("%s, %s: %v", where, q.name, err)
					}
					r, err := got.Row(0)
					if err != nil {
						t.Fatal(err)
					}
					if fmt.Sprint(r[0]) != fmt.Sprint(q.want) {
						t.Errorf("%s, %s: %v rows, want %d", where, q.name, r[0], q.want)
					}
				}
				if got, err := f.Count(ctx, opts...); err != nil || got != n {
					t.Errorf("%s, Count: %d, %v; want %d", where, got, err, n)
				}
			}
		}
	}
}

// TestAParquetCountReadsNoColumnChunk: a count of a Parquet file is its footer's
// rows (step 370): a file whose every column chunk is garbage counts, and only a
// read of a column refuses it.
func TestAParquetCountReadsNoColumnChunk(t *testing.T) {
	ctx := t.Context()
	var pq bytes.Buffer
	lf := ursus.Frame(ursus.Values("x", spillInts(0, 5000)), ursus.Values("y", spillInts(0, 5000)))
	if err := lf.WriteParquet(ctx, &pq, ursus.WithRowGroupRows(1000)); err != nil {
		t.Fatal(err)
	}
	b := pq.Bytes()
	footer := len(b) - 8 - int(binary.LittleEndian.Uint32(b[len(b)-8:]))
	for i := 4; i < footer; i++ {
		b[i] = 0xA5 // every page and chunk, after the leading magic and before the footer
	}
	path := filepath.Join(t.TempDir(), "garbage.parquet")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	scan := func() *ursus.LazyFrame { return ursus.ScanParquet(path) }
	for _, threads := range []int{1, 4} {
		if got, err := scan().Count(ctx, ursus.WithThreads(threads)); err != nil || got != 5000 {
			t.Errorf("Count on %d threads: %d, %v; want 5000", threads, got, err)
		}
		df, err := scan().Select(ursus.Len()).Collect(ctx, ursus.WithThreads(threads))
		if err != nil {
			t.Fatalf("a count on %d threads: %v", threads, err)
		}
		if r, _ := df.Row(0); fmt.Sprint(r[0]) != "5000" {
			t.Errorf("a count on %d threads: %v rows, want 5000", threads, r[0])
		}
		if _, err := scan().Select(ursus.Col("x")).Collect(ctx, ursus.WithThreads(threads)); err == nil {
			t.Errorf("a read of a garbage column chunk on %d threads succeeded", threads)
		}
	}
}

// TestACSVCountIsItsRows: a count of a CSV file reads no column, and is the rows a
// read gives, through quoted newlines, blank lines, comments and skipped rows, in
// batches of a row and more (step 370).
func TestACSVCountIsItsRows(t *testing.T) {
	cases := []struct {
		name string
		text string
		opts []ursus.CSVOption
	}{
		{"plain", "a,b\n1,x\n2,y\n3,z\n", nil},
		{"no final newline", "a,b\n1,x\n2,y\n3,z", nil},
		{"CRLF", "a,b\r\n1,x\r\n2,y\r\n", nil},
		{"quoted newlines", "a,b\n1,\"x\ny\"\n2,\"p\nq\nr\"\n3,z\n", nil},
		{"blank lines", "a,b\n1,x\n\n2,y\n\n\n3,z\n", nil},
		{"comments", "a,b\n#c\n1,x\n#d\n2,y\n", []ursus.CSVOption{ursus.WithComment("#")}},
		{"no header", "1,x\n2,y\n3,z\n", []ursus.CSVOption{ursus.WithHasHeader(false)}},
		{"rows skipped", "junk\njunk\na,b\n1,x\n2,y\n", []ursus.CSVOption{ursus.WithSkipRows(2)}},
		{"rows skipped after the header", "a,b\n1,x\n2,y\n3,z\n4,w\n", []ursus.CSVOption{ursus.WithSkipRowsAfterHeader(2)}},
		{"lines skipped", "x\ny\na,b\n1,x\n", []ursus.CSVOption{ursus.WithSkipLines(2)}},
		{"one column with a blank line", "a\n1\n\n2\n", nil},
		{"a header alone", "a,b\n", nil},
		{"an empty quoted field", "a\n1\n\"\"\n2\n", nil},
	}
	for _, tc := range cases {
		for _, size := range []int{1, 2, 0} {
			var opts []ursus.CollectOption
			if size > 0 {
				opts = append(opts, ursus.WithBatchSize(size))
			}
			lf := ursus.ScanCSVReader([]byte(tc.text), "x.csv", tc.opts...)
			df, err := lf.Collect(t.Context(), opts...)
			if err != nil {
				t.Fatal(tc.name, err)
			}
			n, err := lf.Count(t.Context(), opts...)
			if err != nil {
				t.Fatal(tc.name, err)
			}
			if n != int64(df.Height()) {
				t.Errorf("%s in batches of %d: counted %d, read %d", tc.name, size, n, df.Height())
			}
		}
	}
}
