package readfuzz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/float16"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/fuzzcheck"
)

// FuzzScanIPCFile: any bytes, read as an Arrow IPC file through ScanIPCBytes, are a
// frame or an error within fuzzcheck's limits (step 511, the plan's 450), every
// buffer arrow-go allocates for the read asked of a bounded allocator, which may
// grant no request past what the input could hold decompressed (arrowin.ReadLimit).
// A frame read is held to arrow-go's own reading of the bytes: the same columns, the
// same rows, each cell the value arrow-go's arrays hold (checkIPC). One input in
// eight is also written to a file and read by ScanIPC and ScanIPCFiles, which must
// answer as the bytes did, and by ReadIPCSchema, which must equal CollectSchema.
func FuzzScanIPCFile(f *testing.F) {
	for _, b := range ipcSeeds(f, true) {
		f.Add(b)
	}
	f.Fuzz(scanIPCFileOne)
}

// scanIPCFileOne is FuzzScanIPCFile's check of one input.
func scanIPCFileOne(t *testing.T, b []byte) {
	if why := oversized(b, true); why != "" {
		t.Skip("finding: " + why)
	}
	mem := &fuzzcheck.Allocator{Limit: ipcRequestLimit(len(b))}
	defer readBase(mem)()
	var r read
	fuzzcheck.Run(t, ipcLimits(len(b), mem), func() error {
		return r.of(t, ursus.ScanIPCBytes(b, "fuzz.arrow"))
	})
	checkIPC(t, b, r, true)
	if crc32.ChecksumIEEE(b)%8 != 0 {
		return
	}
	path := filepath.Join(t.TempDir(), "fuzz.arrow")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var byPath, byFiles read
	var schema *ursus.Schema
	var schemaErr error
	fuzzcheck.Run(t, ipcLimits(len(b), mem), func() error {
		byPath.of(t, ursus.ScanIPC(path))
		byFiles.of(t, ursus.ScanIPCFiles([]string{path}))
		schema, schemaErr = ursus.ReadIPCSchema(path)
		return firstErr(byPath.schemaErr, byPath.err, byFiles.schemaErr, byFiles.err, schemaErr)
	})
	for _, other := range []struct {
		name string
		r    read
	}{{"ScanIPC", byPath}, {"ScanIPCFiles", byFiles}} {
		if why := sameRead(other.r, r); why != "" {
			t.Fatalf("%s of the file reads otherwise than ScanIPCBytes of its bytes: %s", other.name, why)
		}
	}
	switch {
	case (schemaErr == nil) != (byPath.schemaErr == nil):
		t.Fatalf("ReadIPCSchema answers %v and ScanIPC's CollectSchema %v", schemaErr, byPath.schemaErr)
	case schemaErr == nil && !schema.Equal(byPath.schema):
		t.Fatalf("ReadIPCSchema is %s and ScanIPC's CollectSchema %s", schema, byPath.schema)
	}
}

// FuzzScanIPCStream: FuzzScanIPCFile's checks of an Arrow IPC stream, read through
// ScanIPCStreamBytes and, one input in eight, ScanIPCStream of a file.
func FuzzScanIPCStream(f *testing.F) {
	for _, b := range ipcSeeds(f, false) {
		f.Add(b)
	}
	f.Fuzz(scanIPCStreamOne)
}

// scanIPCStreamOne is FuzzScanIPCStream's check of one input.
func scanIPCStreamOne(t *testing.T, b []byte) {
	if why := oversized(b, false); why != "" {
		t.Skip("finding: " + why)
	}
	mem := &fuzzcheck.Allocator{Limit: ipcRequestLimit(len(b))}
	defer readBase(mem)()
	var r read
	fuzzcheck.Run(t, ipcLimits(len(b), mem), func() error {
		return r.of(t, ursus.ScanIPCStreamBytes(b, "fuzz.arrows"))
	})
	checkIPC(t, b, r, false)
	if crc32.ChecksumIEEE(b)%8 != 0 {
		return
	}
	path := filepath.Join(t.TempDir(), "fuzz.arrows")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var byPath read
	fuzzcheck.Run(t, ipcLimits(len(b), mem), func() error {
		return byPath.of(t, ursus.ScanIPCStream(path))
	})
	if why := sameRead(byPath, r); why != "" {
		t.Fatalf("ScanIPCStream of the file reads otherwise than ScanIPCStreamBytes of its bytes: %s", why)
	}
}

