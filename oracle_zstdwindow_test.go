package ursus_test

// The zstdwindow group (step 528): Zstandard streams and Arrow IPC and Parquet files
// whose frames declare windows about 128 MiB, the limit the reference decoder keeps
// by default, which bench/scripts/oracle/record_zstdwindow.py writes into
// testdata/zstd, each recorded as Polars 2.1.0 reads it. A case's param path names the
// file; ursus reads it by the scan of its kind, and a refusal, of a window past 128
// MiB, is an I/O error naming the limit. A Parquet page's window is not held to it, as
// Polars 2.1 holds it to none.

import (
	"strings"

	"github.com/advenn/ursus"
)

func init() {
	read := func(in oracleInputs) *ursus.LazyFrame {
		path := in.text("path")
		switch {
		case strings.HasSuffix(path, ".csv.zst"):
			return ursus.ScanCSV(path)
		case strings.HasSuffix(path, ".ndjson.zst"):
			return ursus.ScanNDJSON(path)
		case strings.HasSuffix(path, ".json.zst"):
			return ursus.ScanJSON(path)
		case strings.HasSuffix(path, ".arrows"):
			return ursus.ScanIPCStream(path)
		case strings.HasSuffix(path, ".arrow"):
			return ursus.ScanIPC(path)
		}
		return ursus.ScanParquet(path)
	}
	reg := map[string]oracleQuery{}
	for _, name := range []string{
		"window_1mib_csv_zst", "window_8mib_csv_zst", "window_64mib_csv_zst", "window_128mib_csv_zst",
		"window_128mib_ndjson_zst", "window_128mib_json_zst", "window_128mib_arrow", "window_128mib_arrows",
		"window_128mib_parquet", "window_256mib_parquet",
	} {
		reg[name] = read
	}
	for _, name := range []string{
		"window_256mib_csv_zst", "window_512mib_csv_zst", "single_segment_140mib_csv_zst",
		"window_256mib_ndjson_zst", "window_256mib_json_zst", "window_256mib_arrow", "window_256mib_arrows",
	} {
		reg[name] = oracleRefusedAs(ursus.ErrIO, read)
	}
	oracleGroups["zstdwindow"] = reg
}
