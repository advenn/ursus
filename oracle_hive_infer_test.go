package ursus_test

// The hive_infer group (step 503, from internal/source/hive/hive_test.go's TestInfer):
// the type a partition key's values infer. Each case's params name the files of its
// layout, k=<value>/<row>.parquet, which the test writes from the recorded input
// frame into a directory of its own and scans with hive partitioning, as
// bench/scripts/oracle/hive_infer.py asks Polars.

import (
	"os"
	"path/filepath"

	"github.com/advenn/ursus"
)

// oracleTexts is the case's param name, a list of strings.
func oracleTexts(in oracleInputs, name string) []string {
	list, ok := in.params[name].([]any)
	if !ok {
		in.t.Fatalf("the case has no list param %q", name)
	}
	out := make([]string, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			in.t.Fatalf("param %q holds %v (%T), not text", name, v, v)
		}
		out[i] = s
	}
	return out
}

func init() {
	// scan writes row j of the input frame to the j-th path, below a fresh directory,
	// and reads the directory back as a hive dataset, its rows in the input's order.
	scan := func(in oracleInputs) *ursus.LazyFrame {
		root := in.t.TempDir()
		for j, rel := range oracleTexts(in, "paths") {
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				in.t.Fatal(err)
			}
			if err := in.df().Filter(ursus.Col("i").Eq(int64(j))).SinkParquet(in.t.Context(), path); err != nil {
				in.t.Fatal(err)
			}
		}
		return ursus.ScanParquet(root, ursus.WithHivePartitioning(true)).Sort(ursus.Asc(ursus.Col("i")))
	}
	reg := map[string]oracleQuery{}
	for _, name := range []string{
		"ints", "int128_past_int64", "int128_min", "floats", "float_forms", "bools",
		"date_iso", "date_short", "date_slashes", "date_dots", "date_dmy", "datetimes", "datetimes_zoned",
		"text_plus", "text_nan_lower", "text_hex", "text_underscore", "text_comma", "text_yes", "text_bare_exponent",
		"text_hour_alone", "text_month_13", "text_feb_30", "text_int_beside_x", "text_date_beside_x", "text_bool_beside_1",
		"text_zoned_beside_naive", "int_like_a_date", "two_digit_year",
		"gen_ints", "gen_dates", "gen_floats",
	} {
		reg[name] = scan
	}
	// Polars 2.1 reads a key of dates by its first value's layout and refuses the
	// third; ursus reads each value by its own, as CSV dates, and Polars is asked so
	// (step 507): the key as text, each value by the layouts.
	reg["date_layouts_mixed"] = scan
	oracleGroups["hive_infer"] = reg
	// A date of a two-digit year is text here, where Polars reads 24-01-02 as the
	// 24th of January of the year 2 (feature-gaps §9, step 347).
	oracleDifferences["hive_infer/two_digit_year"] = oracleDifference{
		why:    "a key of a two-digit year is text here; Polars reads the year 2",
		dtypes: map[string]string{"k": "String"},
		cells:  map[string]map[int]any{"k": {0: "24-01-02"}},
	}
}
