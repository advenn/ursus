package ursus_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/parquet"
	"github.com/advenn/ursus/ursustest"
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
// reads a hive dataset; and each file's path as a column. Each answer is Polars
// 2.1.0's since step 502, recorded in testdata/oracle/hive.json and spelled in
// oracle_hive_test.go; here stay the files a filter skips, what a directory holds that
// is passed over (feature-gaps.md §9), and forty files on eight threads against their
// Concat.
func TestHivePartitions(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	dir := t.TempDir()
	v := func(vs ...int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("v", vs)) }
	root := filepath.Join(dir, "t")
	hivePart(t, root, "a=1/b=x/p0.parquet", v(1, 2))
	hivePart(t, root, "a=2/b=y%20z/p0.parquet", v(3))
	hivePart(t, root, "a=__HIVE_DEFAULT_PARTITION__/b=w/p0.parquet", v(4))

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
		// A key's text tested, and a conjunct of keys the statistics' pruner reads no
		// part of: each opens one file.
		for name, pred := range map[string]ursus.Expr{
			"a key's text": c("b").Str().StartsWith("y"),
			"a + 1 == 3":   c("a").Add(int64(1)).Eq(int64(3)),
		} {
			lf := ursus.ScanParquet(root)
			src := lf.Plan().(*plan.Scan).Src.(*parquet.Source)
			if df, err := lf.Filter(pred).Collect(ctx); err != nil || df.Height() != 1 {
				t.Errorf("%s: %v, %v", name, df, err)
			}
			if read, skipped := src.FileStats(); read != 1 || skipped != 2 {
				t.Errorf("%s: %d files read, %d skipped", name, read, skipped)
			}
		}
	})

	t.Run("what a directory holds", func(t *testing.T) {
		// A directory not key=value passes over; _SUCCESS, a .crc file and a
		// hidden directory are passed over, where Polars reads the last two
		// (feature-gaps §9); the root's own name is no key. It reads as the same
		// dataset without them.
		d := filepath.Join(dir, "k=0", "m")
		hivePart(t, d, "x/a=1/p0.parquet", v(1))
		writeText(t, filepath.Join(d, "x", "a=1"), "_SUCCESS", "")
		writeText(t, filepath.Join(d, "x", "a=1"), ".p0.parquet.crc", "junk")
		hivePart(t, d, ".tmp/a=2/p0.parquet", v(2))
		clean := filepath.Join(dir, "clean")
		hivePart(t, clean, "a=1/p0.parquet", v(1))
		got, want := collect(t, ursus.ScanParquet(d)), collect(t, ursus.ScanParquet(clean))
		ursustest.AssertFrameEqual(t, got, want)
		if !slices.Equal(got.Columns(), []string{"v", "a"}) {
			t.Errorf("the columns: %v", got.Columns())
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
