package readfuzz_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/endian"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
)

// refusals counts refusedAsCorrupt's reads.
var refusals int

// refusedAsCorrupt reads b as an IPC file or stream through every public entry point
// that opens one, each under the IPC targets' bounds, and fails t unless each refuses
// it as the walk does, KindIO, saying the input is corrupt; ReadIPCSchema, which reads
// no record batch, may answer where the forgery is in one.
func refusedAsCorrupt(t *testing.T, name string, b []byte, file bool) {
	t.Helper()
	mem := &fuzzcheck.Allocator{Limit: ipcRequestLimit(len(b))}
	defer readBase(mem)()
	ext := ".arrows"
	if file {
		ext = ".arrow"
	}
	path := filepath.Join(t.TempDir(), "forged"+ext)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	reads := map[string]func() error{}
	if file {
		reads["ScanIPCBytes"] = func() error { _, err := ursus.ScanIPCBytes(b, "forged.arrow").Collect(t.Context()); return err }
		reads["ScanIPC"] = func() error { _, err := ursus.ScanIPC(path).Collect(t.Context()); return err }
		reads["ScanIPCFiles"] = func() error { _, err := ursus.ScanIPCFiles([]string{path}).Collect(t.Context()); return err }
		reads["ReadIPCSchema"] = func() error { _, err := ursus.ReadIPCSchema(path); return err }
	} else {
		reads["ScanIPCStreamBytes"] = func() error {
			_, err := ursus.ScanIPCStreamBytes(b, "forged.arrows").Collect(t.Context())
			return err
		}
		reads["ScanIPCStream"] = func() error { _, err := ursus.ScanIPCStream(path).Collect(t.Context()); return err }
		reads["ScanIPCStreamFrom"] = func() error {
			_, err := ursus.ScanIPCStreamFrom([]ursus.IPCStreamFile{{Name: "forged", Open: func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(b)), nil
			}}}).Collect(t.Context())
			return err
		}
	}
	for entry, read := range reads {
		refusals++
		var err error
		fuzzcheck.Run(t, ipcLimits(len(b), mem), func() error { err = read(); return err })
		if err == nil && entry == "ReadIPCSchema" {
			continue // it reads the footer and the dictionaries, and no record batch
		}
		if !errors.Is(err, ursus.ErrIO) || !strings.Contains(err.Error(), "is corrupt") {
			t.Errorf("%s through %s: %v, not refused as corrupt", name, entry, err)
		}
	}
}

// TestForgedIPCMetadataIsRefused: IPC metadata declaring a count past its bytes, of
// a schema's fields, a field's children, a schema's custom metadata or a view
// column's variadic buffers, and a field its own child, each patched into a file and
// a stream of the kitchen sink or of custom metadata as the IPC targets' seeds are
// (patchMeta), is refused through every public entry point that opens IPC, within
// fuzzcheck's bounds (step 519).
func TestForgedIPCMetadataIsRefused(t *testing.T) {
	for _, file := range []bool{true, false} {
		sink := writeIPC(t, file, kitchenSink(t))
		withMeta := writeIPC(t, file, metadataRecord(t))
		for _, p := range []struct {
			name string
			b    []byte
		}{
			{"fields", patchMeta(sink, file, "fields", 0x7fffffff)},
			{"children", patchMeta(sink, file, "children", 0x7fffffff)},
			{"metadata", patchMeta(withMeta, file, "metadata", 0x7fffffff)},
			{"variadic", patchMeta(sink, file, "variadic", 1<<40)},
			{"cycle", patchMeta(sink, file, "cycle", 0)},
		} {
			refusedAsCorrupt(t, p.name, p.b, file)
		}
	}
	t.Logf("%d reads refused", refusals)
}

// cleanInput is a well-formed IPC input: a file's bytes or a stream's.
type cleanInput struct {
	name string
	b    []byte
	file bool
}

// cleanInputs are the IPC inputs the walk must pass, for file and stream both:
// testdata/ipc's files, Polars 1.44.1's, and Polars 2.1's and pyarrow 25.0.1's with
// Null columns (step 522); step 511's kitchen sink in each codec, a List 60 deep,
// 1,000 integers and custom metadata, as arrow-go writes them; arrow-go's big-endian
// writing of the kitchen sink's fixed-width columns; and what ursus's own writer
// writes of a frame of every type it has, in each codec.
func cleanInputs(t *testing.T) []cleanInput {
	t.Helper()
	var out []cleanInput
	paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "ipc", "*.arrow*"))
	if len(paths) == 0 {
		t.Fatal("no files in testdata/ipc")
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, cleanInput{filepath.Base(p), b, strings.HasSuffix(p, ".arrow")})
	}
	sink := kitchenSink(t)
	frame := everyType(t, sink)
	for _, file := range []bool{true, false} {
		for _, c := range []struct {
			name string
			opts []ipc.Option
			ours []ursus.IPCSinkOption
		}{
			{"uncompressed", nil, nil},
			{"LZ4", []ipc.Option{ipc.WithLZ4()}, []ursus.IPCSinkOption{ursus.WithIPCCompression(ursus.IPCLZ4)}},
			{"Zstandard", []ipc.Option{ipc.WithZstd()}, []ursus.IPCSinkOption{ursus.WithIPCCompression(ursus.IPCZstd)}},
		} {
			out = append(out, cleanInput{"the kitchen sink, " + c.name, writeIPC(t, file, sink, c.opts...), file})
			var buf bytes.Buffer
			write := frame.Lazy().WriteIPCStream
			if file {
				write = frame.Lazy().WriteIPC
			}
			if err := write(t.Context(), &buf, c.ours...); err != nil {
				t.Fatal(err)
			}
			out = append(out, cleanInput{"ursus's writing of every type, " + c.name, buf.Bytes(), file})
		}
		out = append(out,
			cleanInput{"a List 60 deep", writeIPC(t, file, deepRecord(t, 60)), file},
			cleanInput{"1,000 integers", writeIPC(t, file, intRecord(t)), file},
			cleanInput{"custom metadata", writeIPC(t, file, metadataRecord(t)), file},
			cleanInput{"the kitchen sink's fixed widths, big-endian", writeIPC(t, file, bigEndian(t, sink)), file})
	}
	return out
}

