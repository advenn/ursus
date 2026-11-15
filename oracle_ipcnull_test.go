package ursus_test

// The ipcnull group (step 522): Arrow IPC files and streams with a Null column,
// Polars 2.1.0's and pyarrow 25.0.1's, which bench/scripts/oracle/record_ipcnull.py
// writes into testdata/ipc, each recorded as Polars 2.1.0 reads it. A case's param path
// names the file; ursus reads it through every IPC entry point and arrow-go's records
// handed to ScanArrowRecords and ScanArrow (ipcEntryReads), each the same frame, and
// the frame ScanIPC or ScanIPCStream reads is compared with the recording.

import (
	"os"
	"strings"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

func init() {
	// read is the file or stream at the param path, every read of it the same frame.
	read := func(in oracleInputs) *ursus.LazyFrame {
		path := in.text("path")
		b, err := os.ReadFile(path)
		if err != nil {
			in.t.Fatal(err)
		}
		file := !strings.HasSuffix(path, ".arrows")
		first := ursus.ScanIPCStream(path)
		if file {
			first = ursus.ScanIPC(path)
		}
		want, err := first.Collect(in.t.Context())
		if err != nil {
			return first
		}
		reads, schema := ipcEntryReads(in.t, b, file)
		if schema != nil {
			ursustest.AssertSchemaEqual(in.t, schema, want.Schema())
		}
		for _, r := range reads {
			got, err := r.lf.Collect(in.t.Context())
			if err != nil {
				in.t.Fatalf("%s refuses %s, which %s reads: %v", r.name, path, map[bool]string{true: "ScanIPC", false: "ScanIPCStream"}[file], err)
			}
			ursustest.AssertFrameEqual(in.t, got, want)
		}
		return first
	}
	oracleGroups["ipcnull"] = map[string]oracleQuery{
		"polars_null_file":            read,
		"polars_null_stream":          read,
		"polars_null_lz4_file":        read,
		"polars_null_lz4_stream":      read,
		"polars_null_zstd_file":       read,
		"polars_null_zstd_stream":     read,
		"polars_null_empty_file":      read,
		"polars_null_empty_stream":    read,
		"polars_null_only_file":       read,
		"polars_null_only_stream":     read,
		"polars_null_batches_file":    read,
		"pyarrow_null_file":           read,
		"pyarrow_null_stream":         read,
		"pyarrow_null_lz4_file":       read,
		"pyarrow_null_lz4_stream":     read,
		"pyarrow_null_zstd_file":      read,
		"pyarrow_null_zstd_stream":    read,
		"pyarrow_null_empty_file":     read,
		"pyarrow_null_empty_stream":   read,
		"pyarrow_null_batches_file":   read,
		"pyarrow_null_batches_stream": read,
	}
}