// TestScanIPCDeepSchema is FuzzScanIPCFile's and FuzzScanIPCStream's check of a
// schema nested 1,000 deep, of no record: an input of 44 to 88 KB, which the fuzzer
// would spend its time minimizing, so an ordinary test.
func TestScanIPCDeepSchema(t *testing.T) {
	rec := deepRecord(t, 1000).NewSlice(0, 0)
	t.Run("file", func(t *testing.T) { scanIPCFileOne(t, writeIPC(t, true, rec)) })
	t.Run("stream", func(t *testing.T) { scanIPCStreamOne(t, writeIPC(t, false, rec)) })
}

// TestReadIPCSchemaRecovers: a footer one byte of whose schema is changed makes
// arrow-go's flatbuffers accessors panic, and ReadIPCSchema, which no query's
// recovery wraps, let the panic reach its caller (step 511, found by
// FuzzScanIPCFile). It is an error, the file's, now.
func TestReadIPCSchemaRecovers(t *testing.T) {
	b := array.NewInt8Builder(memory.NewGoAllocator())
	b.Append(1)
	col := b.NewArray()
	schema := arrow.NewSchema([]arrow.Field{{Name: "x", Type: arrow.PrimitiveTypes.Int8}}, nil)
	file := writeIPC(t, true, array.NewRecordBatch(schema, []arrow.Array{col}, 1))
	file[len(file)-10] = 0x40 // in the footer's schema, the field's name
	path := filepath.Join(t.TempDir(), "corrupt.arrow")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	fuzzcheck.Run(t, limits(len(file)), func() error {
		_, err = ursus.ReadIPCSchema(path)
		return err
	})
	if !errors.Is(err, ursus.ErrIO) {
		t.Fatalf("ReadIPCSchema of a corrupt footer: %v, want an ErrIO", err)
	}
}

// TestOversizedPassesCleanInputs: the walk that skips oversized's finding skips no
// well-formed input, so the IPC targets are not made vacuous by it: the Polars files,
// the kitchen sink in each codec, a deep schema, and the patched seeds' original.
func TestOversizedPassesCleanInputs(t *testing.T) {
	for _, file := range []bool{true, false} {
		ext := ".arrows"
		if file {
			ext = ".arrow"
		}
		paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "ipc", "*"+ext))
		inputs := map[string][]byte{}
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			inputs[filepath.Base(p)] = b
		}
		sink := kitchenSink(t)
		inputs["kitchen sink"] = writeIPC(t, file, sink)
		inputs["kitchen sink, LZ4"] = writeIPC(t, file, sink, ipc.WithLZ4())
		inputs["kitchen sink, Zstandard"] = writeIPC(t, file, sink, ipc.WithZstd())
		inputs["a List 60 deep"] = writeIPC(t, file, deepRecord(t, 60))
		inputs["1,000 integers"] = writeIPC(t, file, intRecord(t))
		inputs["custom metadata"] = writeIPC(t, file, metadataRecord(t))
		if len(paths) == 0 {
			t.Fatalf("no %s files in testdata/ipc", ext)
		}
		for name, b := range inputs {
			if why := oversized(b, file); why != "" {
				t.Errorf("%s (file %v) is skipped as oversized: %s", name, file, why)
			}
		}
	}
}

// ipcRequestLimit is the bounded allocator's limit for an IPC input of n bytes: what
// ursus's own allocator grants, arrowin.ReadLimit, so that a request it lets past is
// refused here.
func ipcRequestLimit(n int) int { return int(min(arrowin.ReadLimit(int64(n)), int64(math.MaxInt))) }

