package ursus_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestIncludeFilePaths is step 359: Polars' include_file_paths for CSV and NDJSON, a
// String column of each row's file path after the files' columns, the path as given.
// Each answer is Polars 2.1.0's since step 502, recorded in testdata/oracle/
// filepaths.json and spelled in oracle_filepaths_test.go; here stay bytes read under a
// name of the caller's, the big files cut into blocks on four threads, and the path
// column being no file's.
func TestIncludeFilePaths(t *testing.T) {
	ctx, c := t.Context(), ursus.Col
	dir := t.TempDir()
	writeText(t, dir, "p1.csv", "a,b\n1,x\n2,y\n")
	writeText(t, dir, "p2.csv", "a,b\n3,z\n")
	csvPaths := ursus.WithCSVIncludeFilePaths("path")
	jsonPaths := ursus.WithNDJSONIncludeFilePaths("path")

	// Bytes read under a name: the name is the caller's here, "in-mem" in Polars.
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		want string
	}{
		{"bytes, their name", ursus.ScanCSVReader([]byte("a\n1\n"), "mem.csv", csvPaths), "mem.csv String"},
		{"NDJSON's bytes", ursus.ScanNDJSONBytes([]byte("{\"a\":1}\n"), "mem.ndjson", jsonPaths), "mem.ndjson String"},
	} {
		if got := cells(t, tc.lf, "path"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// One thread reads the files in turn, four by blocks at once.
	glob := ursus.ScanCSVGlob(filepath.Join(dir, "p*.csv"), csvPaths)
	one, err := glob.Collect(ctx, ursus.WithThreads(1))
	if err != nil {
		t.Fatal(err)
	}
	four, err := glob.Collect(ctx, ursus.WithThreads(4))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, four, one)

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
}
