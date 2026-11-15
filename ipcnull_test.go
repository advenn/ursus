package ursus_test

// An Arrow IPC file or stream with a Null column reads (step 522, backported to 0.7 by
// step 532). Every Arrow writer writes a Null column as a field node and no buffer;
// arrow-go's IPC reader builds it as a Null array of no buffers, and arrow-go's own
// array.Validate, which ursus calls on every array it imports, wanted the one buffer a
// Null array's layout declares, so ursus refused every IPC input with a Null column,
// top-level or nested, its own writing among them, and Polars', which writes an
// all-null column as Null. The differential here writes generated frames holding Null
// columns with WriteIPC and WriteIPCStream and reads each back through every entry
// point 0.7 has, each the frame written; TestIPCNullFilesReadAsArrowGoReadsThem reads
// Polars 2.1.0's and pyarrow 25.0.1's files in testdata/ipc, each the rows arrow-go
// reads in it. Master holds those files to Polars' recorded reading of them
// (testdata/oracle/ipcnull.json), through step 496's oracle harness, which 0.7 has not.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// inRead is one way to read an IPC input, by name.
type inRead struct {
	name string
	lf   *ursus.LazyFrame
}

// ipcEntryReads are every public way 0.7 has to read b, an Arrow IPC file (file true)
// or stream: from a path, a list of paths and a glob, and arrow-go's own reading of
// its records handed to ScanArrowRecords and, for a stream, its reader to ScanArrow,
// arrowin's other entry points. A file's ReadIPCSchema is its schema, returned too.
// Master reads it from its bytes, an fs.FS and an opener as well, which 0.7 has not.
func ipcEntryReads(t *testing.T, b []byte, file bool) ([]inRead, *ursus.Schema) {
	t.Helper()
	dir := t.TempDir()
	base := "in.arrows"
	if file {
		base = "in.arrow"
	}
	path := filepath.Join(dir, base)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var reads []inRead
	var schema *ursus.Schema
	if file {
		s, err := ursus.ReadIPCSchema(path)
		if err != nil {
			t.Fatalf("ReadIPCSchema: %v", err)
		}
		schema = s
		reads = append(reads,
			inRead{"ScanIPC", ursus.ScanIPC(path)},
			inRead{"ScanIPCFiles", ursus.ScanIPCFiles([]string{path})},
			inRead{"ScanIPCGlob", ursus.ScanIPCGlob(filepath.Join(dir, "*.arrow"))})
	} else {
		reads = append(reads,
			inRead{"ScanIPCStream", ursus.ScanIPCStream(path)},
			inRead{"ScanArrow", ursus.ScanArrow(func() (array.RecordReader, error) {
				return ipc.NewReader(bytes.NewReader(b))
			})})
	}
	if recs := arrowRecords(t, b, file); len(recs) > 0 {
		reads = append(reads, inRead{"ScanArrowRecords of arrow-go's records", ursus.ScanArrowRecords(recs...)})
	}
	return reads, schema
}

// arrowRecords are b's records as arrow-go's IPC reader reads them, released with the
// test.
func arrowRecords(t *testing.T, b []byte, file bool) []arrow.RecordBatch {
	t.Helper()
	var recs []arrow.RecordBatch
	t.Cleanup(func() {
		for _, r := range recs {
			r.Release()
		}
	})
	if file {
		fr, err := ipc.NewFileReader(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("arrow-go: %v", err)
		}
		defer fr.Close()
		for i := range fr.NumRecords() {
			rec, err := fr.RecordBatchAt(i)
			if err != nil {
				t.Fatalf("arrow-go, record %d: %v", i, err)
			}
			recs = append(recs, rec)
		}
		return recs
	}
	r, err := ipc.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("arrow-go: %v", err)
	}
	defer r.Release()
	for r.Next() {
		rec := r.RecordBatch()
		rec.Retain()
		recs = append(recs, rec)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("arrow-go: %v", err)
	}
	return recs
}