// ipcLimits bounds one read of an IPC input of n bytes: readfuzz's limits, plus
// twice the largest buffer a read of it may be granted, which a buffer declaring a
// decompressed length below arrowin.ReadLimit is, before its bytes are found short.
func ipcLimits(n int, mem *fuzzcheck.Allocator) fuzzcheck.Limits {
	lim := limits(n)
	lim.Alloc += 2 * int64(mem.Limit)
	lim.Mem = mem
	return lim
}

// readBase makes mem the allocator every IPC read's granted requests come from, and
// returns what puts the last one back.
func readBase(mem memory.Allocator) func() {
	old := arrowin.ReadBase
	arrowin.ReadBase = mem
	return func() { arrowin.ReadBase = old }
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// sameRead says how a differs from b, two reads of the same bytes: "" for not at all.
func sameRead(a, b read) string {
	switch {
	case (a.schemaErr == nil) != (b.schemaErr == nil):
		return fmt.Sprintf("a schema %v, and %v", a.schemaErr, b.schemaErr)
	case a.schemaErr != nil:
		return ""
	case !a.schema.Equal(b.schema):
		return fmt.Sprintf("the schema %s, and %s", a.schema, b.schema)
	case (a.df == nil) != (b.df == nil):
		return fmt.Sprintf("a frame %v, and %v", a.err, b.err)
	case a.df == nil:
		return ""
	case a.df.Height() != b.df.Height():
		return fmt.Sprintf("%d rows, and %d", a.df.Height(), b.df.Height())
	}
	for i := range a.df.Height() {
		x, err := a.df.Row(i)
		if err != nil {
			return err.Error()
		}
		y, err := b.df.Row(i)
		if err != nil {
			return err.Error()
		}
		for j := range x {
			if !sameValue(x[j], y[j]) {
				return fmt.Sprintf("row %d, column %d: %v, and %v", i, j, x[j], y[j])
			}
		}
	}
	return ""
}

// checkIPC holds r, ursus's read of b, an IPC file or stream, to arrow-go's reading
// of the same bytes: where ursus reads a frame, arrow-go reads every record, under the
// same bounds, and ursus's columns are arrow-go's fields, its rows theirs, and each
// cell the value of arrow-go's array (cellOf). A type whose values the target does
// not compare, a decimal, is compared null for null.
func checkIPC(t *testing.T, b []byte, r read, file bool) {
	t.Helper()
	if r.df == nil {
		return
	}
	got, err := arrowRead(b, file)
	if err != nil {
		t.Fatalf("ursus reads %d rows of %d bytes arrow-go reads as: %v", r.df.Height(), len(b), err)
	}
	names := r.df.Columns()
	if len(names) != len(got.names) || strings.Join(names, "\x00") != strings.Join(got.names, "\x00") {
		t.Fatalf("ursus reads columns %q, and arrow-go fields %q", names, got.names)
	}
	if r.df.Height() != got.rows {
		t.Fatalf("ursus reads %d rows, and arrow-go %d", r.df.Height(), got.rows)
	}
	for i := range got.rows {
		row, err := r.df.Row(i)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		for j := range names {
			if want := got.cols[j][i]; !sameValue(row[j], want) {
				t.Fatalf("row %d, column %q (%s): ursus reads %#v, and arrow-go holds %#v",
					i, names[j], r.df.Schema().Field(j).Type, row[j], want)
			}
		}
	}
}

// arrowed is an IPC input as arrow-go reads it: its fields' names, its rows, and
// each column's cells as cellOf gives them.
type arrowed struct {
	names []string
	rows  int
	cols  [][]any
}

// arrowRead reads b as arrow-go does, through the bounds ursus reads it under: an
// allocator granting no request past arrowin.ReadLimit, and a stream's messages no
// larger than the stream. A panic in arrow-go, reading or in an array's accessor, is
// its error.
func arrowRead(b []byte, file bool) (out arrowed, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("arrow-go panicked: %v", v)
		}
	}()
	mem := &fuzzcheck.Allocator{Limit: ipcRequestLimit(len(b))}
	opts := []ipc.Option{ipc.WithAllocator(mem)}
	var schema *arrow.Schema
	var next func() (arrow.RecordBatch, error)
	if file {
		fr, err := ipc.NewFileReader(bytes.NewReader(b), opts...)
		if err != nil {
			return out, err
		}
		defer fr.Close()
		schema = fr.Schema()
		i := 0
		next = func() (arrow.RecordBatch, error) {
			if i >= fr.NumRecords() {
				return nil, nil
			}
			i++
			return fr.RecordBatchAt(i - 1)
		}
	} else {
		limit := int64(max(len(b), 1))
		opts = append(opts, ipc.WithMetadataSizeLimit(limit), ipc.WithBodySizeLimit(limit))
		sr, err := ipc.NewReaderFromMessageReader(ipc.NewMessageReader(bytes.NewReader(b), opts...), opts...)
		if err != nil {
			return out, err
		}
		defer sr.Release()
		schema = sr.Schema()
		next = func() (arrow.RecordBatch, error) {
			if !sr.Next() {
				return nil, sr.Err()
			}
			return sr.RecordBatch(), nil
		}
	}
	for _, fd := range schema.Fields() {
		out.names = append(out.names, fd.Name)
	}
	out.cols = make([][]any, len(out.names))
	for {
		rec, err := next()
		if err != nil {
			return out, err
		}
		if rec == nil {
			return out, nil
		}
		for j := range out.cols {
			col := rec.Column(j)
			if err := array.Validate(col); err != nil {
				return out, err
			}
			for i := range int(rec.NumRows()) {
				out.cols[j] = append(out.cols[j], cellOf(col, i))
			}
		}
		out.rows += int(rec.NumRows())
	}
}

