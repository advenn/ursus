package ursus_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestScanIPCFiles is step 360: Polars' scan_ipc of several files, a glob, a list and
// a hive directory, and include_file_paths. Each answer is Polars 2.1.0's since step
// 502, recorded in testdata/oracle/ipcfiles.json and spelled in oracle_ipcfiles_test.go;
// here stay the serial and the blocked reading paths compared with each other, and a
// filter on a key ruling a file out before it is opened.
func TestScanIPCFiles(t *testing.T) {
	ctx, c := t.Context(), ursus.Col
	dir := t.TempDir()
	write := func(rel string, lf *ursus.LazyFrame) string {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := lf.WriteIPC(ctx, f); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ab := func(a []int64, b []string) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("a", a), ursus.Values("b", b))
	}
	p1 := write("p1.arrow", ab([]int64{1, 2}, []string{"x", "y"}))
	p2 := write("p2.arrow", ab([]int64{3}, []string{"z"}))
	p3 := write("p3.arrow", ursus.Frame(ursus.Values("b", []string{"w"}), ursus.Values("a", []int64{4})))
	d := filepath.Join(dir, "d")
	write("d/k=1/j=a/x.arrow", ursus.Frame(ursus.Values("a", []int64{1})))
	write("d/k=2/j=b/y.arrow", ursus.Frame(ursus.Values("a", []int64{2})))

	// One thread reads the files in turn, four at once.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"a glob", ursus.ScanIPCGlob(filepath.Join(dir, "p[12].arrow"))},
		{"a list, by name", ursus.ScanIPCFiles([]string{p2, p1, p3})},
		{"a directory's keys and paths", ursus.ScanIPC(d, ursus.WithIPCIncludeFilePaths("p"))},
		{"a filter on a key", ursus.ScanIPC(d).Filter(c("k").Eq(int64(2)))},
	} {
		one, err := tc.lf.Collect(ctx, ursus.WithThreads(1))
		if err != nil {
			t.Fatalf("%s, 1 thread: %v", tc.name, err)
		}
		four, err := tc.lf.Collect(ctx, ursus.WithThreads(4))
		if err != nil {
			t.Fatalf("%s, 4 threads: %v", tc.name, err)
		}
		ursustest.AssertFrameEqual(t, four, one)
	}

	// A filter on a key rules a file out before it is opened: a file that is no IPC
	// file, in a key's directory the filter rules out, is not read.
	e := filepath.Join(dir, "e")
	write("e/k=1/x.arrow", ursus.Frame(ursus.Values("a", []int64{1})))
	write("e/k=2/y.arrow", ursus.Frame(ursus.Values("a", []int64{2})))
	if err := os.MkdirAll(filepath.Join(e, "k=3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e, "k=3", "z.arrow"), []byte("not an IPC file"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, th := range []int{1, 4} {
		df, err := ursus.ScanIPC(e).Filter(c("k").Lt(int64(3))).Collect(ctx, ursus.WithThreads(th))
		if err != nil || df.Height() != 2 {
			t.Errorf("a file ruled out, %d threads: %v, %v", th, df, err)
		}
		if _, err := ursus.ScanIPC(e).Collect(ctx, ursus.WithThreads(th)); err == nil {
			t.Errorf("%d threads: the file not ruled out was read", th)
		}
	}
}
