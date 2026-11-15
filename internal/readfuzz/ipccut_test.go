package readfuzz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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

// An IPC input cut short (step 534). A read error that wraps io.EOF is the engine's
// end of its input (every consumer tests errors.Is(err, io.EOF)), so an IPC read
// whose error wrapped io.EOF answered the rows before it, and no error: a compressed
// buffer whose bytes decompress short, which arrow-go reads with io.ReadFull and
// panics with its io.EOF; arrow-go's stream reader's error wrapping the same, which it
// recovered; and a stream ending inside a message, which ursus's message reader
// (internal/arrowmeta) answered as arrow-go's does, io.EOF where it read none of a
// part. Step 529's fuzzing found the first two, a file and a stream whose Collect and
// Count answered 0 rows where arrow-go refuses the bytes.

// The step 529 findings, as the fuzzer saved them, seeds of the IPC targets.
const ipcCutFinding = "finding-buffer-cut-short-read-as-the-end"

// batchBuffers is the field id of a RecordBatch's buffers (Message.fbs), a vector of
// structs of two int64s, a buffer's offset in its body and its length.
const batchBuffers, batchCompression = 2, 3

// TestAnIPCInputCutShortIsRefusedOrReadAsArrowGoReadsIt: the two findings are refused,
// by Collect and by Count, each with an ErrIO the engine does not take for the end;
// and every IPC file and stream in testdata/ipc, and the record cutRecord in each
// codec as arrow-go writes a file and a stream of it, cut after each of its bytes, is
// refused so, or read as arrow-go reads the same bytes (ipcCutRead). A stream cut
// between two messages ends there for arrow-go, which takes a stream's end marker to
// be optional, as the format does, and so for ursus; one cut inside a message is
// refused by ursus, where arrow-go reads one cut where a message, its metadata or
// its body begins as ending there.
func TestAnIPCInputCutShortIsRefusedOrReadAsArrowGoReadsIt(t *testing.T) {
	for _, file := range []bool{true, false} {
		target := "FuzzScanIPCStream"
		if file {
			target = "FuzzScanIPCFile"
		}
		b := corpusInput(t, filepath.Join("testdata", "fuzz", target, ipcCutFinding))
		if ipcCutRead(t, target+"'s "+ipcCutFinding, b, file) {
			t.Errorf("%s's %s is read; arrow-go refuses it", target, ipcCutFinding)
		}
	}
	reads, cuts := 0, 0
	for _, in := range ipcCutInputs(t) {
		for k := 0; k < len(in.b); k += flipStride {
			cuts++
			if ipcCutRead(t, fmt.Sprintf("%s (file %v) cut after %d of its %d bytes", in.name, in.file, k, len(in.b)),
				in.b[:k], in.file) {
				reads++
			}
		}
	}
	t.Logf("%d cuts, %d of them read as arrow-go reads them, the others refused", cuts, reads)
}

// TestAnIPCBufferCutShortIsRefusedOrReadAsArrowGoReadsIt: inside a record, where a
// cut of the whole input does not reach in a file, whose footer it cuts: each buffer
// of each compressed record batch and dictionary batch of each compressed file and
// stream of ipcCutInputs, its length declared shorter by each of its bytes, is
// refused, or read as arrow-go reads the same bytes (ipcCutRead). A buffer cut inside
// its eight bytes of decompressed length is refused by arrow-go, slicing past it; one
// cut after them decompresses short, which arrow-go reads with io.ReadFull and panics
// with io.EOF where it decompressed nothing, and io.ErrUnexpectedEOF otherwise; one
// whose cut leaves the codec's frame whole enough for every byte it declares reads.
func TestAnIPCBufferCutShortIsRefusedOrReadAsArrowGoReadsIt(t *testing.T) {
	reads, cuts := 0, 0
	for _, in := range ipcCutInputs(t) {
		lengths := compressedBufferLengths(in.b, in.file)
		if strings.Contains(in.name, "lz4") || strings.Contains(in.name, "zstd") || strings.Contains(in.name, "LZ4") ||
			strings.Contains(in.name, "Zstandard") {
			if len(lengths) == 0 {
				t.Fatalf("%s (file %v): no compressed buffer found", in.name, in.file)
			}
		}
		for _, at := range lengths {
			n := int64(binary.LittleEndian.Uint64(in.b[at:]))
			for k := int64(0); k < n; k += flipStride {
				b := bytes.Clone(in.b)
				binary.LittleEndian.PutUint64(b[at:], uint64(k))
				cuts++
				what := fmt.Sprintf("%s (file %v), its buffer of %d bytes whose length is at byte %d declared %d",
					in.name, in.file, n, at, k)
				if ipcCutRead(t, what, b, in.file) {
					reads++
				}
			}
		}
	}
	if cuts == 0 {
		t.Fatal("no buffer cut")
	}
	t.Logf("%d buffers cut short, %d of them read as arrow-go reads them, the others refused", cuts, reads)
}