// uncompared stands for a value the target does not compare.
type uncompared struct{}

// epoch is an instant or a date as arrow-go holds it: v times unit since 1970.
type epoch struct {
	v    int64
	unit time.Duration
}

// cellOf is row i of arr as DataFrame.Row gives a cell of the type arrowin reads arr
// as: a Go integer, float, bool or string of the width, a []byte, a time.Duration;
// an epoch for a Date or a Datetime, which Row gives as a time.Time; a []any of a
// list's elements and a map[string]any of a struct's fields; a dictionary's value;
// nil for a null; and uncompared for a value not compared.
func cellOf(arr arrow.Array, i int) any {
	if arr.IsNull(i) {
		return nil
	}
	switch a := arr.(type) {
	case *array.Boolean:
		return a.Value(i)
	case *array.Int8:
		return a.Value(i)
	case *array.Int16:
		return a.Value(i)
	case *array.Int32:
		return a.Value(i)
	case *array.Int64:
		return a.Value(i)
	case *array.Uint8:
		return a.Value(i)
	case *array.Uint16:
		return a.Value(i)
	case *array.Uint32:
		return a.Value(i)
	case *array.Uint64:
		return a.Value(i)
	case *array.Float16:
		return half(a.Value(i).Uint16())
	case *array.Float32:
		return a.Value(i)
	case *array.Float64:
		return a.Value(i)
	case *array.String:
		return a.Value(i)
	case *array.LargeString:
		return a.Value(i)
	case *array.StringView:
		return a.Value(i)
	case *array.Binary:
		return bytes.Clone(a.Value(i))
	case *array.LargeBinary:
		return bytes.Clone(a.Value(i))
	case *array.BinaryView:
		return bytes.Clone(a.Value(i))
	case *array.FixedSizeBinary:
		return bytes.Clone(a.Value(i))
	case *array.Date32:
		return epoch{int64(a.Value(i)), 24 * time.Hour}
	case *array.Date64:
		return epoch{int64(a.Value(i)), time.Millisecond}
	case *array.Timestamp:
		return epoch{int64(a.Value(i)), a.DataType().(*arrow.TimestampType).Unit.Multiplier()}
	case *array.Time32:
		return time.Duration(int64(a.Value(i)) * int64(a.DataType().(*arrow.Time32Type).Unit.Multiplier()))
	case *array.Time64:
		return time.Duration(int64(a.Value(i)) * int64(a.DataType().(*arrow.Time64Type).Unit.Multiplier()))
	case *array.Duration:
		return time.Duration(int64(a.Value(i)) * int64(a.DataType().(*arrow.DurationType).Unit.Multiplier()))
	case array.ListLike:
		lo, hi := a.ValueOffsets(i)
		vals := a.ListValues()
		out := make([]any, 0, max(hi-lo, 0))
		for k := lo; k < hi; k++ {
			out = append(out, cellOf(vals, int(k)))
		}
		return out
	case *array.Struct:
		st := a.DataType().(*arrow.StructType)
		out := make(map[string]any, st.NumFields())
		for k, fd := range st.Fields() {
			if _, dup := out[fd.Name]; dup {
				return uncompared{}
			}
			out[fd.Name] = cellOf(a.Field(k), i)
		}
		return out
	case *array.Dictionary:
		return cellOf(a.Dictionary(), a.GetValueIndex(i))
	case array.ExtensionArray:
		return cellOf(a.Storage(), i)
	}
	return uncompared{}
}

