package ursus_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestGlobsAndDirectories is step 349: a glob's ** matches any number of
// directories, none among them, as Polars' globs do, in every scan's Glob; and a CSV
// or NDJSON directory is read as its files, as Polars reads one. Every answer is
// Polars 1.44.1's but where noted.
func TestGlobsAndDirectories(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	dir := t.TempDir()
	put := func(rel, text string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeText(t, filepath.Dir(p), filepath.Base(p), text)
	}
	put("top.csv", "v\n0\n")
	put("x/a.csv", "v\n1\n")
	put("x/y/b.csv", "v\n2\n")
	put("x/y/c.txt", "v\n3\n")
	vs := func(q *ursus.LazyFrame) string {
		t.Helper()
		return cells(t, q.Sort(ursus.Asc(c("v"))), "v")
	}
	for _, tc := range []struct{ pattern, want string }{
		{"**/*.csv", "0 1 2 Int64"},
		{"x/**/*.csv", "1 2 Int64"},
		{"x/**/b.csv", "2 Int64"},
		{"x*/**/*.csv", "1 2 Int64"},
		{"x/*/*.csv", "2 Int64"}, // filepath.Glob's, as before
	} {
		if got := vs(ursus.ScanCSVGlob(filepath.Join(dir, filepath.FromSlash(tc.pattern)))); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.pattern, got, tc.want)
		}
	}
	if _, err := ursus.ScanCSVGlob(filepath.Join(dir, "**", "*.json")).Collect(ctx); err == nil || !strings.Contains(err.Error(), "no files match") {
		t.Errorf("no match: %v", err)
	}
	if _, err := ursus.ScanCSVGlob(filepath.Join(dir, "**", "[")).Collect(ctx); err == nil {
		t.Error("a bad pattern read")
	}

	// A directory, as its files; one of two extensions refused. Polars:
	// "directory contained paths with different file extensions".
	put("d/a.csv", "v\n1\n")
	put("d/e/b.csv", "v\n2\n")
	if got := vs(ursus.ScanCSV(filepath.Join(dir, "d"))); got != "1 2 Int64" {
		t.Errorf("a CSV directory: %s", got)
	}
	if _, err := ursus.ScanCSV(filepath.Join(dir, "x")).Collect(ctx); err == nil || !strings.Contains(err.Error(), "two extensions") {
		t.Errorf("a CSV directory of two extensions: %v", err)
	}
	put("n/a.ndjson", "{\"v\":1}\n")
	put("n/b.ndjson", "{\"v\":2}\n")
	if got := vs(ursus.ScanNDJSON(filepath.Join(dir, "n"))); got != "1 2 Int64" {
		t.Errorf("an NDJSON directory: %s", got)
	}
	if got := vs(ursus.ScanNDJSONGlob(filepath.Join(dir, "**", "*.ndjson"))); got != "1 2 Int64" {
		t.Errorf("an NDJSON glob: %s", got)
	}

	// Parquet's glob and a directory of two extensions.
	p := filepath.Join(dir, "p")
	hivePart(t, p, "a=1/x.parquet", ursus.Frame(i64s("v", 1)))
	hivePart(t, p, "a=2/y/x.parquet", ursus.Frame(i64s("v", 2)))
	if got := vs(ursus.ScanParquetGlob(filepath.Join(p, "**", "*.parquet"))); got != "1 2 Int64" {
		t.Errorf("a Parquet glob: %s", got)
	}
	writeText(t, filepath.Join(p, "a=1"), "notes.txt", "x")
	if _, err := ursus.ScanParquet(p).Collect(ctx); err == nil || !strings.Contains(err.Error(), "two extensions") {
		t.Errorf("a Parquet directory of two extensions: %v", err)
	}
}