// requireEveryReadIs holds every read of b, a file or a stream, to want, and a file's
// schema to want's.
func requireEveryReadIs(t *testing.T, b []byte, file bool, want *ursus.DataFrame) {
	t.Helper()
	reads, schema := ipcEntryReads(t, b, file)
	if schema != nil {
		ursustest.AssertSchemaEqual(t, schema, want.Schema())
	}
	for _, r := range reads {
		got, err := r.lf.Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}

// inColumn is a column the differential may generate: its Arrow type, and a row's
// JSON as arrow-go's FromJSON reads it.
type inColumn struct {
	name string
	typ  arrow.DataType
	row  func(r *rand.Rand, i int) string
}

// inNulls is k nulls as JSON elements.
func inNulls(k int) string { return strings.TrimSuffix(strings.Repeat("null,", k), ",") }

// inOrNull is v, or null one row in five.
func inOrNull(r *rand.Rand, v func() string) string {
	if r.IntN(5) == 0 {
		return "null"
	}
	return v()
}

// inColumns are the differential's columns: the row and a Null column in every frame,
// a second Null column and Null nested in a List, a List of Lists, a Struct, a Struct
// of only it, a fixed-size list (an Array) and a map's values, beside columns of other
// types.
var inColumns = []inColumn{
	{"i", arrow.PrimitiveTypes.Int64, func(_ *rand.Rand, i int) string { return strconv.Itoa(i) }},
	{"n", arrow.Null, func(*rand.Rand, int) string { return "null" }},
	{"n2", arrow.Null, func(*rand.Rand, int) string { return "null" }},
	{"x", arrow.PrimitiveTypes.Int64, func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string { return strconv.FormatInt(r.Int64N(2001)-1000, 10) })
	}},
	{"f", arrow.PrimitiveTypes.Float64, func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string { return strconv.FormatFloat(r.NormFloat64()*1e3, 'g', -1, 64) })
	}},
	{"s", arrow.BinaryTypes.String, func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string {
			b, _ := json.Marshal([]string{"", "a", "héllo", "a string longer than twelve"}[r.IntN(4)])
			return string(b)
		})
	}},
	{"b", arrow.FixedWidthTypes.Boolean, func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string { return strconv.FormatBool(r.IntN(2) == 0) })
	}},
	{"l", arrow.ListOf(arrow.Null), func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string { return "[" + inNulls(r.IntN(4)) + "]" })
	}},
	{"ll", arrow.ListOf(arrow.ListOf(arrow.Null)), func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string {
			inner := make([]string, r.IntN(3))
			for k := range inner {
				inner[k] = inOrNull(r, func() string { return "[" + inNulls(r.IntN(3)) + "]" })
			}
			return "[" + strings.Join(inner, ",") + "]"
		})
	}},
	{"st", arrow.StructOf(arrow.Field{Name: "z", Type: arrow.Null, Nullable: true},
		arrow.Field{Name: "y", Type: arrow.PrimitiveTypes.Int64, Nullable: true}), func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string {
			return `{"z": null, "y": ` + inOrNull(r, func() string { return strconv.Itoa(r.IntN(100)) }) + "}"
		})
	}},
	{"sn", arrow.StructOf(arrow.Field{Name: "z", Type: arrow.Null, Nullable: true}), func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string { return `{"z": null}` })
	}},
	{"fa", arrow.FixedSizeListOf(2, arrow.Null), func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string { return "[null, null]" })
	}},
	{"m", arrow.MapOf(arrow.BinaryTypes.String, arrow.Null), func(r *rand.Rand, _ int) string {
		return inOrNull(r, func() string {
			entries := make([]string, r.IntN(3))
			for k := range entries {
				entries[k] = fmt.Sprintf(`{"key": "k%d", "value": null}`, k)
			}
			return "[" + strings.Join(entries, ",") + "]"
		})
	}},
}