// half is an IEEE 754 binary16's value, by the standard's definition: arrow-go's
// float16.Num.Float32 reads a subnormal's fraction as a float32 subnormal's.
func half(h uint16) float32 {
	sign := float64(1)
	if h>>15 == 1 {
		sign = -1
	}
	exp, frac := int(h>>10&0x1f), float64(h&0x3ff)
	switch exp {
	case 0:
		return float32(sign * math.Ldexp(frac, -24))
	case 0x1f:
		if frac == 0 {
			return float32(math.Inf(int(sign)))
		}
		return float32(math.NaN())
	}
	return float32(sign * math.Ldexp(1024+frac, exp-25))
}

// sameValue is whether got, a cell DataFrame.Row gives, is want, a cell of the same
// value: floats by their bits, NaN as NaN; an epoch as the instant a time.Time is;
// uncompared as anything not null.
func sameValue(got, want any) bool {
	switch w := want.(type) {
	case nil:
		return got == nil
	case uncompared:
		return got != nil
	case float32:
		g, ok := got.(float32)
		return ok && (math.Float32bits(g) == math.Float32bits(w) || g != g && w != w)
	case float64:
		g, ok := got.(float64)
		return ok && (math.Float64bits(g) == math.Float64bits(w) || g != g && w != w)
	case []byte:
		g, ok := got.([]byte)
		return ok && bytes.Equal(g, w)
	case epoch:
		g, ok := got.(time.Time)
		if !ok {
			return false
		}
		have := new(big.Int).Mul(big.NewInt(g.Unix()), big.NewInt(1e9))
		have.Add(have, big.NewInt(int64(g.Nanosecond())))
		return have.Cmp(new(big.Int).Mul(big.NewInt(w.v), big.NewInt(int64(w.unit)))) == 0
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k := range w {
			if !sameValue(g[k], w[k]) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, v := range w {
			if gv, ok := g[k]; !ok || !sameValue(gv, v) {
				return false
			}
		}
		return true
	}
	return got == want
}

// ipcSeeds are the IPC targets' seeds: Polars-written files from testdata/ipc; a
// kitchen sink of every type arrowin reads, a ListView among them, uncompressed, LZ4
// and Zstandard; schemas nested past and short of arrow-go's 64 levels; and inputs
// whose declared lengths are past what they hold: a footer block's body, a message's
// body and metadata, a buffer's length, and a compressed buffer's decompressed
// length, each 2^40.
func ipcSeeds(f *testing.F, file bool) [][]byte {
	var seeds [][]byte
	ext := ".arrows"
	if file {
		ext = ".arrow"
	}
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "ipc", "*"+ext))
	if err != nil || len(paths) == 0 {
		f.Fatalf("no %s files in testdata/ipc: %v", ext, err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		seeds = append(seeds, b)
	}
	sink := kitchenSink(f)
	for _, opts := range [][]ipc.Option{nil, {ipc.WithLZ4()}, {ipc.WithZstd()}} {
		seeds = append(seeds, writeIPC(f, file, sink, opts...))
	}
	// A List nested 60 deep, with a row; and 70 deep, with none, as arrow-go's
	// writer writes no record nested past its 64 levels, and its reader loads none:
	// the schema alone is read, by arrow-go and by arrowin. TestScanIPCDeepSchema
	// reads one 1,000 deep.
	seeds = append(seeds, writeIPC(f, file, deepRecord(f, 60)))
	seeds = append(seeds, writeIPC(f, file, deepRecord(f, 70).NewSlice(0, 0)))
	ints := intRecord(f)
	plain := writeIPC(f, file, ints)
	seeds = append(seeds, plain[:len(plain)/2], []byte("ARROW1\x00\x00"), nil)
	// The body of each of the two batches is one buffer of 8,000 bytes, its length
	// written in its message's metadata, as the message's body length, and in a
	// file's footer block; and, compressed, as the buffer's decompressed length.
	const huge = 1 << 40
	body := int64(ints.NumRows()) * 8
	for n := range 3 {
		if p := patchNth(plain, body, n, huge); p != nil {
			seeds = append(seeds, p)
		}
	}
	seeds = append(seeds, patchLast(plain, body, huge))
	if !file {
		// The second message's metadata length.
		meta := bytes.Clone(plain)
		at := bytes.Index(meta[8:], []byte{0xff, 0xff, 0xff, 0xff}) + 8
		binary.LittleEndian.PutUint32(meta[at+4:], 0x7fffffff)
		seeds = append(seeds, meta)
	}
	for _, codec := range []ipc.Option{ipc.WithLZ4(), ipc.WithZstd()} {
		// A compressed buffer's first eight bytes are its decompressed length.
		seeds = append(seeds, patchNth(writeIPC(f, file, ints, codec), body, 0, huge))
	}
	// Lengths the metadata declares past its buffer, which oversized's finding
	// skips: a schema's fields, a struct's children, the custom metadata of a
	// schema, and a view column's variadic buffers; and a field its own child.
	withMeta := writeIPC(f, file, metadataRecord(f))
	sink0 := writeIPC(f, file, sink)
	for _, p := range [][]byte{
		patchMeta(sink0, file, "fields", 0x7fffffff),
		patchMeta(sink0, file, "children", 0x7fffffff),
		patchMeta(withMeta, file, "metadata", 0x7fffffff),
		patchMeta(sink0, file, "variadic", huge),
		patchMeta(sink0, file, "cycle", 0),
	} {
		if p == nil {
			f.Fatal("a seed's length was not found to patch")
		}
		seeds = append(seeds, p)
	}
	return seeds
}