// everyType is sink read by ursus, with a column of each of ursus's types the kitchen
// sink's Arrow types do not read as, that ursus writes to Arrow: an Int128, a
// Categorical, a struct of a list, text and a Categorical, a Null column and a struct
// of a Null field. An Enum has no Arrow type in ursus yet. The Null columns read back
// since step 522: arrow-go's IPC reader makes a Null array of no buffers, which its own
// array.Validate, which ursus calls, refused (a finding of step 519).
func everyType(t *testing.T, sink arrow.RecordBatch) *ursus.DataFrame {
	t.Helper()
	c := ursus.Col
	df, err := ursus.ScanArrowRecords(sink).WithColumns(
		c("i64").Cast(ursus.Int128).Alias("int128"),
		c("utf8").Cast(ursus.Categorical).Alias("categorical"),
		ursus.Struct(c("list"), c("utf8").Alias("text"), c("utf8").Cast(ursus.Categorical).Alias("cat")).Alias("nested"),
		ursus.Null(ursus.NullT).Alias("null"),
		ursus.Struct(ursus.Null(ursus.NullT).Alias("z"), c("i64")).Alias("null_field"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df
}

// bigEndian is the fixed-width columns of rec as a big-endian writer holds them, each
// value's bytes reversed, a Decimal128's sixteen whole as arrow-go's reader reverses
// them back (ipc's swapType), and the schema saying so.
func bigEndian(t *testing.T, rec arrow.RecordBatch) arrow.RecordBatch {
	t.Helper()
	var fields []arrow.Field
	var cols []arrow.Array
	for i, f := range rec.Schema().Fields() {
		fw, ok := f.Type.(arrow.FixedWidthDataType)
		if !ok || f.Type.ID() == arrow.DICTIONARY || f.Type.ID() == arrow.FIXED_SIZE_BINARY {
			continue
		}
		d := rec.Column(i).Data()
		bufs := make([]*memory.Buffer, len(d.Buffers()))
		for k, b := range d.Buffers() {
			if b != nil {
				bufs[k] = memory.NewBufferBytes(bytes.Clone(b.Bytes()))
			}
		}
		if width := fw.BitWidth() / 8; width > 1 {
			v := bufs[1].Bytes()
			for at := 0; at+width <= len(v); at += width {
				for l, r := at, at+width-1; l < r; l, r = l+1, r-1 {
					v[l], v[r] = v[r], v[l]
				}
			}
		}
		fields = append(fields, f)
		cols = append(cols, array.MakeFromData(array.NewData(f.Type, d.Len(), bufs, nil, d.NullN(), d.Offset())))
	}
	if len(cols) < 10 {
		t.Fatalf("the kitchen sink has %d fixed-width columns", len(cols))
	}
	return array.NewRecordBatch(arrow.NewSchemaWithEndian(fields, nil, endian.BigEndian), cols, rec.NumRows())
}

// TestTheWalkPassesCleanInputs: no well-formed input is refused (step 519). Each of
// cleanInputs is read by ScanIPCBytes or ScanIPCStreamBytes, every footer and message
// walked, under the IPC targets' bounds, and its frame is what arrow-go reads of the
// same bytes, cell for cell (checkIPC). A walk that refused one, or read one otherwise,
// fails here, as TestOversizedPassesCleanInputs holds the targets' own walk.
func TestTheWalkPassesCleanInputs(t *testing.T) {
	files, streams := 0, 0
	for _, in := range cleanInputs(t) {
		mem := &fuzzcheck.Allocator{Limit: ipcRequestLimit(len(in.b))}
		restore := readBase(mem)
		var r read
		fuzzcheck.Run(t, ipcLimits(len(in.b), mem), func() error {
			if in.file {
				return r.of(t, ursus.ScanIPCBytes(in.b, in.name))
			}
			return r.of(t, ursus.ScanIPCStreamBytes(in.b, in.name))
		})
		restore()
		if r.schemaErr != nil || r.err != nil {
			t.Fatalf("%s (file %v) is refused: %v", in.name, in.file, firstErr(r.schemaErr, r.err))
		}
		if r.df.Height() == 0 && !strings.Contains(in.name, "polars") && !strings.Contains(in.name, "_empty") {
			t.Fatalf("%s (file %v): no rows read", in.name, in.file)
		}
		checkIPC(t, in.b, r, in.file)
		if in.file {
			files++
		} else {
			streams++
		}
	}
	t.Logf("%d files and %d streams read as arrow-go reads them", files, streams)
}
