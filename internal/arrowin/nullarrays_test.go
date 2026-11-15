package arrowin_test

// A Null array of no buffers is valid (step 522). The Arrow IPC format writes a Null
// column as a field node and no buffer, and arrow-go's IPC reader builds it as a Null
// array of no buffers (loadNull), where its builders and its array.Validate give a Null
// array the one buffer its layout declares, always nil. Validate refused the reader's,
// "expected at least 1 buffers for null, got 0", and with it every IPC input holding a
// Null column at any depth. The two shapes hold the same data: a Null array has no
// values to keep.

import (
	"bytes"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/extensions"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/uerr"
)

// nullBearing is a record of four rows, a column of each Arrow type ursus reads that
// holds a Null array: a Null column, a List's, a LargeList's, a ListView's and a
// FixedSizeList's elements, a Struct's field, a Map's values, a Struct's field in a
// List, a Dictionary's values and an extension's storage. arrow-go built each, every
// Null array holding the nil buffer.
func nullBearing(t *testing.T, mem memory.Allocator) arrow.RecordBatch {
	t.Helper()
	i32 := arrow.PrimitiveTypes.Int32
	z := arrow.Field{Name: "z", Type: arrow.Null, Nullable: true}
	cols := []struct {
		name string
		arr  arrow.Array
	}{
		{"null", fromJSON(t, mem, arrow.Null, `[null, null, null, null]`)},
		{"list", fromJSON(t, mem, arrow.ListOf(arrow.Null), `[[null], [], null, [null, null]]`)},
		{"large_list", fromJSON(t, mem, arrow.LargeListOf(arrow.Null), `[[null, null], null, [], [null]]`)},
		{"list_view", fromJSON(t, mem, arrow.ListViewOf(arrow.Null), `[[], [null], [null, null, null], null]`)},
		{"fixed_list", fromJSON(t, mem, arrow.FixedSizeListOf(2, arrow.Null), `[[null, null], null, [null, null], [null, null]]`)},
		{"struct", fromJSON(t, mem, arrow.StructOf(z, arrow.Field{Name: "y", Type: i64, Nullable: true}),
			`[{"z": null, "y": 1}, null, {"z": null, "y": null}, {"z": null, "y": 4}]`)},
		{"map", fromJSON(t, mem, arrow.MapOf(str, arrow.Null),
			`[[{"key": "a", "value": null}], null, [], [{"key": "b", "value": null}, {"key": "c", "value": null}]]`)},
		{"list_struct", fromJSON(t, mem, arrow.ListOf(arrow.StructOf(z)), `[[{"z": null}, null], null, [], [{"z": null}]]`)},
		{"dictionary", keep(t, array.NewDictionaryArray(&arrow.DictionaryType{IndexType: i32, ValueType: arrow.Null},
			fromJSON(t, mem, i32, `[0, null, 1, 0]`), keep(t, array.NewNull(2))))},
		{"extension", keep(t, array.NewExtensionArrayWithStorage(extensions.NewOpaqueType(arrow.Null, "nothing", "ursus"),
			keep(t, array.NewNull(4))))},
	}
	fields := make([]arrow.Field, len(cols))
	arrs := make([]arrow.Array, len(cols))
	for k, c := range cols {
		fields[k] = arrow.Field{Name: c.name, Type: c.arr.DataType(), Nullable: true}
		arrs[k] = c.arr
	}
	rec := array.NewRecordBatch(arrow.NewSchema(fields, nil), arrs, 4)
	t.Cleanup(rec.Release)
	return rec
}

// emptyNulls counts the Null arrays of no buffer in d, d itself among them, its
// children and its dictionary.
func emptyNulls(d arrow.ArrayData) int {
	n := 0
	dt := d.DataType()
	if ext, ok := dt.(arrow.ExtensionType); ok {
		dt = ext.StorageType()
	}
	if dt.ID() == arrow.NULL && len(d.Buffers()) == 0 {
		n++
	}
	for _, c := range d.Children() {
		n += emptyNulls(c)
	}
	if dict, ok := d.Dictionary().(*array.Data); ok && dict != nil {
		n += emptyNulls(dict)
	}
	return n
}