// patchNth is b with the n-th little-endian int64 old, counted from 0, made new.
func patchNth(b []byte, old int64, n int, new int64) []byte {
	var want [8]byte
	binary.LittleEndian.PutUint64(want[:], uint64(old))
	at := -1
	for range n + 1 {
		k := bytes.Index(b[at+1:], want[:])
		if k < 0 {
			return nil
		}
		at += 1 + k
	}
	out := bytes.Clone(b)
	binary.LittleEndian.PutUint64(out[at:], uint64(new))
	return out
}

// patchLast is b with its last little-endian int64 old made new.
func patchLast(b []byte, old, new int64) []byte {
	var want [8]byte
	binary.LittleEndian.PutUint64(want[:], uint64(old))
	at := bytes.LastIndex(b, want[:])
	out := bytes.Clone(b)
	binary.LittleEndian.PutUint64(out[at:], uint64(new))
	return out
}

// writeIPC is rec written as an IPC file or stream, twice, two batches. (arrow-go's
// writer refuses a slice of a ListView column, so the batches are not halves.)
func writeIPC(f testing.TB, file bool, rec arrow.RecordBatch, opts ...ipc.Option) []byte {
	var buf bytes.Buffer
	opts = append(opts, ipc.WithSchema(rec.Schema()))
	var w interface {
		Write(arrow.RecordBatch) error
		Close() error
	}
	if file {
		fw, err := ipc.NewFileWriter(&buf, opts...)
		if err != nil {
			f.Fatal(err)
		}
		w = fw
	} else {
		w = ipc.NewWriter(&buf, opts...)
	}
	for range 2 {
		if rec.NumRows() == 0 {
			break
		}
		if err := w.Write(rec); err != nil {
			f.Fatalf("writing %s: %v", rec.Schema(), err)
		}
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}
	return buf.Bytes()
}

