package ursus_test

// The filepaths group (step 502, from filepaths_test.go's table): include_file_paths
// for CSV and NDJSON, a String column of each row's file path after the files'
// columns, each over the layout the param files describes, scanned under the param
// options, the scan_csv or scan_ndjson arguments bench/scripts/oracle/filepaths.py
// asks Polars with; the path column is stripped of the directory's root.

import (
	"path/filepath"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	type scan = func(in oracleInputs, root string) *ursus.LazyFrame
	over := func(q scan) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return q(in, in.textFiles()) }
	}
	paths := func(root string, names []string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = filepath.Join(root, n)
		}
		return out
	}
	rel := func(root string, lf *ursus.LazyFrame) *ursus.LazyFrame {
		return lf.WithColumns(oracleRelative("path", root))
	}
	// csv is the files named, or the glob p*.csv, under the options, the path relative.
	csv := func(names ...string) scan {
		return func(in oracleInputs, r string) *ursus.LazyFrame {
			if names == nil {
				return rel(r, ursus.ScanCSVGlob(filepath.Join(r, "p*.csv"), in.csvOptions()...))
			}
			return rel(r, ursus.ScanCSVFiles(paths(r, names), in.csvOptions()...))
		}
	}
	nd := func(names ...string) scan {
		return func(in oracleInputs, r string) *ursus.LazyFrame {
			if names == nil {
				return rel(r, ursus.ScanNDJSONGlob(filepath.Join(r, "p*.ndjson"), in.ndjsonOptions()...))
			}
			return rel(r, ursus.ScanNDJSONFiles(paths(r, names), in.ndjsonOptions()...))
		}
	}
	then := func(q scan, f func(*ursus.LazyFrame) *ursus.LazyFrame) scan {
		return func(in oracleInputs, r string) *ursus.LazyFrame { return f(q(in, r)) }
	}
	path := func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(c("path")) }
	bigs := []string{"big0.csv", "big1.csv", "big2.csv"}
	oracleGroups["filepaths"] = map[string]oracleQuery{
		"a_glob": over(csv()),
		"a_list_as_given": over(func(in oracleInputs, r string) *ursus.LazyFrame {
			return rel(r, ursus.ScanCSVFiles([]string{filepath.Join(r, "p1.csv"), r + "/./p2.csv"}, in.csvOptions()...))
		}),
		"one_file":       over(csv("p1.csv")),
		"the_path_alone": over(then(csv(), path)),
		"a_filter_on_it": over(then(csv(), func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(c("path").Str().EndsWith("p2.csv")).Select(c("a"))
		})),
		"under_a_limit":        over(then(csv(), func(lf *ursus.LazyFrame) *ursus.LazyFrame { return path(lf.Head(2)) })),
		"a_schema_given":       over(csv("p1.csv")),
		"ndjsons_glob":         over(nd()),
		"ndjsons_path_alone":   over(then(nd("p2.ndjson", "p1.ndjson"), path)),
		"ndjsons_schema_given": over(nd("p1.ndjson")),
		"a_field_past_the_files": oracleRefusedAs(ursus.ErrValue, over(func(in oracleInputs, r string) *ursus.LazyFrame {
			return ursus.ScanCSV(filepath.Join(r, "m.csv"), in.csvOptions()...)
		})),
		"csvs_name_taken": oracleRefusedAs(ursus.ErrSchema, over(func(in oracleInputs, r string) *ursus.LazyFrame {
			return ursus.ScanCSV(filepath.Join(r, "p1.csv"), in.csvOptions()...)
		})),
		"ndjsons_name_taken": oracleRefusedAs(ursus.ErrSchema, over(func(in oracleInputs, r string) *ursus.LazyFrame {
			return ursus.ScanNDJSON(filepath.Join(r, "p1.ndjson"), in.ndjsonOptions()...)
		})),
		"many_rows_gen": over(csv(bigs...)),
		"a_count_a_file_gen": over(then(csv(bigs...), func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(c("path")).Agg(ursus.Len().Alias("n")).Sort(ursus.Asc(c("path")))
		})),
	}
	oracleDifferences["filepaths/a_count_a_file_gen"] = oracleCount("n")
}
