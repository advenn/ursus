package ursus_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestParquetSchemaEvolution is step 348: Parquet files whose columns differ read as
// one, as Polars' scan_parquet reads them by its schema, missing_columns,
// extra_columns and cast_options. Every answer is Polars 1.44.1's.
func TestParquetSchemaEvolution(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	dir := t.TempDir()
	p1 := hivePart(t, dir, "p1.parquet", ursus.Frame(i64s("a", 1, 2), strs("b", "x", "y")))
	p2 := hivePart(t, dir, "p2.parquet", ursus.Frame(i64s("a", 3), f64s("c", 1.5)))
	p3 := hivePart(t, dir, "p3.parquet", ursus.Frame(strs("b", "z"), i64s("a", 4)))
	p4 := hivePart(t, dir, "p4.parquet", ursus.Frame(i64s("a", 5), strs("b", "w")).Select(c("a").Cast(ursus.Int32), c("b")))
	p5 := hivePart(t, dir, "p5.parquet", ursus.Frame(f64s("f", 0.5)).Select(c("f").Cast(ursus.Float32)))
	p6 := hivePart(t, dir, "p6.parquet", ursus.Frame(f64s("f", 1.25)))
	refused := func(t *testing.T, q *ursus.LazyFrame, want ...string) {
		t.Helper()
		_, err := q.Collect(ctx)
		if err == nil {
			t.Fatalf("read, want an error saying %q", want)
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%v, want it to say %q", err, w)
			}
		}
	}
	both := []string{p1, p2}

	t.Run("refused as Polars refuses", func(t *testing.T) {
		// Polars: "extra column in file outside of expected schema: c, hint:
		// specify this column in the schema, or pass extra_columns='ignore'".
		refused(t, ursus.ScanParquetFiles(both), `"c"`)
		refused(t, ursus.ScanParquetFiles(both, ursus.WithInsertMissingColumns(true)), `"c"`, "WithIgnoreExtraColumns")
		// Polars: "data type mismatch for column a: incoming: Int32 != target:
		// Int64, hint: pass cast_options=pl.ScanCastOptions(integer_cast='upcast')".
		refused(t, ursus.ScanParquetFiles([]string{p1, p4}), `"a"`)
		// Polars: "incoming dtype cannot safely cast to target dtype".
		refused(t, ursus.ScanParquetFiles([]string{p4, p1}, ursus.WithUpcastIntegers(true)), `"a"`)
		refused(t, ursus.ScanParquetFiles([]string{p6, p5}), `"f"`)
	})

	t.Run("missing and extra columns", func(t *testing.T) {
		got := rowsOf(t, ursus.ScanParquetFiles(both, ursus.WithInsertMissingColumns(true), ursus.WithIgnoreExtraColumns(true)))
		if got != "1,x 2,y 3,<nil> {a: Int64, b: String}" {
			t.Errorf("%s", got)
		}
		// Columns in another order are read by name.
		if got := rowsOf(t, ursus.ScanParquetFiles([]string{p1, p3})); got != "1,x 2,y 4,z {a: Int64, b: String}" {
			t.Errorf("another order: %s", got)
		}
	})

	t.Run("upcasts", func(t *testing.T) {
		if got := rowsOf(t, ursus.ScanParquetFiles([]string{p1, p4}, ursus.WithUpcastIntegers(true))); got != "1,x 2,y 5,w {a: Int64, b: String}" {
			t.Errorf("Int32 to Int64: %s", got)
		}
		if got := rowsOf(t, ursus.ScanParquetFiles([]string{p6, p5}, ursus.WithUpcastFloats(true))); got != "1.25 0.5 {f: Float64}" {
			t.Errorf("Float32 to Float64: %s", got)
		}
		// An unsigned integer into a signed one that holds it, and not into one
		// that does not; on one thread too.
		i32 := hivePart(t, dir, "i32.parquet", ursus.Frame(i64s("n", -1)).Select(c("n").Cast(ursus.Int32)))
		u16 := hivePart(t, dir, "u16.parquet", ursus.Frame(i64s("n", 65535)).Select(c("n").Cast(ursus.Uint16)))
		u32 := hivePart(t, dir, "u32.parquet", ursus.Frame(i64s("n", 7)).Select(c("n").Cast(ursus.Uint32)))
		q := ursus.ScanParquetFiles([]string{i32, u16}, ursus.WithUpcastIntegers(true))
		if got := rowsOf(t, q); got != "-1 65535 {n: Int32}" {
			t.Errorf("UInt16 to Int32: %s", got)
		}
		df, err := q.Collect(ctx, ursus.WithThreads(1))
		if err != nil || df.Height() != 2 {
			t.Errorf("on one thread: %v", err)
		}
		refused(t, ursus.ScanParquetFiles([]string{i32, u32}, ursus.WithUpcastIntegers(true)), `"n"`)
	})

	t.Run("a given schema", func(t *testing.T) {
		s := mustSchema(ursus.Field{Name: "a", Type: ursus.Int64}, ursus.Field{Name: "b", Type: ursus.String},
			ursus.Field{Name: "c", Type: ursus.Float64})
		got := rowsOf(t, ursus.ScanParquetFiles(both, ursus.WithParquetSchema(s), ursus.WithInsertMissingColumns(true)))
		if got != "1,x,<nil> 2,y,<nil> 3,<nil>,1.5 {a: Int64, b: String, c: Float64}" {
			t.Errorf("%s", got)
		}
	})

	t.Run("a filter on a column a file lacks, and on one it widens", func(t *testing.T) {
		q := ursus.ScanParquetFiles(both, ursus.WithInsertMissingColumns(true), ursus.WithIgnoreExtraColumns(true)).
			Filter(c("b").IsNull())
		if got := rowsOf(t, q); got != "3,<nil> {a: Int64, b: String}" {
			t.Errorf("b is null: %s", got)
		}
		q = ursus.ScanParquetFiles([]string{p1, p4}, ursus.WithUpcastIntegers(true)).Filter(c("a").Gt(int64(4)))
		if got := rowsOf(t, q); got != "5,w {a: Int64, b: String}" {
			t.Errorf("a > 4: %s", got)
		}
	})

	t.Run("a directory", func(t *testing.T) {
		// A hive dataset whose files' columns differ.
		d := filepath.Join(dir, "ds")
		hivePart(t, d, "k=1/p.parquet", ursus.Frame(i64s("a", 1)))
		hivePart(t, d, "k=2/p.parquet", ursus.Frame(i64s("a", 2), strs("b", "x")))
		got := rowsOf(t, ursus.ScanParquet(d, ursus.WithParquetSchema(mustSchema(ursus.Field{Name: "a", Type: ursus.Int64},
			ursus.Field{Name: "b", Type: ursus.String})), ursus.WithInsertMissingColumns(true)).Sort(ursus.Asc(c("a"))))
		if got != "1,<nil>,1 2,x,2 {a: Int64, b: String, k: Int64!}" {
			t.Errorf("%s", got)
		}
	})
}