// intRecord is a record of one Int64 column of 1,000 values, no nulls: its body is
// one buffer of 8,000 bytes, whose length the patched seeds find and change.
func intRecord(f testing.TB) arrow.RecordBatch {
	b := array.NewInt64Builder(memory.NewGoAllocator())
	for i := range 1000 {
		b.Append(int64(i) * 7)
	}
	col := b.NewArray()
	schema := arrow.NewSchema([]arrow.Field{{Name: "x", Type: arrow.PrimitiveTypes.Int64}}, nil)
	return array.NewRecordBatch(schema, []arrow.Array{col}, int64(col.Len()))
}

// deepRecord is a record of one column, a List nested depth deep, of one row.
func deepRecord(f testing.TB, depth int) arrow.RecordBatch {
	t := arrow.DataType(arrow.PrimitiveTypes.Int8)
	for range depth {
		t = arrow.ListOf(t)
	}
	js := strings.Repeat("[", depth) + "1" + strings.Repeat("]", depth)
	col, _, err := array.FromJSON(memory.NewGoAllocator(), t, strings.NewReader("["+js+"]"))
	if err != nil {
		f.Fatal(err)
	}
	schema := arrow.NewSchema([]arrow.Field{{Name: "deep", Type: t, Nullable: true}}, nil)
	return array.NewRecordBatch(schema, []arrow.Array{col}, 1)
}

// metadataRecord is intRecord with custom metadata on its schema.
func metadataRecord(f testing.TB) arrow.RecordBatch {
	rec := intRecord(f)
	md := arrow.NewMetadata([]string{"origin"}, []string{"step 511"})
	return array.NewRecordBatch(arrow.NewSchema(rec.Schema().Fields(), &md), rec.Columns(), rec.NumRows())
}