// inGenerate draws a frame of n rows from r: the row i and the Null column n, and each
// other column of inColumns one time in two, in an order drawn too, built as arrow-go
// arrays and read by ScanArrowRecords, whose Null arrays hold the one nil buffer.
func inGenerate(t *testing.T, r *rand.Rand, n int) *ursus.DataFrame {
	t.Helper()
	var cols []inColumn
	for k, c := range inColumns {
		if k < 2 || r.IntN(2) == 0 {
			cols = append(cols, c)
		}
	}
	r.Shuffle(len(cols), func(a, b int) { cols[a], cols[b] = cols[b], cols[a] })
	mem := memory.NewGoAllocator()
	fields := make([]arrow.Field, len(cols))
	arrs := make([]arrow.Array, len(cols))
	for k, c := range cols {
		rows := make([]string, n)
		for i := range rows {
			rows[i] = c.row(r, i)
		}
		a, _, err := array.FromJSON(mem, c.typ, strings.NewReader("["+strings.Join(rows, ",")+"]"))
		if err != nil {
			t.Fatalf("building %s: %v", c.name, err)
		}
		t.Cleanup(a.Release)
		fields[k] = arrow.Field{Name: c.name, Type: c.typ, Nullable: true}
		arrs[k] = a
	}
	rec := array.NewRecordBatch(arrow.NewSchema(fields, nil), arrs, int64(n))
	t.Cleanup(rec.Release)
	df, err := ursus.ScanArrowRecords(rec).Collect(t.Context())
	if err != nil {
		t.Fatalf("the generated frame: %v", err)
	}
	return df
}

// TestIPCNullColumnsRoundTrip: generated frames of 0, 1 and many rows, a Null column
// in each, and Null nested in lists, structs, an Array and a map beside columns of
// other types, written by WriteIPC and WriteIPCStream in each codec, at the default
// batch size and at a small one, so that a file holds many record batches; each is
// read back through every entry point 0.7 has (ipcEntryReads), and a file's Null
// column and row alone by a projection, and each read is the frame written.
func TestIPCNullColumnsRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(522, 2026))
	codecs := []struct {
		name string
		c    ursus.IPCCompression
	}{{"uncompressed", ursus.IPCUncompressed}, {"LZ4", ursus.IPCLZ4}, {"Zstandard", ursus.IPCZstd}}
	for seed := range 6 {
		for _, n := range []int{0, 1, 300 + r.IntN(2000)} {
			want := inGenerate(t, r, n)
			batch := 1 + r.IntN(97)
			for _, codec := range codecs {
				for _, opts := range [][]ursus.IPCSinkOption{
					{ursus.WithIPCCompression(codec.c)},
					{ursus.WithIPCCompression(codec.c), ursus.WithBatchSize(batch)},
				} {
					name := fmt.Sprintf("seed %d, %d rows, %s, %d options, columns %v", seed, n, codec.name, len(opts), want.Columns())
					t.Run(name, func(t *testing.T) {
						var file, stream bytes.Buffer
						if err := want.Lazy().WriteIPC(t.Context(), &file, opts...); err != nil {
							t.Fatalf("WriteIPC: %v", err)
						}
						if err := want.Lazy().WriteIPCStream(t.Context(), &stream, opts...); err != nil {
							t.Fatalf("WriteIPCStream: %v", err)
						}
						requireEveryReadIs(t, file.Bytes(), true, want)
						requireEveryReadIs(t, stream.Bytes(), false, want)
						path := filepath.Join(t.TempDir(), "in.arrow")
						if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
							t.Fatal(err)
						}
						got, err := ursus.ScanIPC(path).Select(ursus.Col("n"), ursus.Col("i")).Collect(t.Context())
						if err != nil {
							t.Fatalf("a projection of the Null column: %v", err)
						}
						alone, err := want.Select(t.Context(), ursus.Col("n"), ursus.Col("i"))
						if err != nil {
							t.Fatal(err)
						}
						ursustest.AssertFrameEqual(t, got, alone)
					})
				}
			}
		}
	}
}

