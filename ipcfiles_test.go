package ursus_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestScanIPCFiles is step 360: Polars' scan_ipc of several files, a glob, a list and
// a hive directory, and include_file_paths. Every answer is Polars 1.44.1's.
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
	p4 := write("p4.arrow", ursus.Frame(ursus.Values("a", []int64{5})))
	p5 := write("p5.arrow", ursus.Frame(ursus.Values("a", []float64{1.5}), ursus.Values("b", []string{"q"})))
	d := filepath.Join(dir, "d")
	x := write("d/k=1/j=a/x.arrow", ursus.Frame(ursus.Values("a", []int64{1})))
	y := write("d/k=2/j=b/y.arrow", ursus.Frame(ursus.Values("a", []int64{2})))

	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		col  string
		want string
	}{
		{"a glob", ursus.ScanIPCGlob(filepath.Join(dir, "p[12].arrow")), "b", "x y z String"},
		{"a list", ursus.ScanIPCFiles([]string{p2, p1}), "a", "3 1 2 Int64"},
		{"by name", ursus.ScanIPCFiles([]string{p1, p3}), "b", "x y w String"},
		{"by name, the other", ursus.ScanIPCFiles([]string{p1, p3}), "a", "1 2 4 Int64"},
		{"a directory's keys", ursus.ScanIPC(d), "k", "1 2 Int64"},
		{"its second key", ursus.ScanIPC(d), "j", "a b String"},
		{"its columns", ursus.ScanIPC(d), "a", "1 2 Int64"},
		{"a glob, no keys", ursus.ScanIPCGlob(filepath.Join(d, "**", "*.arrow")), "a", "1 2 Int64"},
		{"a glob with keys", ursus.ScanIPCGlob(filepath.Join(d, "**", "*.arrow"), ursus.WithIPCHivePartitioning(true)), "k", "1 2 Int64"},
		{"the paths", ursus.ScanIPC(d, ursus.WithIPCIncludeFilePaths("p")), "p", x + " " + y + " String"},
		{"a filter on a key", ursus.ScanIPC(d).Filter(c("k").Eq(int64(2))), "a", "2 Int64"},
		{"a key alone", ursus.ScanIPC(d).Select(c("k")), "k", "1 2 Int64"},
		{"a key's type given", ursus.ScanIPC(d, ursus.WithIPCHiveSchema(ursus.MustSchema(ursus.Of("k", ursus.Int8)))), "k", "1 2 Int8"},
		{"under a limit", ursus.ScanIPCGlob(filepath.Join(dir, "p[12].arrow")).Head(2), "a", "1 2 Int64"},
		{"one file's path", ursus.ScanIPC(p1, ursus.WithIPCIncludeFilePaths("p")), "p", p1 + " " + p1 + " String"},
	} {
		for _, th := range []int{1, 4} {
			if got := cellsOn(t, tc.lf, tc.col, th); got != tc.want {
				t.Errorf("%s, %d threads: %q, want %q", tc.name, th, got, tc.want)
			}
		}
	}
	if s, err := ursus.ScanIPC(d, ursus.WithIPCIncludeFilePaths("p")).CollectSchema(ctx); err != nil || strings.Join(s.Names(), ",") != "a,k,j,p" {
		t.Errorf("the directory's schema: %v, %v", s, err)
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
		if got := cellsOn(t, ursus.ScanIPC(e).Filter(c("k").Lt(int64(3))), "a", th); got != "1 2 Int64" {
			t.Errorf("a file ruled out, %d threads: %s", th, got)
		}
		if _, err := ursus.ScanIPC(e).Collect(ctx, ursus.WithThreads(th)); err == nil {
			t.Errorf("%d threads: the file not ruled out was read", th)
		}
	}

	// Refused, as Polars refuses them.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		says string
	}{
		// "did not find column b"
		{"a column missing", ursus.ScanIPCFiles([]string{p1, p4}), `no column "b"`},
		// "extra column in file outside of expected schema: b"
		{"a column more", ursus.ScanIPCFiles([]string{p4, p1}), `a column "b"`},
		// "data type mismatch for column a: incoming: Float64 != target: Int64"
		{"another type", ursus.ScanIPCFiles([]string{p1, p5}), "Float64"},
		// "column name for file paths "a" conflicts with column name from file"
		{"a path column's name taken", ursus.ScanIPC(d, ursus.WithIPCIncludeFilePaths("k")), "file paths"},
	} {
		if _, err := tc.lf.Collect(ctx); !errors.Is(err, ursus.ErrSchema) || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.says)
		}
	}
}
