package arrowmeta

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
)

// forged is a clean IPC input being forged: its bytes and its flatbuffers, each a
// slice of them, in the order metadataOf gives: a stream's schema, dictionary batch
// and record batches; a file's footer, dictionary block and record blocks.
type forged struct {
	t     *testing.T
	b     []byte
	file  bool
	metas [][]byte
}

func newForged(t *testing.T, b []byte, file bool) *forged {
	b = bytes.Clone(b)
	return &forged{t: t, b: b, file: file, metas: metadataOf(t, b, file)}
}

func (f *forged) at(i int) at {
	w, err := newWalker(f.metas[i])
	if err != nil {
		f.t.Fatal(err)
	}
	return at{f.t, w}
}

// schema is the walk of the flatbuffer holding the schema, the schema, and its first
// field: forgeRecord's struct of a view and a list of views, with custom metadata.
func (f *forged) schema() (at, table, table) {
	a := f.at(0)
	s := a.schema(f.file)
	return a, s, a.field(s, 0)
}

// batch is the walk of the dictionary batch's flatbuffer (dict) or the first record
// batch's, and its RecordBatch table.
func (f *forged) batch(dict bool) (at, table) {
	i := 2
	if dict {
		i = 1
	}
	a := f.at(i)
	return a, a.batch()
}

// offsetIn is where sub, a slice of b, begins in b.
func offsetIn(b, sub []byte) int { return cap(b) - cap(sub) }

// TestForgedMetadataIsRefused: what arrow-go would trust, in a stream and a file of
// forgeRecord's, forged as step 511's findings forged it and more, is refused by the
// walk, KindIO, before arrow-go reads it: a count past what its bytes hold, of fields,
// children and key-values; a field its own child; a variadic count past the buffers,
// in a record batch and a dictionary batch, and fewer counts than view columns; a
// buffer outside the body; a codec and a type Arrow does not have; a stream's
// messages out of order; and a file's dictionary block holding a record batch.
func TestForgedMetadataIsRefused(t *testing.T) {
	const huge = 0x7fffffff
	cases := []struct {
		name         string
		file, stream bool
		patch        func(f *forged)
	}{
		{"a schema's fields", true, true, func(f *forged) {
			a, s, _ := f.schema()
			count, _ := a.vector(s, 1)
			put32(a.w.b, count, huge)
		}},
		{"a field's children", true, true, func(f *forged) {
			a, _, fld := f.schema()
			count, _ := a.vector(fld, 5)
			put32(a.w.b, count, huge)
		}},
		{"a schema's custom metadata", true, true, func(f *forged) {
			a, s, _ := f.schema()
			count, _ := a.vector(s, 2)
			put32(a.w.b, count, huge)
		}},
		{"a field's custom metadata", true, true, func(f *forged) {
			a, _, fld := f.schema()
			count, _ := a.vector(fld, 6)
			put32(a.w.b, count, huge)
		}},
		{"a field its own child", true, true, func(f *forged) {
			a, _, fld := f.schema()
			_, start := a.vector(fld, 5)
			put32(a.w.b, start, fld.at-start) // wraps, past 2^31, back to the field
		}},
		{"a field of type 99", true, true, func(f *forged) {
			a, _, fld := f.schema()
			p, _, _ := a.w.slot(fld, 2, 1, "a type tag")
			a.w.b[p] = 99
		}},
		{"a record batch's variadic count", true, true, func(f *forged) {
			a, rb := f.batch(false)
			_, start := a.vector(rb, 4)
			put64(a.w.b, start, 1<<40)
		}},
		{"a dictionary batch's variadic count", true, true, func(f *forged) {
			a, rb := f.batch(true)
			_, start := a.vector(rb, 4)
			put64(a.w.b, start, 1<<40)
		}},
		{"fewer variadic counts than view columns", true, true, func(f *forged) {
			a, rb := f.batch(false)
			count, _ := a.vector(rb, 4)
			put32(a.w.b, count, 1)
		}},
		{"a buffer outside the body", true, true, func(f *forged) {
			a, rb := f.batch(false)
			_, start := a.vector(rb, 2)
			put64(a.w.b, start+16, 1<<40)
		}},
		{"a record batch before its dictionary", false, true, func(f *forged) {
			from := offsetIn(f.b, f.metas[1]) - 8
			to := offsetIn(f.b, f.metas[2]) - 8
			f.b = append(f.b[:from:from], f.b[to:]...)
		}},
		{"a stream not beginning with its schema", false, true, func(f *forged) {
			f.b = f.b[offsetIn(f.b, f.metas[1])-8:]
		}},
		{"a dictionary block holding a record batch", true, false, func(f *forged) {
			a := f.at(0)
			root := a.table(a.w.b.u32(0))
			_, dicts := a.vector(root, 2)
			_, recs := a.vector(root, 3)
			copy(a.w.b[dicts:dicts+24], a.w.b[recs:recs+24])
		}},
	}
	rec := forgeRecord(t)
	for _, file := range []bool{true, false} {
		clean := writeIPC(t, file, rec)
		if _, _, err := readWalked(clean, file); err != nil {
			t.Fatalf("the clean input, file %v: %v", file, err)
		}
		for _, c := range cases {
			if file && !c.file || !file && !c.stream {
				continue
			}
			name := fmt.Sprintf("%s, file %v", c.name, file)
			t.Run(name, func(t *testing.T) {
				f := newForged(t, clean, file)
				c.patch(f)
				_, _, err := readWalked(f.b, file)
				refused(t, name, err)
			})
		}
		// A codec Arrow does not have, in a compressed input: Zstandard's, 1, which
		// a writer writes, where LZ4's, 0, is the default and left out.
		name := fmt.Sprintf("a codec of 7, file %v", file)
		t.Run(name, func(t *testing.T) {
			f := newForged(t, writeIPC(t, file, rec, ipc.WithZstd()), file)
			a, rb := f.batch(false)
			ct := a.table(a.ref(rb, 3))
			p, ok, err := a.w.slot(ct, 0, 1, "a codec")
			if !ok || err != nil || a.w.b[p] != 1 {
				t.Fatalf("no Zstandard codec in the compressed batch: %v, %v", ok, err)
			}
			a.w.b[p] = 7
			_, _, err = readWalked(f.b, file)
			refused(t, name, err)
		})
	}
}

