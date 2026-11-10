package ursus_test

// The partitionwrite group (step 502, from partitionwrite_test.go's layouts): a
// dataset written by keys, each key's directory and file named as Polars names them,
// files of at most so many rows, and CSV, IPC and NDJSON datasets; each a sink of the
// input under the params (format, keys, include_key, max_rows), the dataset's file
// names the written text, or the dataset read back, as bench/scripts/oracle/
// partitionwrite.py asks Polars.

import (
	"encoding/json"
	"path/filepath"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	byV := func(q *ursus.LazyFrame) *ursus.LazyFrame { return q.Sort(ursus.Asc(c("v"))) }
	// sink writes the input under the params' options into dir.
	sink := func(in oracleInputs, dir string) error {
		var opts []ursus.PartitionOption
		if keys := oracleStrings(in, "keys"); keys != nil {
			opts = append(opts, ursus.WithPartitionBy(keys...))
		}
		if b, ok := in.params["include_key"].(bool); ok {
			opts = append(opts, ursus.WithPartitionIncludeKey(b))
		}
		if n, ok := in.params["max_rows"].(json.Number); ok {
			opts = append(opts, ursus.WithMaxRowsPerFile(int(oracleInt(in.t, n))))
		}
		ctx := in.t.Context()
		switch f := in.text("format"); f {
		case "parquet":
			po := make([]ursus.ParquetSinkOption, len(opts))
			for i, o := range opts {
				po[i] = o
			}
			return in.df().SinkParquet(ctx, dir, po...)
		case "csv":
			co := make([]ursus.CSVSinkOption, len(opts))
			for i, o := range opts {
				co[i] = o
			}
			return in.df().SinkCSV(ctx, dir, co...)
		case "ipc":
			io := make([]ursus.IPCSinkOption, len(opts))
			for i, o := range opts {
				io[i] = o
			}
			return in.df().SinkIPC(ctx, dir, io...)
		case "ndjson":
			no := make([]ursus.NDJSONSinkOption, len(opts))
			for i, o := range opts {
				no[i] = o
			}
			return in.df().SinkNDJSON(ctx, dir, no...)
		default:
			in.t.Fatalf("no sink of the format %q", f)
			return nil
		}
	}
	// listing is the dataset's file names, a line each.
	listing := func(in oracleInputs) *ursus.LazyFrame {
		dir := filepath.Join(in.t.TempDir(), "out")
		if err := sink(in, dir); err != nil {
			return in.written("", err)
		}
		return in.written(oracleListing(in, dir), nil)
	}
	// back is the dataset read back by q.
	back := func(q func(dir string) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			dir := filepath.Join(in.t.TempDir(), "out")
			if err := sink(in, dir); err != nil {
				in.t.Fatal(err)
			}
			return q(dir)
		}
	}
	oneFile := func(dir string) *ursus.LazyFrame {
		return ursus.ScanParquet(filepath.Join(dir, "k=a%2Fb", "j=2", "00000000.parquet"))
	}
	withPaths := func(dir string) *ursus.LazyFrame {
		return byV(ursus.ScanParquet(dir, ursus.WithIncludeFilePaths("path")).WithColumns(oracleRelative("path", dir)))
	}
	reg := map[string]oracleQuery{
		"a_hive_dataset_read_back":                     back(func(dir string) *ursus.LazyFrame { return byV(ursus.ScanParquet(dir)) }),
		"a_files_columns":                              back(oneFile),
		"a_files_columns_without_keys":                 back(oneFile),
		"without_keys_read_back":                       back(func(dir string) *ursus.LazyFrame { return byV(ursus.ScanParquet(dir).Select(c("k"), c("j"), c("v"))) }),
		"files_of_at_most_two_rows_read_back":          back(withPaths),
		"files_of_at_most_three_rows_no_key_read_back": back(withPaths),
		"a_hive_dataset_gen_read_back":                 back(func(dir string) *ursus.LazyFrame { return byV(ursus.ScanParquet(dir)) }),
	}
	for _, name := range []string{
		"a_hive_dataset", "a_keys_name_escaped", "a_key_of_d", "a_key_of_naive", "a_key_of_tz", "a_key_of_ns", "a_key_of_f", "a_key_of_b",
		"a_key_of_dec", "a_key_of_tm", "files_of_at_most_two_rows", "files_of_at_most_three_rows_no_key", "a_csv_dataset",
		"an_ipc_dataset", "an_ndjson_dataset", "an_unknown_key_refused", "a_hive_dataset_gen",
	} {
		reg[name] = listing
	}
	oracleGroups["partitionwrite"] = reg
	// A key's name is escaped as its value is, where Polars writes it as it is and a
	// slash in it makes a directory; a time of day's key keeps its fraction, where
	// Polars' path drops it (feature-gaps.md §9, step 367).
	oracleDifferences["partitionwrite/a_keys_name_escaped"] = oracleDifference{
		why:  "a key's name is escaped as a value is here, a%20b%2Fc; Polars writes it as it is, a slash making a directory",
		text: "a%20b%2Fc=1/00000000.parquet",
	}
	oracleDifferences["partitionwrite/a_key_of_tm"] = oracleDifference{
		why:  "a time of day's key keeps its fraction here, where Polars' path drops it",
		text: "tm=03%3A04%3A05.123456/00000000.parquet",
	}
}
