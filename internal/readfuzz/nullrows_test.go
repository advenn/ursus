package readfuzz_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
)

// A record of Null columns, or of none, holds nothing a row, and Arrow IPC writes it
// as its field nodes and no buffer, so a few hundred bytes declare 2^40 rows (step
// 511's finding-record-of-2e40-rows-of-no-data). ursus holds such a column as its
// type and its length since step 529, a scan whose result is collected or counted
// hands its records on whole, and Collect, Count, Head, Select, Slice and Concat of
// them run in constant space: here, inside the IPC targets' bounds, the 2^40 input's
// record, well-formed, and generated inputs of 2^20 to 2^40 rows a record, a file or a
// stream, in each codec. The oracle is arrow-go's reading of the same bytes: its
// records' rows, summed, and its fields.
//
// The 2^40 input as the fuzzer saved it is a record of no columns and 2^40 + 18,432
// rows whose field nodes' offset points past its metadata: arrow-go, reading no
// column, never follows it, and step 519's walk refuses it as corrupt before a row is
// read. The targets skipped it with every other record of so many rows, so the
// refusal went unseen; it is held here, and arrow-go's writing of the record it
// declares is read.

// nullInput is an IPC input of records of Null columns, or of none.
type nullInput struct {
	name string
	b    []byte
	file bool
}

// seed2e40 is the 2^40 input as the fuzzer saved it.
func seed2e40(t *testing.T) []byte {
	return corpusInput(t, filepath.Join("testdata", "fuzz", "FuzzScanIPCStream", "finding-record-of-2e40-rows-of-no-data"))
}

// declared is the rows of each record arrow-go reads of a stream.
func declared(t *testing.T, b []byte) []int64 {
	t.Helper()
	sr, err := ipc.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Release()
	var rows []int64
	for sr.Next() {
		rows = append(rows, sr.RecordBatch().NumRows())
	}
	return rows
}