// schemaOnly is an IPC file or stream of s and no record.
func schemaOnly(t *testing.T, file bool, s *arrow.Schema) []byte {
	t.Helper()
	var buf bytes.Buffer
	if file {
		w, err := ipc.NewFileWriter(&buf, ipc.WithSchema(s))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	if err := ipc.NewWriter(&buf, ipc.WithSchema(s)).Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestFieldsNestNoDeeperThanMaxDepth: a List nested so that its fields are MaxDepth
// deep is read, as arrow-go reads it; one a level deeper is refused, before arrow-go's
// fieldFromFB recurses on it.
func TestFieldsNestNoDeeperThanMaxDepth(t *testing.T) {
	for _, depth := range []int{MaxDepth, MaxDepth + 1} {
		typ := arrow.DataType(arrow.PrimitiveTypes.Int8)
		for range depth - 1 {
			typ = arrow.ListOf(typ)
		}
		s := arrow.NewSchema([]arrow.Field{{Name: "deep", Type: typ, Nullable: true}}, nil)
		for _, file := range []bool{true, false} {
			b := schemaOnly(t, file, s)
			if _, err := readArrow(b, file); err != nil {
				t.Fatalf("arrow-go reads none of its own schema %d deep: %v", depth, err)
			}
			_, _, err := readWalked(b, file)
			if depth <= MaxDepth && err != nil {
				t.Errorf("fields %d deep, file %v: %v", depth, file, err)
			}
			if depth > MaxDepth {
				refused(t, fmt.Sprintf("fields %d deep, file %v", depth, file), err)
			}
		}
	}
}

// TestASharedFieldCostsEachTimeItIsReached: a schema of structs 40 deep, each holding
// a struct and an integer, forged so that each struct's second child is its first: a
// field reached 2^40 times from a few kilobytes, which arrow-go's fieldFromFB would
// build each time, the process ending before it finished. The walk counts a field
// each time it reaches it, and refuses the schema at its budget, having allocated
// about nothing.
func TestASharedFieldCostsEachTimeItIsReached(t *testing.T) {
	typ := arrow.DataType(arrow.PrimitiveTypes.Int8)
	for range 40 {
		typ = arrow.StructOf(arrow.Field{Name: "a", Type: typ, Nullable: true},
			arrow.Field{Name: "b", Type: arrow.PrimitiveTypes.Int8, Nullable: true})
	}
	s := arrow.NewSchema([]arrow.Field{{Name: "s", Type: typ, Nullable: true}}, nil)
	for _, file := range []bool{true, false} {
		f := newForged(t, schemaOnly(t, file, s), file)
		a, _, fld := f.schema()
		shared := 0
		for {
			count, start := a.vector(fld, 5)
			if a.w.b.u32(count) != 2 {
				break
			}
			first := start + a.w.b.u32(start)
			put32(a.w.b, start+4, first-(start+4))
			shared++
			p, err := a.w.elem(start, 0, "a child")
			if err != nil {
				t.Fatal(err)
			}
			fld = a.table(p)
		}
		if shared != 40 {
			t.Fatalf("file %v: %d structs' children shared, of 40", file, shared)
		}
		before := heapAllocs()
		_, _, err := readWalked(f.b, file)
		if grew := heapAllocs() - before; grew > 1<<20 {
			t.Errorf("file %v: walking the shared fields allocated %d bytes", file, grew)
		}
		refused(t, fmt.Sprintf("a field reached 2^40 times, file %v", file), err)
	}
}