// kitchenSink is a record of a column of each type arrowin reads, four rows each,
// a null among them.
func kitchenSink(f testing.TB) arrow.RecordBatch {
	mem := memory.NewGoAllocator()
	i64 := arrow.PrimitiveTypes.Int64
	str := arrow.BinaryTypes.String
	cols := []struct {
		name string
		t    arrow.DataType
		js   string
	}{
		{"bool", arrow.FixedWidthTypes.Boolean, `[true, null, false, true]`},
		{"i8", arrow.PrimitiveTypes.Int8, `[1, null, -128, 127]`},
		{"i16", arrow.PrimitiveTypes.Int16, `[1, null, -32768, 32767]`},
		{"i32", arrow.PrimitiveTypes.Int32, `[1, null, -2147483648, 2147483647]`},
		{"i64", i64, `[1, null, -9223372036854775808, 9223372036854775807]`},
		{"u8", arrow.PrimitiveTypes.Uint8, `[1, null, 0, 255]`},
		{"u16", arrow.PrimitiveTypes.Uint16, `[1, null, 0, 65535]`},
		{"u32", arrow.PrimitiveTypes.Uint32, `[1, null, 0, 4294967295]`},
		{"u64", arrow.PrimitiveTypes.Uint64, `[1, null, 0, 18446744073709551615]`},
		{"f16", arrow.FixedWidthTypes.Float16, ""}, // by its bits, below
		{"f32", arrow.PrimitiveTypes.Float32, `[1.5, null, -0.0, 3.4e38]`},
		{"f64", arrow.PrimitiveTypes.Float64, `[1.5, null, -0.0, 1e308]`},
		{"utf8", str, `["a", null, "", "héllo"]`},
		{"large_utf8", arrow.BinaryTypes.LargeString, `["a", null, "", "x"]`},
		{"string_view", arrow.BinaryTypes.StringView, `["a", null, "", "a string longer than twelve bytes"]`},
		{"binary", arrow.BinaryTypes.Binary, `["AQI=", null, "", "/w=="]`},
		{"binary_view", arrow.BinaryTypes.BinaryView, `["AQI=", null, "", "AAAAAAAAAAAAAAAAAAAAAAAA"]`},
		{"fixed_binary", &arrow.FixedSizeBinaryType{ByteWidth: 2}, `["AQI=", null, "AAA=", "//8="]`},
		{"date32", arrow.FixedWidthTypes.Date32, `[0, null, -1, 19000]`},
		{"date64", arrow.FixedWidthTypes.Date64, `[0, null, -86400000, 1641600000000]`},
		{"ts_utc", &arrow.TimestampType{Unit: arrow.Millisecond, TimeZone: "UTC"}, `[0, null, -1, 1700000000000]`},
		{"ts_naive", &arrow.TimestampType{Unit: arrow.Microsecond}, `[0, null, -1, 1700000000000000]`},
		{"time32", arrow.FixedWidthTypes.Time32s, `[0, null, 3600, 86399]`},
		{"time64", arrow.FixedWidthTypes.Time64ns, `[0, null, 1, 86399999999999]`},
		{"duration", arrow.FixedWidthTypes.Duration_ms, `[0, null, -5, 86400000]`},
		{"decimal", &arrow.Decimal128Type{Precision: 10, Scale: 2}, `["1.23", null, "-99999999.99", "0.00"]`},
		{"list", arrow.ListOf(i64), `[[1, 2], null, [], [3, null]]`},
		{"large_list", arrow.LargeListOf(str), `[["a"], null, [], ["b", null]]`},
		{"fixed_list", arrow.FixedSizeListOf(2, arrow.PrimitiveTypes.Int32), `[[1, 2], null, [3, null], [5, 6]]`},
		{"list_view", arrow.ListViewOf(i64), `[[1, 2], null, [], [3, null, 4]]`},
		{"map", arrow.MapOf(str, i64), `[[{"key": "a", "value": 1}], null, [], [{"key": "b", "value": null}]]`},
		{"struct", arrow.StructOf(arrow.Field{Name: "a", Type: i64, Nullable: true}, arrow.Field{Name: "b", Type: str, Nullable: true}),
			`[{"a": 1, "b": "x"}, null, {"a": null, "b": ""}, {"a": 4, "b": null}]`},
		{"dict", &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int16, ValueType: str}, `["b", null, "a", "b"]`},
	}
	fields := make([]arrow.Field, len(cols))
	arrs := make([]arrow.Array, len(cols))
	for k, c := range cols {
		var a arrow.Array
		var err error
		if c.js == "" {
			// Halves by their bits, subnormals among them, which arrow-go's
			// float16.New flushes to zero.
			b := array.NewFloat16Builder(mem)
			b.AppendValues([]float16.Num{float16.FromBits(0x0001), float16.FromBits(0), float16.FromBits(0x83ff), float16.FromBits(0x7bff)},
				[]bool{true, false, true, true})
			a = b.NewArray()
		} else if a, _, err = array.FromJSON(mem, c.t, strings.NewReader(c.js)); err != nil {
			f.Fatalf("building %s: %v", c.name, err)
		}
		fields[k] = arrow.Field{Name: c.name, Type: c.t, Nullable: true}
		arrs[k] = a
	}
	return array.NewRecordBatch(arrow.NewSchema(fields, nil), arrs, 4)
}
