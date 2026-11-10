package ursus_test

// The ipcfiles group (step 502, from ipcfiles_test.go's table and ipc_test.go's
// Polars files): scan_ipc of several files, a glob, a list and a hive directory, and
// include_file_paths, each over the layout the param files describes, written with
// WriteIPC, and scanned under the param options, the scan_ipc arguments
// bench/scripts/oracle/ipcfiles.py asks Polars with; a path column is stripped of the
// directory's root, and a refusal is a schema error. A file of testdata/ipc, Polars
// 1.44.1's, is the param path, read as Polars 2.1 reads it.

import (
	"os"
	"path/filepath"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	layout := func(in oracleInputs) string {
		return in.frameFiles(func(lf *ursus.LazyFrame, path string) error {
			f, err := os.Create(path)
			if err != nil {
				return err
			}
			defer f.Close()
			return lf.WriteIPC(in.t.Context(), f)
		})
	}
	// over asks q of the layout's root, under the options.
	over := func(q func(root string, opts []ursus.IPCScanOption) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return q(layout(in), in.ipcOptions()) }
	}
	files := func(root string, names ...string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = filepath.Join(root, n)
		}
		return out
	}
	d := func(root string) string { return filepath.Join(root, "d") }
	glob := func(pattern ...string) func(string, []ursus.IPCScanOption) *ursus.LazyFrame {
		return func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame {
			return ursus.ScanIPCGlob(filepath.Join(append([]string{r}, pattern...)...), o...)
		}
	}
	list := func(names ...string) func(string, []ursus.IPCScanOption) *ursus.LazyFrame {
		return func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame {
			return ursus.ScanIPCFiles(files(r, names...), o...)
		}
	}
	dir := func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame { return ursus.ScanIPC(d(r), o...) }
	schemaErr := func(q oracleQuery) oracleQuery { return oracleRefusedAs(ursus.ErrSchema, q) }
	fixture := func(in oracleInputs) *ursus.LazyFrame { return ursus.ScanIPC(in.text("path")) }
	oracleGroups["ipcfiles"] = map[string]oracleQuery{
		"a_glob":            over(glob("p[12].arrow")),
		"a_list":            over(list("p2.arrow", "p1.arrow")),
		"by_name":           over(list("p1.arrow", "p3.arrow")),
		"a_directorys_keys": over(dir),
		"a_glob_no_keys":    over(glob("d", "**", "*.arrow")),
		"a_glob_with_keys":  over(glob("d", "**", "*.arrow")),
		"the_paths": over(func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame {
			return dir(r, o).WithColumns(oracleRelative("p", d(r)))
		}),
		"a_filter_on_a_key": over(func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame { return dir(r, o).Filter(c("k").Eq(int64(2))) }),
		"a_key_alone":       over(func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame { return dir(r, o).Select(c("k")) }),
		"a_keys_type_given": over(dir),
		"under_a_limit": over(func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame {
			return glob("p[12].arrow")(r, o).Head(2)
		}),
		"one_files_path": over(func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame {
			return ursus.ScanIPC(filepath.Join(r, "p1.arrow"), o...).WithColumns(oracleRelative("p", r))
		}),
		"a_column_missing":          schemaErr(over(list("p1.arrow", "p4.arrow"))),
		"a_column_more":             schemaErr(over(list("p4.arrow", "p1.arrow"))),
		"another_type":              schemaErr(over(list("p1.arrow", "p5.arrow"))),
		"a_path_columns_name_taken": schemaErr(over(dir)),
		"polars_file":               fixture,
		"polars_file_lz4":           fixture,
		"polars_file_zstd":          fixture,
		"polars_stream":             func(in oracleInputs) *ursus.LazyFrame { return ursus.ScanIPCStream(in.text("path")) },
		"a_directory_gen": over(func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame {
			return ursus.ScanIPC(filepath.Join(r, "g"), o...).Sort(ursus.Asc(c("r")))
		}),
		"a_glob_gen": over(func(r string, o []ursus.IPCScanOption) *ursus.LazyFrame {
			return glob("g", "**", "*.arrow")(r, o).Sort(ursus.Asc(c("r")))
		}),
	}
}