// ipcCutRead holds ursus's read of b, an IPC file or stream cut short, described as
// what, to arrow-go's reading of the same bytes, under the same bounds (arrowRead),
// and says whether it read b. Where ursus reads a frame, arrow-go reads the bytes too,
// and the frame is what it reads, column for column, row for row and cell for cell
// (checkIPC), and Count is its height. Where ursus refuses b, Collect and Count each
// refuse it (refusal).
func ipcCutRead(t *testing.T, what string, b []byte, file bool) bool {
	t.Helper()
	ctx := t.Context()
	lf := ursus.ScanIPCStreamBytes(b, "cut.arrows")
	if file {
		lf = ursus.ScanIPCBytes(b, "cut.arrow")
	}
	df, err := lf.Collect(ctx)
	n, cerr := lf.Count(ctx)
	refusal(t, what+": Collect", err)
	refusal(t, what+": Count", cerr)
	if err != nil {
		// Count reads no column (step 370), so a column only ursus's own check of it
		// refuses, a view past its data buffer, is counted: as many rows as arrow-go's
		// reader reads the records to hold, or refused.
		if cerr == nil {
			if rows, rerr := arrowRows(b, file); rerr != nil || int64(rows) != n {
				t.Errorf("%s: Collect refuses it, and Count says %d rows, where arrow-go reads %d, %v: %v",
					what, n, rows, rerr, err)
			}
		}
		return false
	}
	if cerr != nil || n != int64(df.Height()) {
		t.Errorf("%s: Collect reads %d rows, and Count says %d, %v", what, df.Height(), n, cerr)
	}
	got, aerr := arrowRead(b, file)
	switch {
	case aerr != nil:
		t.Errorf("%s: read as %d rows, and no error, where arrow-go refuses the same bytes: %v", what, df.Height(), aerr)
		return true
	case got.rows != df.Height():
		t.Errorf("%s: read as %d rows, and arrow-go reads %d", what, df.Height(), got.rows)
		return true
	}
	t.Run(what, func(t *testing.T) { checkIPC(t, b, read{schema: df.Schema(), df: df}, file) })
	return true
}

// refusal fails t unless err, what's refusal of an input cut short or nil, is an
// error that does not wrap io.EOF, the engine's end of its input; nor is ursus's bug,
// ErrInternal; and is an ErrIO where it says the input is cut short, wrapping
// io.ErrUnexpectedEOF. A refusal of malformed Arrow data is an ErrValue
// (internal/arrowin), as for ScanArrow's records.
func refusal(t *testing.T, what string, err error) {
	t.Helper()
	switch {
	case err == nil:
	case errors.Is(err, io.EOF):
		t.Errorf("%s refuses it with %v, an error the engine takes for the end", what, err)
	case errors.Is(err, ursus.ErrInternal):
		t.Errorf("%s refuses it with %v, ursus's bug", what, err)
	case errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, ursus.ErrIO):
		t.Errorf("%s refuses it with %v, cut short and not an ErrIO", what, err)
	}
}