// nullRecords writes a file or a stream of len(rows) records of cols Null columns,
// named n0, n1 and on, each of its rows.
func nullRecords(t testing.TB, file bool, cols int, rows []int64, opts ...ipc.Option) []byte {
	t.Helper()
	fields := make([]arrow.Field, cols)
	for j := range fields {
		fields[j] = arrow.Field{Name: fmt.Sprintf("n%d", j), Type: arrow.Null, Nullable: true}
	}
	schema := arrow.NewSchema(fields, nil)
	var buf bytes.Buffer
	opts = append(opts, ipc.WithSchema(schema))
	var w interface {
		Write(arrow.RecordBatch) error
		Close() error
	}
	if file {
		fw, err := ipc.NewFileWriter(&buf, opts...)
		if err != nil {
			t.Fatal(err)
		}
		w = fw
	} else {
		w = ipc.NewWriter(&buf, opts...)
	}
	for _, n := range rows {
		arrs := make([]arrow.Array, cols)
		for j := range arrs {
			arrs[j] = array.NewNull(int(n))
		}
		if err := w.Write(array.NewRecordBatch(schema, arrs, n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// nullInputs are the 2^40 input's record, and generated files and streams of up to
// three Null columns, or none, in one to four records of 2^20 to 2^40 rows, the last
// of two of them exactly 2^40, in each codec.
func nullInputs(t *testing.T) []nullInput {
	t.Helper()
	seed := declared(t, seed2e40(t))
	ins := []nullInput{{
		name: fmt.Sprintf("the 2^40 input's record of %v rows, as arrow-go writes it", seed),
		b:    nullRecords(t, false, 0, seed),
	}}
	rng := rand.New(rand.NewPCG(529, 3))
	codecs := []struct {
		name string
		opts []ipc.Option
	}{{"", nil}, {", LZ4", []ipc.Option{ipc.WithLZ4()}}, {", Zstandard", []ipc.Option{ipc.WithZstd()}}}
	for i := range 12 {
		file := i%2 == 0
		cols := rng.IntN(4)
		rows := make([]int64, 1+rng.IntN(4))
		for k := range rows {
			e := 20 + rng.IntN(20)
			rows[k] = 1<<e + rng.Int64N(1<<e)
		}
		if i == 0 || i == 1 {
			rows[len(rows)-1] = 1 << 40
		}
		c := codecs[i%len(codecs)]
		kind := "stream"
		if file {
			kind = "file"
		}
		ins = append(ins, nullInput{
			name: fmt.Sprintf("%s of %d Null columns, records of %v rows%s", kind, cols, rows, c.name),
			b:    nullRecords(t, file, cols, rows, c.opts...),
			file: file,
		})
	}
	return ins
}

// arrowNulls is b as arrow-go reads it: its records' rows summed, its fields'
// names, and whether each is Null. A record's rows are checked to be declared alone:
// no buffer of a Null column, and no column, has bytes to hold one.
func arrowNulls(t *testing.T, b []byte, file bool) (rows int64, names []string, recs []arrow.RecordBatch) {
	t.Helper()
	var schema *arrow.Schema
	if file {
		fr, err := ipc.NewFileReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		schema = fr.Schema()
		for i := range fr.NumRecords() {
			rec, err := fr.RecordBatchAt(i)
			if err != nil {
				t.Fatal(err)
			}
			rec.Retain()
			recs = append(recs, rec)
		}
	} else {
		sr, err := ipc.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		schema = sr.Schema()
		for sr.Next() {
			rec := sr.RecordBatch()
			rec.Retain()
			recs = append(recs, rec)
		}
		if sr.Err() != nil {
			t.Fatal(sr.Err())
		}
	}
	for _, f := range schema.Fields() {
		if f.Type.ID() != arrow.NULL {
			t.Fatalf("field %q is %s, not Null", f.Name, f.Type)
		}
		names = append(names, f.Name)
	}
	for _, rec := range recs {
		rows += rec.NumRows()
	}
	return rows, names, recs
}

// TestNullRecordsReadInConstantSpace: each of nullInputs, through each entry point
// that reads its bytes, a path or arrow-go's records, is counted as arrow-go sums its
// records' rows, and collected, its head taken, a column selected, a slice taken and
// the frame concatenated with itself, each the rows arrow-go reads and every column
// Null and all null; the frame collected is counted in memory, concatenated with the
// scan, and exported to an Arrow record that is counted again; all of it inside the
// IPC targets' bounds, 64 MiB allocated and ten seconds, at 2^40 rows a record. Before step 529 the 2^40 input allocated until
// the harness ended the process.
func TestNullRecordsReadInConstantSpace(t *testing.T) {
	ctx := context.Background()
	t.Run("the 2^40 input", func(t *testing.T) {
		// Refused at its record, which step 519's walk finds corrupt, whatever is
		// asked of it.
		b := seed2e40(t)
		mem := &fuzzcheck.Allocator{Limit: ipcRequestLimit(len(b))}
		defer readBase(mem)()
		var count, collect, head error
		fuzzcheck.Run(t, ipcLimits(len(b), mem), func() error {
			_, count = ursus.ScanIPCStreamBytes(b, "seed").Count(ctx)
			_, collect = ursus.ScanIPCStreamBytes(b, "seed").Collect(ctx)
			_, head = ursus.ScanIPCStreamBytes(b, "seed").Head(1000).Collect(ctx)
			return nil
		})
		for name, err := range map[string]error{"Count": count, "Collect": collect, "Head": head} {
			if !errors.Is(err, ursus.ErrIO) || !strings.Contains(err.Error(), "is corrupt") {
				t.Fatalf("%s of the 2^40 input: %v, want it refused as corrupt", name, err)
			}
		}
	})
	for _, in := range nullInputs(t) {
		t.Run(in.name, func(t *testing.T) {
			want, names, recs := arrowNulls(t, in.b, in.file)
			path := filepath.Join(t.TempDir(), "in")
			if err := os.WriteFile(path, in.b, 0o600); err != nil {
				t.Fatal(err)
			}
			scans := map[string]func() *ursus.LazyFrame{
				"ScanArrowRecords": func() *ursus.LazyFrame { return ursus.ScanArrowRecords(recs...) },
			}
			if in.file {
				scans["ScanIPCBytes"] = func() *ursus.LazyFrame { return ursus.ScanIPCBytes(in.b, "in.arrow") }
				scans["ScanIPC"] = func() *ursus.LazyFrame { return ursus.ScanIPC(path) }
			} else {
				scans["ScanIPCStreamBytes"] = func() *ursus.LazyFrame { return ursus.ScanIPCStreamBytes(in.b, "in.arrows") }
				scans["ScanIPCStream"] = func() *ursus.LazyFrame { return ursus.ScanIPCStream(path) }
			}
			k, off, ln := int(min(want, 1000)), int(want/3), int(want/4)
			for name, scan := range scans {
				mem := &fuzzcheck.Allocator{Limit: ipcRequestLimit(len(in.b))}
				restore := readBase(mem)
				var (
					count, headCount, memCount, recCount int64
					df, head, sel, sliced, twice, mixed  *ursus.DataFrame
				)
				fuzzcheck.Run(t, ipcLimits(len(in.b), mem), func() error {
					var err error
					if count, err = scan().Count(ctx); err != nil {
						return err
					}
					if headCount, err = scan().Head(k).Count(ctx); err != nil {
						return err
					}
					if df, err = scan().Collect(ctx); err != nil {
						return err
					}
					if head, err = scan().Head(k).Collect(ctx); err != nil {
						return err
					}
					sel = df
					if len(names) > 0 {
						if sel, err = scan().Select(ursus.Col(names[len(names)-1]).Alias("m")).Collect(ctx); err != nil {
							return err
						}
					}
					if sliced, err = scan().Slice(off, ln).Collect(ctx); err != nil {
						return err
					}
					if twice, err = scan().Concat(scan()).Collect(ctx); err != nil {
						return err
					}
					// The frame in memory, counted, and concatenated with the scan; and
					// exported to Arrow, a Null array a column, and read back.
					if memCount, err = df.Lazy().Count(ctx); err != nil {
						return err
					}
					if mixed, err = df.Lazy().Head(k).Concat(scan(), df.Lazy()).Collect(ctx); err != nil {
						return err
					}
					rec, err := df.Record()
					if err != nil {
						return err
					}
					defer rec.Release()
					recCount, err = ursus.ScanArrowRecords(rec).Count(ctx)
					return err
				})
				restore()
				switch {
				case count != want:
					t.Fatalf("%s: Count is %d, and arrow-go's records hold %d rows", name, count, want)
				case headCount != int64(k):
					t.Fatalf("%s: Count of Head(%d) is %d", name, k, headCount)
				case int64(df.Height()) != want || df.Width() != len(names):
					t.Fatalf("%s: Collect is %d rows of %d columns, and arrow-go reads %d of %d",
						name, df.Height(), df.Width(), want, len(names))
				case head.Height() != k:
					t.Fatalf("%s: Head(%d) is %d rows", name, k, head.Height())
				case int64(sel.Height()) != want:
					t.Fatalf("%s: a Select is %d rows, and arrow-go reads %d", name, sel.Height(), want)
				case sliced.Height() != ln:
					t.Fatalf("%s: Slice(%d, %d) is %d rows", name, off, ln, sliced.Height())
				case int64(twice.Height()) != 2*want:
					t.Fatalf("%s: Concat with itself is %d rows, and arrow-go reads %d twice", name, twice.Height(), want)
				case memCount != want || recCount != want:
					t.Fatalf("%s: the frame in memory counts %d rows, and its Arrow record %d, of %d", name, memCount, recCount, want)
				case int64(mixed.Height()) != int64(k)+2*want:
					t.Fatalf("%s: the frame's head, the scan and the frame are %d rows", name, mixed.Height())
				}
				for _, f := range []*ursus.DataFrame{df, head, sel, sliced, twice, mixed} {
					if !nullFrame(f) {
						t.Fatalf("%s: a column read is not Null: %s", name, f.Schema())
					}
					if why := allNull(f); why != "" {
						t.Fatalf("%s: %s", name, why)
					}
				}
				for j, n := range df.Columns() {
					if n != names[j] {
						t.Fatalf("%s: columns %q, and arrow-go's fields %q", name, df.Columns(), names)
					}
				}
			}
		})
	}
}
