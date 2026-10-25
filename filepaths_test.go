package ursus_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestIncludeFilePaths is step 359: Polars' include_file_paths for CSV and NDJSON, a
// String column of each row's file path after the files' columns, the path as given.
// Every answer is Polars 1.44.1's.
func TestIncludeFilePaths(t *testing.T) {
	ctx, c := t.Context(), ursus.Col
	dir := t.TempDir()
	p1, p2 := writeText(t, dir, "p1.csv", "a,b\n1,x\n2,y\n"), writeText(t, dir, "p2.csv", "a,b\n3,z\n")
	n1, n2 := writeText(t, dir, "p1.ndjson", "{\"a\":1}\n{\"a\":2}\n"), writeText(t, dir, "p2.ndjson", "{\"a\":3}\n")
	csvPaths := ursus.WithCSVIncludeFilePaths("path")
	jsonPaths := ursus.WithNDJSONIncludeFilePaths("path")
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		col  string
		want string
	}{
		{"a glob", ursus.ScanCSVGlob(filepath.Join(dir, "p*.csv"), csvPaths), "path", p1 + " " + p1 + " " + p2 + " String"},
		{"its columns", ursus.ScanCSVGlob(filepath.Join(dir, "p*.csv"), csvPaths), "b", "x y z String"},
		{"a list as given", ursus.ScanCSVFiles([]string{p1, dir + "/./p2.csv"}, csvPaths), "path", p1 + " " + p1 + " " + dir + "/./p2.csv String"},
		{"one file", ursus.ScanCSV(p1, csvPaths), "path", p1 + " " + p1 + " String"},
		{"the path alone", ursus.ScanCSVGlob(filepath.Join(dir, "p*.csv"), csvPaths).Select(c("path")), "path", p1 + " " + p1 + " " + p2 + " String"},
		{"a filter on it", ursus.ScanCSVGlob(filepath.Join(dir, "p*.csv"), csvPaths).Filter(c("path").Eq(p2)), "a", "3 Int64"},
		{"under a limit", ursus.ScanCSVGlob(filepath.Join(dir, "p*.csv"), csvPaths).Head(2), "path", p1 + " " + p1 + " String"},
		{"bytes, their name", ursus.ScanCSVReader([]byte("a\n1\n"), "mem.csv", csvPaths), "path", "mem.csv String"},
		{"a schema given", ursus.ScanCSV(p1, csvPaths, ursus.WithSchema(ursus.MustSchema(ursus.Of("a", ursus.Int64), ursus.Of("b", ursus.String)))), "path", p1 + " " + p1 + " String"},
		{"NDJSON's glob", ursus.ScanNDJSONGlob(filepath.Join(dir, "p*.ndjson"), jsonPaths), "path", n1 + " " + n1 + " " + n2 + " String"},
		{"NDJSON's columns", ursus.ScanNDJSONGlob(filepath.Join(dir, "p*.ndjson"), jsonPaths), "a", "1 2 3 Int64"},
		{"NDJSON's path alone", ursus.ScanNDJSONFiles([]string{n2, n1}, jsonPaths).Select(c("path")), "path", n2 + " " + n1 + " " + n1 + " String"},
		{"NDJSON's schema given", ursus.ScanNDJSON(n1, jsonPaths, ursus.WithNDJSONSchema(ursus.MustSchema(ursus.Of("a", ursus.Int32)))), "path", n1 + " " + n1 + " String"},
		{"NDJSON's bytes", ursus.ScanNDJSONBytes([]byte("{\"a\":1}\n"), "mem.ndjson", jsonPaths), "path", "mem.ndjson String"},
	} {
		for _, th := range []int{1, 4} {
			if got := cellsOn(t, tc.lf, tc.col, th); got != tc.want {
				t.Errorf("%s, %d threads: %q, want %q", tc.name, th, got, tc.want)
			}
		}
	}
	// The schema: the path column last.
	if s, err := ursus.ScanCSVGlob(filepath.Join(dir, "p*.csv"), csvPaths).CollectSchema(ctx); err != nil || strings.Join(s.Names(), ",") != "a,b,path" {
		t.Errorf("the schema: %v, %v", s, err)
	}

	// Files of many rows, cut into blocks on four threads: each row's path is its
	// file's, a count a file.
	var big []string
	for i := range 3 {
		var b strings.Builder
		b.WriteString("a,b\n")
		for j := range 20_000 * (i + 1) {
			fmt.Fprintf(&b, "%d,\"t\n%d\"\n", j, i)
		}
		big = append(big, writeText(t, dir, fmt.Sprintf("big%d.csv", i), b.String()))
	}
	for _, th := range []int{1, 4} {
		df, err := ursus.ScanCSVFiles(big, csvPaths).GroupBy(c("path")).Agg(ursus.Len().Alias("n"), c("b").Str().Slice(2, 1).Unique().Alias("i")).
			Sort(ursus.Asc(c("path"))).Collect(ctx, ursus.WithThreads(th))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(df.ToMaps()); got != fmt.Sprintf("[map[i:[0] n:20000 path:%s] map[i:[1] n:40000 path:%s] map[i:[2] n:60000 path:%s]] <nil>", big[0], big[1], big[2]) {
			t.Errorf("%d threads: %s", th, got)
		}
	}

	// The path column is no file's: a record of a field more is refused still, and a
	// JSON document's key of its name past the inferred lines is a key the schema has
	// not, refused as one.
	for _, lf := range []*ursus.LazyFrame{
		ursus.ScanCSVReader([]byte("a,b\n1,2\n3,4,5\n"), "m.csv", csvPaths),
		ursus.ScanJSONBytes([]byte(`[{"a":1},{"a":2,"path":3}]`), "m.json", ursus.WithInferSchemaLines(1), jsonPaths),
	} {
		if _, err := lf.Collect(ctx); !errors.Is(err, ursus.ErrValue) && !errors.Is(err, ursus.ErrSchema) {
			t.Errorf("a field past the files': %v", err)
		}
	}

	// A name a column of the files has already is refused, as Polars refuses it:
	// "column name for file paths "a" conflicts with column name from file".
	for _, lf := range []*ursus.LazyFrame{
		ursus.ScanCSV(p1, ursus.WithCSVIncludeFilePaths("a")),
		ursus.ScanNDJSON(n1, ursus.WithNDJSONIncludeFilePaths("a")),
	} {
		if _, err := lf.Collect(ctx); !errors.Is(err, ursus.ErrSchema) || !strings.Contains(err.Error(), "file paths") {
			t.Errorf("a name taken: %v", err)
		}
	}
}