// arrowRows is how many rows b's record batches hold as arrow-go's reader reads them,
// under arrowRead's bounds, no array validated: an error where it refuses one.
func arrowRows(b []byte, file bool) (rows int, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("arrow-go panicked: %v", v)
		}
	}()
	mem := &fuzzcheck.Allocator{Limit: ipcRequestLimit(len(b))}
	opts := []ipc.Option{ipc.WithAllocator(mem)}
	if file {
		fr, err := ipc.NewFileReader(bytes.NewReader(b), opts...)
		if err != nil {
			return 0, err
		}
		defer fr.Close()
		for i := range fr.NumRecords() {
			rec, err := fr.RecordBatchAt(i)
			if err != nil {
				return 0, err
			}
			rows += int(rec.NumRows())
		}
		return rows, nil
	}
	limit := int64(max(len(b), 1))
	opts = append(opts, ipc.WithMetadataSizeLimit(limit), ipc.WithBodySizeLimit(limit))
	sr, err := ipc.NewReaderFromMessageReader(ipc.NewMessageReader(bytes.NewReader(b), opts...), opts...)
	if err != nil {
		return 0, err
	}
	defer sr.Release()
	for sr.Next() {
		rows += int(sr.RecordBatch().NumRows())
	}
	return rows, sr.Err()
}

// ipcCutInputs are the inputs cut short: every IPC file and stream in testdata/ipc,
// Polars 1.44.1's and 2.1's and pyarrow 25.0.1's, three of them in each codec; and
// cutRecord as arrow-go writes a file and a stream of it in each codec.
func ipcCutInputs(t *testing.T) []cleanInput {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "ipc", "*.arrow*"))
	if len(paths) == 0 {
		t.Fatal("no files in testdata/ipc")
	}
	var out []cleanInput
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, cleanInput{filepath.Base(p), b, strings.HasSuffix(p, ".arrow")})
	}
	rec := cutRecord(t)
	for _, file := range []bool{true, false} {
		for _, c := range []struct {
			name string
			opts []ipc.Option
		}{{"uncompressed", nil}, {"LZ4", []ipc.Option{ipc.WithLZ4()}}, {"Zstandard", []ipc.Option{ipc.WithZstd()}}} {
			out = append(out, cleanInput{"the cut record, " + c.name, writeIPC(t, file, rec, c.opts...), file})
		}
	}
	return out
}

// cutRecord is the kitchen sink's columns of a layout each: a bitmap, fixed widths,
// offsets and data, views and their variadic buffer, a list, a struct and a
// dictionary, which a stream sends before its record. It is the kitchen sink less
// most of its columns so that a cut after each byte is a few thousand reads.
func cutRecord(t testing.TB) arrow.RecordBatch {
	sink := kitchenSink(t)
	var fields []arrow.Field
	var cols []arrow.Array
	for i, f := range sink.Schema().Fields() {
		switch f.Name {
		case "bool", "i64", "f64", "utf8", "string_view", "list", "struct", "dict":
			fields = append(fields, f)
			cols = append(cols, sink.Column(i))
		}
	}
	if len(cols) != 8 {
		t.Fatalf("the kitchen sink has %d of the cut record's 8 columns", len(cols))
	}
	return array.NewRecordBatch(arrow.NewSchema(fields, nil), cols, sink.NumRows())
}

// compressedBufferLengths are where in b, an IPC file or stream, the length of each
// buffer of a compressed record batch or dictionary batch is: an int64, the second of
// each Buffer struct of the batch's buffers.
func compressedBufferLengths(b []byte, file bool) []int {
	var metas []fbuf
	if file {
		fb := fbuf(b)
		size, ok := fb.u32(uint32(len(b) - 10))
		if !ok || int(size) > len(b)-10 {
			return nil
		}
		footer := fb[len(b)-10-int(size) : len(b)-10]
		root, _ := footer.u32(0)
		for _, id := range []int{footerDictionaries, footerRecordBatches} {
			start, n, _ := footer.vector(root, id)
			for i := range n {
				if meta, ok := blockMessage(fb, footer, start+uint32(24*i)); ok {
					metas = append(metas, meta)
				}
			}
		}
	} else {
		for pos := 0; pos < len(b); {
			meta, next, ok := message(b, pos)
			if !ok {
				break
			}
			metas = append(metas, meta)
			pos = next
		}
	}
	var out []int
	for _, meta := range metas {
		// meta is a slice of b: where it starts in b is what of b's capacity it lacks.
		base := cap(b) - cap(meta)
		for _, r := range batchesOf(meta) {
			if _, ok := r.b.field(r.t, batchCompression); !ok {
				continue
			}
			start, n, ok := r.b.vector(r.t, batchBuffers)
			if !ok || r.b.past(start, n, 16) {
				continue
			}
			for i := range n {
				out = append(out, base+int(start)+16*i+8)
			}
		}
	}
	return out
}
