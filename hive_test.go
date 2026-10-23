package ursus_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/parquet"
)

// hivePart writes lf as a Parquet file at root/rel, its directories made.
func hivePart(t *testing.T, root, rel string, lf *ursus.LazyFrame) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := lf.SinkParquet(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	return path
}

// rowsOf is each row of q as text, columns apart by a comma, rows by a space, and its
// schema after them.
func rowsOf(t *testing.T, q *ursus.LazyFrame) string {
	t.Helper()
	df, err := q.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := df.ToMaps()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		var cells []string
		for _, name := range df.Columns() {
			cells = append(cells, fmt.Sprint(r[name]))
		}
		out = append(out, strings.Join(cells, ","))
	}
	return strings.Join(out, " ") + " " + df.Schema().String()
}

// TestHivePartitions is step 347: a directory of Parquet files read as one frame,
// each key=value directory a column of the files beneath it, as Polars' scan_parquet
// reads a hive dataset; and each file's path as a column. Every answer is Polars
// 1.44.1's but where noted.
func TestHivePartitions(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	dir := t.TempDir()
	v := func(vs ...int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("v", vs)) }
	root := filepath.Join(dir, "t")
	hivePart(t, root, "a=1/b=x/p0.parquet", v(1, 2))
	hivePart(t, root, "a=2/b=y%20z/p0.parquet", v(3))
	hivePart(t, root, "a=__HIVE_DEFAULT_PARTITION__/b=w/p0.parquet", v(4))
	byV := func(q *ursus.LazyFrame) *ursus.LazyFrame { return q.Sort(ursus.Asc(c("v"))) }

	t.Run("a directory", func(t *testing.T) {
		// The keys after the files' columns, typed by their values, a value
		// percent-decoded, Hive's default partition null.
		if got := rowsOf(t, byV(ursus.ScanParquet(root))); got != "1,1,x 2,1,x 3,2,y z 4,<nil>,w {v: Int64, a: Int64, b: String!}" {
			t.Errorf("%s", got)
		}
		// Only the keys, and a count.
		if got := rowsOf(t, ursus.ScanParquet(root).Select(c("a"), c("b")).Sort(ursus.Asc(c("b")))); got != "<nil>,w 1,x 1,x 2,y z {a: Int64, b: String!}" {
			t.Errorf("the keys alone: %s", got)
		}
		if got := rowsOf(t, ursus.ScanParquet(root).Select(ursus.Len())); got != "4 {len: Uint64!}" {
			t.Errorf("a count: %s", got)
		}
	})

	t.Run("hive schema and dates", func(t *testing.T) {
		if got := schemaOf(t, ursus.ScanParquet(root, ursus.WithHiveSchema(mustSchema(ursus.Field{Name: "a", Type: ursus.Int8})))); got != "{v: Int64, a: Int8, b: String!}" {
			t.Errorf("a given type: %s", got)
		}
		days := filepath.Join(dir, "days")
		hivePart(t, days, "d=2024-01-02/p.parquet", v(1))
		hivePart(t, days, "d=2024-01-03/p.parquet", v(2))
		if got := rowsOf(t, byV(ursus.ScanParquet(days))); !strings.HasSuffix(got, "{v: Int64, d: Date!}") {
			t.Errorf("dates: %s", got)
		}
		if got := schemaOf(t, ursus.ScanParquet(days, ursus.WithHiveDates(false))); got != "{v: Int64, d: String!}" {
			t.Errorf("dates as text: %s", got)
		}
	})

	t.Run("a filter on the keys skips files", func(t *testing.T) {
		for _, threads := range []int{1, 8} {
			lf := ursus.ScanParquet(root)
			src := lf.Plan().(*plan.Scan).Src.(*parquet.Source)
			q := lf.Filter(c("a").Eq(int64(2)))
			df, err := q.Collect(ctx, ursus.WithThreads(threads))
			if err != nil {
				t.Fatal(err)
			}
			if df.Height() != 1 {
				t.Errorf("%d threads: %d rows", threads, df.Height())
			}
			if read, skipped := src.FileStats(); read != 1 || skipped != 2 {
				t.Errorf("%d threads: %d files read, %d skipped; want 1 and 2", threads, read, skipped)
			}
		}
		// A key and a file's column at once; a key's text tested; a null key.
		if got := rowsOf(t, byV(ursus.ScanParquet(root).Filter(c("a").Eq(int64(1)).And(c("v").Gt(int64(1)))))); got != "2,1,x {v: Int64, a: Int64, b: String!}" {
			t.Errorf("a key and a column: %s", got)
		}
		lf := ursus.ScanParquet(root)
		src := lf.Plan().(*plan.Scan).Src.(*parquet.Source)
		if got := rowsOf(t, lf.Filter(c("b").Str().StartsWith("y"))); got != "3,2,y z {v: Int64, a: Int64, b: String!}" {
			t.Errorf("a key's text: %s", got)
		}
		if read, skipped := src.FileStats(); read != 1 || skipped != 2 {
			t.Errorf("a key's text: %d files read, %d skipped", read, skipped)
		}
		// A conjunct of keys the statistics' pruner reads no part of.
		lf = ursus.ScanParquet(root)
		src = lf.Plan().(*plan.Scan).Src.(*parquet.Source)
		if got := rowsOf(t, lf.Filter(c("a").Add(int64(1)).Eq(int64(3)))); got != "3,2,y z {v: Int64, a: Int64, b: String!}" {
			t.Errorf("a + 1 == 3: %s", got)
		}
		if read, skipped := src.FileStats(); read != 1 || skipped != 2 {
			t.Errorf("a + 1 == 3: %d files read, %d skipped", read, skipped)
		}
		if got := rowsOf(t, ursus.ScanParquet(root).Filter(c("a").IsNull())); got != "4,<nil>,w {v: Int64, a: Int64, b: String!}" {
			t.Errorf("a null key: %s", got)
		}
	})

	t.Run("file paths", func(t *testing.T) {
		got := rowsOf(t, byV(ursus.ScanParquet(root, ursus.WithIncludeFilePaths("path")).Select(c("v"), c("path"))))
		want := []string{"1," + filepath.Join(root, "a=1", "b=x", "p0.parquet"), "2," + filepath.Join(root, "a=1", "b=x", "p0.parquet"),
			"3," + filepath.Join(root, "a=2", "b=y%20z", "p0.parquet")}
		if !strings.HasPrefix(got, strings.Join(want, " ")) {
			t.Errorf("%s", got)
		}
		if s := schemaOf(t, ursus.ScanParquet(root, ursus.WithIncludeFilePaths("path"))); s != "{v: Int64, a: Int64, b: String!, path: String!}" {
			t.Errorf("the path last: %s", s)
		}
		// Polars: "column name for file paths "a" conflicts with column name from file".
		if _, err := ursus.ScanParquet(root, ursus.WithIncludeFilePaths("a")).Collect(ctx); err == nil {
			t.Error("a path column named as a key")
		}
	})

	t.Run("a key the files hold", func(t *testing.T) {
		// Polars: the key's value replaces the file's, in the file's place and type.
		x := filepath.Join(dir, "x")
		hivePart(t, x, "a=1/p0.parquet", ursus.Frame(i64s("a", 9), i64s("v", 1)))
		if got := rowsOf(t, ursus.ScanParquet(x)); got != "1,1 {a: Int64!, v: Int64}" {
			t.Errorf("%s", got)
		}
		bad := filepath.Join(dir, "bad")
		hivePart(t, bad, "a=x/p0.parquet", ursus.Frame(i64s("a", 9), i64s("v", 1)))
		if _, err := ursus.ScanParquet(bad).Collect(ctx); err == nil {
			t.Error("a=x read as the files' Int64")
		}
	})

	t.Run("globs, files and lists", func(t *testing.T) {
		// Off by default; on, every directory of the whole path counts.
		glob := filepath.Join(root, "*", "*", "*.parquet")
		if got := schemaOf(t, ursus.ScanParquetGlob(glob)); got != "{v: Int64}" {
			t.Errorf("a glob: %s", got)
		}
		if got := rowsOf(t, byV(ursus.ScanParquetGlob(glob, ursus.WithHivePartitioning(true)))); got != "1,1,x 2,1,x 3,2,y z 4,<nil>,w {v: Int64, a: Int64, b: String!}" {
			t.Errorf("a glob, hive: %s", got)
		}
		one := filepath.Join(root, "a=1", "b=x", "p0.parquet")
		if got := schemaOf(t, ursus.ScanParquet(one)); got != "{v: Int64}" {
			t.Errorf("a file: %s", got)
		}
		if got := schemaOf(t, ursus.ScanParquet(one, ursus.WithHivePartitioning(true))); got != "{v: Int64, a: Int64!, b: String!}" {
			t.Errorf("a file, hive: %s", got)
		}
		if got := schemaOf(t, ursus.ScanParquet(root, ursus.WithHivePartitioning(false))); got != "{v: Int64}" {
			t.Errorf("a directory, no hive: %s", got)
		}
	})

	t.Run("what a directory holds", func(t *testing.T) {
		// A directory not key=value passes over; _SUCCESS, a .crc file and a
		// hidden directory are passed over, where Polars reads the last two
		// (feature-gaps §9); the root's own name is no key.
		d := filepath.Join(dir, "k=0", "m")
		hivePart(t, d, "x/a=1/p0.parquet", v(1))
		writeText(t, filepath.Join(d, "x", "a=1"), "_SUCCESS", "")
		writeText(t, filepath.Join(d, "x", "a=1"), ".p0.parquet.crc", "junk")
		hivePart(t, d, ".tmp/a=2/p0.parquet", v(2))
		if got := rowsOf(t, ursus.ScanParquet(d)); got != "1,1 {v: Int64, a: Int64!}" {
			t.Errorf("%s", got)
		}
		// Refused: keys apart, depths apart, no file.
		for name, files := range map[string][]string{
			"keys apart":   {"a=1/p.parquet", "b=2/p.parquet"},
			"depths apart": {"a=1/b=2/p.parquet", "a=3/p.parquet"},
		} {
			r := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
			for _, f := range files {
				hivePart(t, r, f, v(1))
			}
			if _, err := ursus.ScanParquet(r).Collect(ctx); err == nil || !strings.Contains(err.Error(), "partitioned by") {
				t.Errorf("%s: %v", name, err)
			}
		}
		empty := filepath.Join(dir, "empty")
		if err := os.Mkdir(empty, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := ursus.ScanParquet(empty).Collect(ctx); err == nil || !strings.Contains(err.Error(), "no files") {
			t.Errorf("an empty directory: %v", err)
		}
	})

	t.Run("many files", func(t *testing.T) {
		// Forty files read on eight threads, as their Concat is.
		m := filepath.Join(dir, "many")
		var parts []*ursus.LazyFrame
		for k := range 40 {
			part := ursus.Frame(ursus.Values("v", []int64{int64(k), int64(k + 100)}))
			hivePart(t, m, fmt.Sprintf("g=%d/h=%c/p%d.parquet", k%4, 'a'+k%5, k), part)
			parts = append(parts, part.WithColumns(ursus.Lit(int64(k%4)).Alias("g"), ursus.Lit(string(rune('a'+k%5))).Alias("h")))
		}
		want := collect(t, ursus.Concat(parts).Sort(ursus.Asc(c("v"))))
		for _, threads := range []int{1, 8} {
			got, err := ursus.ScanParquet(m).Sort(ursus.Asc(c("v"))).Collect(ctx, ursus.WithThreads(threads))
			if err != nil {
				t.Fatal(err)
			}
			if same, err := got.Equals(ctx, want, true); err != nil || !same {
				t.Errorf("%d threads: %v\n%v", threads, got, want)
			}
		}
	})
}

func schemaOf(t *testing.T, lf *ursus.LazyFrame) string {
	t.Helper()
	s, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return s.String()
}
