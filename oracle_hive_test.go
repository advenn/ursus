package ursus_test

// The hive group (step 502, from hive_test.go's cases): a directory of Parquet files
// read as one frame, each key=value directory a column, and each file's path as a
// column, each over the layout the param files describes, written with SinkParquet,
// and scanned under the param options, the scan_parquet arguments
// bench/scripts/oracle/hive.py asks Polars with; a path column is stripped of the
// directory's root.

import (
	"path/filepath"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	type scan = func(root string, opts []ursus.ParquetOption) *ursus.LazyFrame
	over := func(q scan) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			root := in.frameFiles(func(lf *ursus.LazyFrame, path string) error { return lf.SinkParquet(in.t.Context(), path) })
			return q(root, in.parquetOptions())
		}
	}
	byV := func(q scan) scan {
		return func(r string, o []ursus.ParquetOption) *ursus.LazyFrame { return q(r, o).Sort(ursus.Asc(c("v"))) }
	}
	sub := func(name string) scan {
		return func(r string, o []ursus.ParquetOption) *ursus.LazyFrame {
			return ursus.ScanParquet(filepath.Join(r, filepath.FromSlash(name)), o...)
		}
	}
	t := sub("t")
	where := func(pred ursus.Expr) scan {
		return func(r string, o []ursus.ParquetOption) *ursus.LazyFrame { return t(r, o).Filter(pred) }
	}
	glob := func(r string, o []ursus.ParquetOption) *ursus.LazyFrame {
		return ursus.ScanParquetGlob(filepath.Join(r, "t", "*", "*", "*.parquet"), o...)
	}
	oracleGroups["hive"] = map[string]oracleQuery{
		"a_directory": over(byV(t)),
		"the_keys_alone": over(func(r string, o []ursus.ParquetOption) *ursus.LazyFrame {
			return t(r, o).Select(c("a"), c("b")).Sort(ursus.Asc(c("b")))
		}),
		"a_count":            over(func(r string, o []ursus.ParquetOption) *ursus.LazyFrame { return t(r, o).Select(ursus.Len()) }),
		"a_hive_schema":      over(byV(t)),
		"dates":              over(byV(sub("days"))),
		"dates_as_text":      over(byV(sub("days"))),
		"a_filter_on_a_key":  over(where(c("a").Eq(int64(2)))),
		"a_key_and_a_column": over(byV(where(c("a").Eq(int64(1)).And(c("v").Gt(int64(1)))))),
		"a_keys_text":        over(where(c("b").Str().StartsWith("y"))),
		"a_keys_arithmetic":  over(where(c("a").Add(int64(1)).Eq(int64(3)))),
		"a_null_key":         over(where(c("a").IsNull())),
		"file_paths": over(byV(func(r string, o []ursus.ParquetOption) *ursus.LazyFrame {
			return t(r, o).Select(c("v"), oracleRelative("path", filepath.Join(r, "t")))
		})),
		"a_path_named_as_a_key":        over(t),
		"a_key_the_file_holds":         over(sub("x")),
		"a_key_the_file_holds_as_text": over(sub("bad")),
		"a_glob":                       over(byV(glob)),
		"a_glob_with_keys":             over(byV(glob)),
		"one_file":                     over(sub("t/a=1/b=x/p0.parquet")),
		"one_file_with_keys":           over(sub("t/a=1/b=x/p0.parquet")),
		"a_directory_without_keys":     over(byV(t)),
		"keys_apart_refused":           over(sub("ka")),
		"depths_apart_refused":         over(sub("da")),
		"many_files_gen": over(func(r string, o []ursus.ParquetOption) *ursus.LazyFrame {
			return sub("m")(r, o).Sort(ursus.Asc(c("r")))
		}),
	}
	oracleDifferences["hive/a_count"] = oracleCount("len")
}
