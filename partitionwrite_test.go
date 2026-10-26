package ursus_test

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// TestPartitionedSinkWritesAHiveDataset is step 367's: a Parquet dataset by two keys,
// each key's directory and file named as Polars 1.44.1 names them, its values escaped
// where Polars escapes them, a null the default partition and an empty text k=; the
// keys kept in each file, or not; read back by ScanParquet as the frame it was.
func TestPartitionedSinkWritesAHiveDataset(t *testing.T) {
	ctx := t.Context()
	keys := []string{"a/b", "c=d", "e f", "g%h", "ü", ":x", "", "", "#?&+", "a/b"}
	ok := []bool{true, true, true, true, true, true, true, false, true, true}
	lf := ursus.Frame(
		ursus.ValuesNullable("k", keys, ok),
		ursus.Values("j", []int64{1, 1, 2, 2, 1, 1, 2, 2, 1, 2}),
		ursus.Values("v", []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}),
	)
	dir := filepath.Join(t.TempDir(), "out")
	if err := lf.SinkParquet(ctx, dir, ursus.WithPartitionBy("k", "j")); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"k=#?&+/j=1/00000000.parquet",
		"k=%3Ax/j=1/00000000.parquet",
		"k=%C3%BC/j=1/00000000.parquet",
		"k=/j=2/00000000.parquet",
		"k=__HIVE_DEFAULT_PARTITION__/j=2/00000000.parquet",
		"k=a%2Fb/j=1/00000000.parquet",
		"k=a%2Fb/j=2/00000000.parquet",
		"k=c%3Dd/j=1/00000000.parquet",
		"k=e%20f/j=2/00000000.parquet",
		"k=g%25h/j=2/00000000.parquet",
	}
	if got := files(t, dir); !slices.Equal(got, want) {
		t.Fatalf("files\n %q\nwant\n %q", got, want)
	}
	expect, err := lf.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ursus.ScanParquet(dir).Sort(ursus.Asc(ursus.Col("v"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	samePartFrame(t, "the dataset read back", back, expect)

	// A file of its own holds its keys, or does not.
	one, err := ursus.ScanParquet(filepath.Join(dir, "k=a%2Fb", "j=2", "00000000.parquet")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(one.Columns(), ","); got != "k,j,v" {
		t.Errorf("a file's columns %s", got)
	}
	// A key's name escaped as a value is, where Polars writes it as it is and a slash
	// in it makes a directory, and read back.
	named := filepath.Join(t.TempDir(), "named")
	odd := ursus.Frame(ursus.Values("a b/c", []int64{1}), ursus.Values("v", []int64{2}))
	if err := odd.SinkParquet(ctx, named, ursus.WithPartitionBy("a b/c"), ursus.WithPartitionIncludeKey(false)); err != nil {
		t.Fatal(err)
	}
	if got := files(t, named); !slices.Equal(got, []string{"a%20b%2Fc=1/00000000.parquet"}) {
		t.Errorf("a key's name spelled %q", got)
	}
	if df, err := ursus.ScanParquet(named).Collect(ctx); err != nil || !slices.Equal(df.Columns(), []string{"v", "a b/c"}) {
		t.Errorf("a key's name read back: %v, %v", df, err)
	}
	bare := filepath.Join(t.TempDir(), "bare")
	if err := lf.SinkParquet(ctx, bare, ursus.WithPartitionBy("k", "j"), ursus.WithPartitionIncludeKey(false)); err != nil {
		t.Fatal(err)
	}
	one, err = ursus.ScanParquet(filepath.Join(bare, "k=a%2Fb", "j=2", "00000000.parquet")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(one.Columns(), ","); got != "v" {
		t.Errorf("a file without its keys has %s", got)
	}
	back, err = ursus.ScanParquet(bare).Select(ursus.Col("k"), ursus.Col("j"), ursus.Col("v")).
		Sort(ursus.Asc(ursus.Col("v"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	samePartFrame(t, "the dataset without its keys read back", back, expect)
}

// TestPartitionedKeysOfEveryKind: a date, an instant with no zone and one with a
// zone, a float, a Boolean and a decimal as Polars spells them in a path.
func TestPartitionedKeysOfEveryKind(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	ts := time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC)
	lf := ursus.Frame(ursus.Values("ts", []time.Time{ts}), ursus.Values("f", []float64{math.Copysign(0, -1)}),
		ursus.Values("v", []int64{1})).Select(
		c("ts").Dt().Date().Alias("d"),
		c("ts").Dt().ReplaceTimeZone("").Cast(ursus.Datetime(ursus.Micro, "")).Alias("naive"),
		c("ts").Cast(ursus.Datetime(ursus.Micro, "UTC")).Dt().ConvertTimeZone("Asia/Tokyo").Alias("tz"),
		c("ts").Cast(ursus.Datetime(ursus.Nano, "")).Dt().ReplaceTimeZone("").Alias("ns"),
		c("f"), ursus.Lit(true).Alias("b"), ursus.Lit(1.5).Cast(ursus.Decimal(5, 2)).Alias("dec"),
		c("ts").Dt().Time().Alias("tm"), c("v"))
	for key, want := range map[string]string{
		"d":     "d=2024-01-02/00000000.parquet",
		"naive": "naive=2024-01-02%2003%3A04%3A05.123456/00000000.parquet",
		"tz":    "tz=2024-01-02%2012%3A04%3A05.123456+09%3A00/00000000.parquet",
		"ns":    "ns=2024-01-02%2003%3A04%3A05.123456000/00000000.parquet",
		"f":     "f=-0.0/00000000.parquet",
		"b":     "b=true/00000000.parquet",
		"dec":   "dec=1.50/00000000.parquet",
		"tm":    "tm=03%3A04%3A05.123456/00000000.parquet",
	} {
		dir := filepath.Join(t.TempDir(), key)
		if err := lf.SinkParquet(ctx, dir, ursus.WithPartitionBy(key)); err != nil {
			t.Fatal(err)
		}
		if got := files(t, dir); !slices.Equal(got, []string{want}) {
			t.Errorf("%s: %q, want %s", key, got, want)
		}
	}
}

// TestPartitionedFilesOfAtMostSoManyRows: a key's rows in their order across files of
// at most n rows, numbered on; with no key, numbered files in the directory itself.
func TestPartitionedFilesOfAtMostSoManyRows(t *testing.T) {
	ctx := t.Context()
	lf := ursus.Frame(ursus.Values("k", []int64{1, 2, 1, 1, 1, 1, 2}), ursus.Values("v", []int64{0, 1, 2, 3, 4, 5, 6}))
	dir := filepath.Join(t.TempDir(), "out")
	if err := lf.SinkParquet(ctx, dir, ursus.WithPartitionBy("k"), ursus.WithMaxRowsPerFile(2),
		ursus.WithBatchSize(3)); err != nil {
		t.Fatal(err)
	}
	want := []string{"k=1/00000000.parquet", "k=1/00000001.parquet", "k=1/00000002.parquet",
		"k=2/00000000.parquet"}
	if got := files(t, dir); !slices.Equal(got, want) {
		t.Fatalf("files %q, want %q", got, want)
	}
	for f, rows := range map[string][]int64{"k=1/00000000.parquet": {0, 2}, "k=1/00000001.parquet": {3, 4},
		"k=1/00000002.parquet": {5}, "k=2/00000000.parquet": {1, 6}} {
		df, err := ursus.ScanParquet(filepath.Join(dir, f)).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		col, _ := df.Column[int64]("v")
		if got := col.Values(); !slices.Equal(got, rows) {
			t.Errorf("%s holds %v, want %v", f, got, rows)
		}
	}
	flat := filepath.Join(t.TempDir(), "flat")
	if err := lf.SinkParquet(ctx, flat, ursus.WithMaxRowsPerFile(3)); err != nil {
		t.Fatal(err)
	}
	if got := files(t, flat); !slices.Equal(got, []string{"00000000.parquet", "00000001.parquet", "00000002.parquet"}) {
		t.Errorf("files without a key %q", got)
	}
}

// TestPartitionedSinkOfEachFormat: CSV, IPC and NDJSON datasets named as Polars names
// them, .csv, .ipc and .jsonl, each read back as the frame it was.
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
		if got, want := files(t, dir), []string{"k=a/00000000" + f.ext, "k=b/00000000" + f.ext}; !slices.Equal(got, want) {
			t.Errorf("%s: files %q, want %q", f.ext, got, want)
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
// query that fails leaves no temporary file; an empty answer writes no file.
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
	if _, err := os.Stat(empty); err != nil {
		t.Errorf("an empty answer made no directory: %v", err)
	}
}
