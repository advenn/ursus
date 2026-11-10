package ursus_test

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// files is every file below dir, by its path from dir, in order.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func samePartFrame(t *testing.T, what string, got, want *ursus.DataFrame) {
	t.Helper()
	ok, err := got.Equals(t.Context(), want, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("%s:\n got:\n%s\nwant:\n%s", what, got, want)
	}
}

// Step 367's partitioned sinks: a Parquet dataset by keys, each key's directory and
// file named as Polars names them, its values escaped where Polars escapes them, a
// null the default partition, files of at most so many rows, and CSV, IPC and NDJSON
// datasets. Each layout is Polars 2.1.0's since step 502, recorded in
// testdata/oracle/partitionwrite.json and spelled in oracle_partitionwrite_test.go;
// here stay the datasets of each format read back as the frame they were, more keys
// than files held open, and the refusals.

// TestPartitionedSinkOfEachFormat: CSV, IPC and NDJSON datasets, each read back as the
// frame it was (their file names are the oracle's).
func TestPartitionedSinkOfEachFormat(t *testing.T) {
	ctx := t.Context()
	lf := ursus.Frame(ursus.Values("k", []string{"b", "a", "b"}), ursus.Values("v", []int64{1, 2, 3}))
	expect, err := lf.Sort(ursus.Asc(ursus.Col("v"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		ext  string
		sink func(dir string) error
		scan func(dir string) *ursus.LazyFrame
	}{
		{".csv", func(dir string) error { return lf.SinkCSV(ctx, dir, ursus.WithPartitionBy("k")) },
			func(dir string) *ursus.LazyFrame { return ursus.ScanCSV(dir) }},
		{".ipc", func(dir string) error { return lf.SinkIPC(ctx, dir, ursus.WithPartitionBy("k")) },
			func(dir string) *ursus.LazyFrame { return ursus.ScanIPC(dir, ursus.WithIPCHivePartitioning(false)) }},
		{".jsonl", func(dir string) error { return lf.SinkNDJSON(ctx, dir, ursus.WithPartitionBy("k")) },
			func(dir string) *ursus.LazyFrame { return ursus.ScanNDJSON(dir) }},
	} {
		dir := filepath.Join(t.TempDir(), "out")
		if err := f.sink(dir); err != nil {
			t.Fatal(err)
		}
		back, err := f.scan(dir).Select(ursus.Col("k"), ursus.Col("v")).Sort(ursus.Asc(ursus.Col("v"))).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		samePartFrame(t, f.ext+" read back", back, expect)
	}
}

// TestPartitionedSinkOfManyKeys: more keys than files held open, their rows
// interleaved, each key's files numbered on as its file is closed and begun again,
// every row read back, and no temporary file left.
func TestPartitionedSinkOfManyKeys(t *testing.T) {
	ctx := t.Context()
	const n = 3000
	k := make([]int64, n)
	v := make([]int64, n)
	for i := range n {
		k[i], v[i] = int64(i%150), int64(i)
	}
	lf := ursus.Frame(ursus.Values("k", k), ursus.Values("v", v))
	dir := filepath.Join(t.TempDir(), "out")
	if err := lf.SinkParquet(ctx, dir, ursus.WithPartitionBy("k"), ursus.WithBatchSize(100)); err != nil {
		t.Fatal(err)
	}
	got := files(t, dir)
	for _, f := range got {
		if strings.HasPrefix(filepath.Base(f), ".") {
			t.Fatalf("a temporary file was left: %s", f)
		}
	}
	if !slices.Contains(got, "k=0/00000001.parquet") {
		t.Errorf("key 0's file was never closed and begun again: %d files", len(got))
	}
	back, err := ursus.ScanParquet(dir).Select(ursus.Col("k"), ursus.Col("v")).Sort(ursus.Asc(ursus.Col("v"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expect, _ := lf.Collect(ctx)
	samePartFrame(t, "150 keys read back", back, expect)
}

// TestPartitionedSinkRefuses: a key no column has, a key named twice, a List key,
// every column a key and none kept, a partition given to a writer of one stream; a
// query that fails leaves no temporary file; an empty answer writes no file. Polars
// 2.1 writes under a key named twice and with no column kept, and panics on a List
// key; the refusals are ursus's.
func TestPartitionedSinkRefuses(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("k", []int64{1, 2}), ursus.Values("v", []int64{1, 2}))
	for name, try := range map[string]func(dir string) error{
		"an unknown key": func(dir string) error { return lf.SinkParquet(ctx, dir, ursus.WithPartitionBy("x")) },
		"a key twice":    func(dir string) error { return lf.SinkParquet(ctx, dir, ursus.WithPartitionBy("k", "k")) },
		"a List key": func(dir string) error {
			return lf.Select(c("k").Implode(), c("v").Implode()).SinkParquet(ctx, dir, ursus.WithPartitionBy("k"))
		},
		"no column left": func(dir string) error {
			return lf.SinkParquet(ctx, dir, ursus.WithPartitionBy("k", "v"), ursus.WithPartitionIncludeKey(false))
		},
		"a stream": func(dir string) error {
			return lf.WriteParquet(ctx, &strings.Builder{}, ursus.WithPartitionBy("k"))
		},
		"a negative row count": func(dir string) error { return lf.SinkParquet(ctx, dir, ursus.WithMaxRowsPerFile(-1)) },
	} {
		if err := try(filepath.Join(t.TempDir(), "out")); err == nil {
			t.Errorf("%s was written", name)
		}
	}

	// The cast fails in the second batch, after the first's files were begun.
	bad := ursus.Frame(ursus.Values("k", []int64{1, 2, 1, 2}), ursus.Values("s", []string{"1", "2", "x", "4"})).
		Select(c("k"), c("s").Cast(ursus.Int64))
	dir := filepath.Join(t.TempDir(), "out")
	if err := bad.SinkParquet(ctx, dir, ursus.WithPartitionBy("k"), ursus.WithBatchSize(2)); err == nil {
		t.Fatal("a failed cast was written")
	}
	for _, f := range files(t, dir) {
		if strings.HasPrefix(filepath.Base(f), ".") {
			t.Errorf("a failed write left %s", f)
		}
	}

	empty := filepath.Join(t.TempDir(), "empty")
	if err := lf.Filter(c("k").Gt(5)).SinkParquet(ctx, empty, ursus.WithPartitionBy("k")); err != nil {
		t.Fatal(err)
	}
	if got := files(t, empty); len(got) != 0 {
		t.Errorf("an empty answer wrote %q", got)
	}
}