// ipcNullFiles are the Arrow IPC files and streams with a Null column in testdata/ipc,
// as master's bench/scripts/oracle/record_ipcnull.py writes them (step 522): Polars
// 2.1.0's (five rows, Null top-level and in a List, a Struct and an Array; no rows; a
// frame of only a Null column; record batches of two rows) and pyarrow 25.0.1's
// (three rows, Null top-level and in a List, a LargeList, a Struct, a FixedSizeList, a
// Map's values and a Dictionary's values; no rows; batches of one row), uncompressed,
// LZ4 and Zstandard.
var ipcNullFiles = []string{
	"polars_null.arrow", "polars_null.arrows", "polars_null_lz4.arrow", "polars_null_lz4.arrows",
	"polars_null_zstd.arrow", "polars_null_zstd.arrows", "polars_null_empty.arrow", "polars_null_empty.arrows",
	"polars_null_only.arrow", "polars_null_only.arrows", "polars_null_batches.arrow",
	"pyarrow_null.arrow", "pyarrow_null.arrows", "pyarrow_null_lz4.arrow", "pyarrow_null_lz4.arrows",
	"pyarrow_null_zstd.arrow", "pyarrow_null_zstd.arrows", "pyarrow_null_empty.arrow", "pyarrow_null_empty.arrows",
	"pyarrow_null_batches.arrow", "pyarrow_null_batches.arrows",
}

// jsonRows decodes newline-delimited JSON objects into Go values, numbers kept as
// their text, so that two writers' rows compare by value whatever their key order.
func jsonRows(t *testing.T, what string, b []byte) []map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var rows []map[string]any
	for dec.More() {
		var row map[string]any
		if err := dec.Decode(&row); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		rows = append(rows, row)
	}
	return rows
}

// TestIPCNullFilesReadAsArrowGoReadsThem: each of Polars' and pyarrow's files and
// streams with a Null column (ipcNullFiles) reads through every entry point 0.7 has,
// each the same frame, and that frame is the file as arrow-go reads it: its columns
// arrow-go's schema's fields, in order, and its rows arrow-go's records' rows, value
// for value, as each side writes them as JSON (arrow-go's array.RecordToJSON, ursus's
// WriteNDJSON). Every file of rows was refused before step 522's fix.
func TestIPCNullFilesReadAsArrowGoReadsThem(t *testing.T) {
	for _, name := range ipcNullFiles {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("testdata", "ipc", name)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			file := !strings.HasSuffix(name, ".arrows")
			var schema *arrow.Schema
			var want bytes.Buffer
			rows := 0
			if file {
				fr, err := ipc.NewFileReader(bytes.NewReader(b))
				if err != nil {
					t.Fatalf("arrow-go: %v", err)
				}
				schema = fr.Schema()
				fr.Close()
			} else {
				r, err := ipc.NewReader(bytes.NewReader(b))
				if err != nil {
					t.Fatalf("arrow-go: %v", err)
				}
				schema = r.Schema()
				r.Release()
			}
			for _, rec := range arrowRecords(t, b, file) {
				if err := array.RecordToJSON(rec, &want); err != nil {
					t.Fatalf("arrow-go's JSON: %v", err)
				}
				rows += int(rec.NumRows())
			}
			lf := ursus.ScanIPCStream(path)
			if file {
				lf = ursus.ScanIPC(path)
			}
			df, err := lf.Collect(t.Context())
			if err != nil {
				t.Fatalf("ursus refuses %s, which arrow-go reads: %v", name, err)
			}
			requireEveryReadIs(t, b, file, df)
			names := make([]string, schema.NumFields())
			for i, f := range schema.Fields() {
				names[i] = f.Name
			}
			if !reflect.DeepEqual(df.Columns(), names) {
				t.Fatalf("ursus reads the columns %v, and arrow-go %v", df.Columns(), names)
			}
			if df.Height() != rows {
				t.Fatalf("ursus reads %d rows, and arrow-go %d", df.Height(), rows)
			}
			var got bytes.Buffer
			if err := df.Lazy().WriteNDJSON(t.Context(), &got); err != nil {
				t.Fatalf("ursus's JSON: %v", err)
			}
			g, w := jsonRows(t, "ursus's JSON", got.Bytes()), jsonRows(t, "arrow-go's JSON", want.Bytes())
			if !reflect.DeepEqual(g, w) {
				t.Fatalf("ursus reads the rows\n%s\nand arrow-go\n%s", got.String(), want.String())
			}
		})
	}
}