// readAll checks rec against its schema and reads it whole, exported again through
// arrowout.
func readAll(t *testing.T, rec arrow.RecordBatch) (arrow.RecordBatch, error) {
	t.Helper()
	schema, err := arrowin.Schema(rec.Schema())
	if err != nil {
		t.Fatal(err)
	}
	if err := arrowin.NewChecker(schema).Check(rec); err != nil {
		return nil, err
	}
	cols := make([]int, schema.Len())
	for i := range cols {
		cols[i] = i
	}
	b, err := arrowin.Batch(rec, schema, cols, 0, int(rec.NumRows()))
	if err != nil {
		return nil, err
	}
	out, err := arrowout.Record(b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(out.Release)
	return out, nil
}

// TestANullArrayOfNoBuffersReads: nullBearing's record, written by arrow-go's IPC
// writer as a file and a stream, uncompressed, LZ4 and Zstandard, and read by its
// reader, holds a Null array of no buffer in every column; each passes the Checker,
// and is read as the record written is read, whose Null arrays hold the nil buffer.
func TestANullArrayOfNoBuffersReads(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	t.Cleanup(func() { mem.AssertSize(t, 0) }) // after every release the test's cleanups make
	rec := nullBearing(t, mem)
	want, err := readAll(t, rec)
	if err != nil {
		t.Fatalf("the record written: %v", err)
	}
	for _, file := range []bool{true, false} {
		for _, codec := range []struct {
			name string
			opts []ipc.Option
		}{{"uncompressed", nil}, {"LZ4", []ipc.Option{ipc.WithLZ4()}}, {"Zstandard", []ipc.Option{ipc.WithZstd()}}} {
			var buf bytes.Buffer
			opts := append(codec.opts, ipc.WithSchema(rec.Schema()), ipc.WithAllocator(mem))
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
			if err := w.Write(rec); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			// The reader allocates apart from mem, which counts the record and the
			// writer alone.
			got := readBack(t, buf.Bytes(), file, memory.NewGoAllocator())
			for i := range int(got.NumCols()) {
				if emptyNulls(got.Column(i).Data()) == 0 {
					t.Fatalf("file %v, %s: arrow-go read column %q with no Null array of no buffer",
						file, codec.name, got.Schema().Field(i).Name)
				}
			}
			read, err := readAll(t, got)
			if err != nil {
				t.Fatalf("file %v, %s: %v", file, codec.name, err)
			}
			for i := range int(want.NumCols()) {
				if !array.Equal(read.Column(i), want.Column(i)) {
					t.Errorf("file %v, %s: column %q read as %v, and as written %v",
						file, codec.name, want.Schema().Field(i).Name, read.Column(i), want.Column(i))
				}
			}
		}
	}
}

// readBack is the one record of b, as arrow-go's IPC reader reads it, released with
// the test.
func readBack(t *testing.T, b []byte, file bool, mem memory.Allocator) arrow.RecordBatch {
	t.Helper()
	var rec arrow.RecordBatch
	if file {
		fr, err := ipc.NewFileReader(bytes.NewReader(b), ipc.WithAllocator(mem))
		if err != nil {
			t.Fatal(err)
		}
		defer fr.Close()
		if rec, err = fr.RecordBatchAt(0); err != nil {
			t.Fatal(err)
		}
	} else {
		r, err := ipc.NewReader(bytes.NewReader(b), ipc.WithAllocator(mem))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Release()
		if !r.Next() {
			t.Fatalf("no record: %v", r.Err())
		}
		rec = r.RecordBatch()
		rec.Retain()
	}
	t.Cleanup(rec.Release)
	return rec
}

// TestOtherwiseMalformedNullsAreRefused: a Null array of no buffer is given the nil
// buffer before Validate and nothing else is: a Null array with a buffer that is not
// nil, with two, or with a child, is refused as before, and so is an array whose own
// shape is wrong over a Null child of no buffer.
func TestOtherwiseMalformedNullsAreRefused(t *testing.T) {
	empty := array.NewData(arrow.Null, 4, nil, nil, 4, 0)
	defer empty.Release()
	short := array.NewData(arrow.Null, 2, nil, nil, 2, 0)
	defer short.Release()
	refused(t, "a Null array with a buffer", make1(t, array.NewData(arrow.Null, 4,
		[]*memory.Buffer{bitsOf(true, true, true, true)}, nil, 4, 0)),
		dtype.Null, uerr.ErrValue, "must be nil")
	refused(t, "a Null array with two buffers", make1(t, array.NewData(arrow.Null, 4,
		[]*memory.Buffer{nil, nil}, nil, 4, 0)),
		dtype.Null, uerr.ErrValue, "at most 1 buffers")
	refused(t, "a Null array with a child", make1(t, array.NewData(arrow.Null, 4,
		nil, []arrow.ArrayData{empty}, 4, 0)),
		dtype.Null, uerr.ErrValue, "must not have child arrays")
	refused(t, "a list's offsets too few for its rows, over a Null child of no buffer",
		make1(t, array.NewData(arrow.ListOf(arrow.Null), 3,
			[]*memory.Buffer{nil, bytesOf([]int32{0, 1})}, []arrow.ArrayData{empty}, 0, 0)),
		dtype.List(dtype.Null), uerr.ErrValue, "is too small")
	refused(t, "a list's last offset past its Null child of no buffer",
		make1(t, array.NewData(arrow.ListOf(arrow.Null), 2,
			[]*memory.Buffer{nil, bytesOf([]int32{0, 1, 5})}, []arrow.ArrayData{empty}, 0, 0)),
		dtype.List(dtype.Null), uerr.ErrValue, "out of bounds")
	refused(t, "a fixed-size list longer than its Null child of no buffer",
		make1(t, array.NewData(arrow.FixedSizeListOf(2, arrow.Null), 2,
			[]*memory.Buffer{nil}, []arrow.ArrayData{short}, 0, 0)),
		dtype.Array(dtype.Null, 2), uerr.ErrValue, "too small")
	refused(t, "a struct of more nulls than rows, over a Null field of no buffer",
		make1(t, array.NewData(arrow.StructOf(arrow.Field{Name: "z", Type: arrow.Null, Nullable: true}), 4,
			[]*memory.Buffer{nil}, []arrow.ArrayData{empty}, 5, 0)),
		dtype.Struct(dtype.Field{Name: "z", Type: dtype.Null, Nullable: true}), uerr.ErrValue, "invalid null count")
}
