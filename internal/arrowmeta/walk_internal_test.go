package arrowmeta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime/metrics"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/internal/uerr"
)

// codecs are the IPC writer's codecs, none among them.
var codecs = []struct {
	name string
	opts []ipc.Option
}{{"uncompressed", nil}, {"LZ4", []ipc.Option{ipc.WithLZ4()}}, {"Zstandard", []ipc.Option{ipc.WithZstd()}}}

// readArrow reads b as arrow-go does alone, every record of a file or a stream,
// retained; a panic in arrow-go is its error.
func readArrow(b []byte, file bool) ([]arrow.RecordBatch, error) {
	if file {
		return readFile(bytes.NewReader(b))
	}
	return readStream(ipc.NewMessageReader(bytes.NewReader(b)))
}

// readWalked reads b as ursus does, through File or NewMessageReader, and is how
// many block messages File walked.
func readWalked(b []byte, file bool) ([]arrow.RecordBatch, int64, error) {
	if !file {
		recs, err := readStream(NewMessageReader("test", "a stream", bytes.NewReader(b), memory.NewGoAllocator(), 0, 0))
		return recs, 0, err
	}
	f, err := OpenFile("test", "a file", bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, 0, err
	}
	recs, err := readFile(f)
	return recs, f.walked.Load(), err
}

func readFile(r ipc.ReadAtSeeker) (recs []arrow.RecordBatch, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("arrow-go panicked: %v", v)
		}
	}()
	fr, err := ipc.NewFileReader(r, ipc.WithAllocator(memory.NewGoAllocator()))
	if err != nil {
		return nil, err
	}
	defer fr.Close()
	for i := range fr.NumRecords() {
		rec, err := fr.RecordBatchAt(i)
		if err != nil {
			return recs, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func readStream(mr ipc.MessageReader) (recs []arrow.RecordBatch, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("arrow-go panicked: %v", v)
		}
	}()
	sr, err := ipc.NewReaderFromMessageReader(mr, ipc.WithAllocator(memory.NewGoAllocator()))
	if err != nil {
		return nil, err
	}
	defer sr.Release()
	for sr.Next() {
		rec := sr.RecordBatch()
		rec.Retain()
		recs = append(recs, rec)
	}
	return recs, sr.Err()
}

// footerBlocks is how many dictionary and record batch blocks the footer of b, an IPC
// file, lists, as arrow-go reads them.
func footerBlocks(t *testing.T, b []byte) int64 {
	fr, err := ipc.NewFileReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	return int64(fr.NumDictionaries() + fr.NumRecords())
}

// generated is each record gen makes from seeds 0 to n-1, little- and big-endian, as
// arrow-go writes it, a file and a stream in each codec: name, bytes, whether a file,
// and the record it holds, twice.
func generated(t *testing.T, n int, each func(name string, b []byte, file bool, rec arrow.RecordBatch)) {
	t.Helper()
	for seed := range n {
		for _, be := range []bool{false, true} {
			rec := newGen(uint64(seed), be).record()
			for _, c := range rec.Columns() {
				if err := array.ValidateFull(c); err != nil {
					t.Fatalf("seed %d made an invalid %s: %v", seed, c.DataType(), err)
				}
			}
			written := rec
			if be {
				written = bigEndian(rec)
			}
			for _, file := range []bool{true, false} {
				for _, c := range codecs {
					name := fmt.Sprintf("seed %d, big-endian %v, file %v, %s, %s", seed, be, file, c.name, rec.Schema())
					each(name, writeIPC(t, file, written, c.opts...), file, rec)
				}
			}
		}
	}
}

// TestTheWalkPassesWhatArrowGoWrites: no well-formed input is refused. Each record
// gen makes from 300 seeds, of every type arrow-go writes nested three deep, views of
// several buffers, dictionaries in lists and structs, unions, runs, list views and
// extensions among them, or of the fixed-width types, big-endian, written by arrow-go
// as a file and as a stream, uncompressed, LZ4 and Zstandard: read through File or
// NewMessageReader, every message walked, each reads as arrow-go reads it alone,
// record for record, where arrow-go reads it; and where arrow-go refuses it, so does
// ursus. Most read back as the record written, which says the input is well-formed;
// the rest are arrow-go's misreadings of its own writing, of nested types in a union
// or a list view, and its refusals, a big-endian Decimal or MonthDayNano interval of
// no rows, whose missing buffer its byte swap dereferences, counted apart.
func TestTheWalkPassesWhatArrowGoWrites(t *testing.T) {
	inputs, asWritten, unread, dicts, views, bigEndian := 0, 0, 0, 0, 0, 0
	generated(t, 300, func(name string, b []byte, file bool, rec arrow.RecordBatch) {
		inputs++
		want, err := readArrow(b, file)
		got, walked, werr := readWalked(b, file)
		if err != nil {
			if werr == nil {
				t.Fatalf("%s: read through the walk, and arrow-go alone refuses it: %v", name, err)
			}
			unread++
			return
		}
		if werr != nil {
			t.Fatalf("%s: refused: %v", name, werr)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d records read through the walk, %d by arrow-go alone", name, len(got), len(want))
		}
		for i := range got {
			if !array.RecordEqual(got[i], want[i]) {
				t.Fatalf("%s: record %d read through the walk otherwise than by arrow-go alone", name, i)
			}
		}
		if file && walked != footerBlocks(t, b) {
			t.Fatalf("%s: %d block messages walked of the %d arrow-go read", name, walked, footerBlocks(t, b))
		}
		if len(want) == 2 && fmt.Sprint(want[0].Columns()) == fmt.Sprint(rec.Columns()) &&
			fmt.Sprint(want[1].Columns()) == fmt.Sprint(rec.Columns()) {
			asWritten++
		}
		s := rec.Schema().String()
		if strings.Contains(s, "dictionary<") {
			dicts++
		}
		if strings.Contains(s, "_view") {
			views++
		}
		if strings.Contains(name, "big-endian true") {
			bigEndian++
		}
	})
	t.Logf("%d inputs: %d read back as written, %d refused by arrow-go alone; of those read, %d with a "+
		"dictionary, %d with a view, %d big-endian", inputs, asWritten, unread, dicts, views, bigEndian)
	if dicts == 0 || views == 0 || bigEndian == 0 || asWritten < inputs/2 {
		t.Fatalf("of %d inputs, %d read back as written, %d hold a dictionary, %d a view, %d are big-endian",
			inputs, asWritten, dicts, views, bigEndian)
	}
}

// TestEveryBlockIsWalked: arrow-go reads each block's metadata of an IPC file in one
// ReadAt of exactly the block's offset and metadata length, which is how File knows a
// read is one to walk. A version of arrow-go that read it otherwise would read
// metadata File never walked; here every dictionary block and record block arrow-go
// reads is walked once.
func TestEveryBlockIsWalked(t *testing.T) {
	files := 0
	generated(t, 40, func(name string, b []byte, file bool, _ arrow.RecordBatch) {
		if !file {
			return
		}
		_, walked, err := readWalked(b, true)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := footerBlocks(t, b); walked != want {
			t.Fatalf("%s: %d block messages walked, and arrow-go read %d", name, walked, want)
		}
		files++
	})
	if files == 0 {
		t.Fatal("no file read")
	}
}

// metadataOf is every flatbuffer of b arrow-go reads, a file's footer and each block's
// message, a stream's messages, each a slice of b, so that a change to one is a change
// to b.
func metadataOf(t *testing.T, b []byte, file bool) [][]byte {
	t.Helper()
	var out [][]byte
	if !file {
		for pos := 0; pos+8 <= len(b); {
			n := int(binary.LittleEndian.Uint32(b[pos+4:]))
			if n == 0 {
				break
			}
			meta := b[pos+8 : pos+8+n]
			out = append(out, meta)
			w, _ := newWalker(meta)
			_, _, body, err := w.message()
			if err != nil {
				t.Fatal(err)
			}
			pos += 8 + n + int(body)
		}
		return out
	}
	n := int(binary.LittleEndian.Uint32(b[len(b)-10:]))
	footer := b[len(b)-10-n : len(b)-10]
	out = append(out, footer)
	_, blocks, err := walkFooter(footer)
	if err != nil {
		t.Fatal(err)
	}
	for _, blk := range blocks {
		out = append(out, b[blk.off+8:blk.off+int64(blk.meta)])
	}
	return out
}

// TestTheWalkReadsOnlyItsBytes: the walk of every flatbuffer of 20 seeds' files and
// streams, cut at every length, and with every byte set to 0x00, 0x7f, 0x80 and 0xff,
// refuses it or passes it, and never reads past it or panics.
func TestTheWalkReadsOnlyItsBytes(t *testing.T) {
	walk := func(meta []byte, footer bool) {
		defer func() {
			if v := recover(); v != nil {
				t.Fatalf("the walk panicked on %x: %v", meta, v)
			}
		}()
		if footer {
			walkFooter(meta)
			return
		}
		r := &messageReader{op: "test", name: "a stream"}
		r.walk(meta)
		r.walk(meta)
	}
	walks := 0
	generated(t, 20, func(name string, b []byte, file bool, _ arrow.RecordBatch) {
		if strings.Contains(name, "LZ4") || strings.Contains(name, "Zstandard") {
			return // the metadata is the uncompressed input's
		}
		for i, meta := range metadataOf(t, bytes.Clone(b), file) {
			footer := file && i == 0
			for cut := range len(meta) {
				walk(meta[:cut], footer)
				walks++
			}
			for at := range meta {
				old := meta[at]
				for _, v := range []byte{0x00, 0x7f, 0x80, 0xff} {
					meta[at] = v
					walk(meta, footer)
					walks++
				}
				meta[at] = old
			}
		}
	})
	t.Logf("%d walks", walks)
}

// refused fails t unless err is the walk's refusal: KindIO, the input named corrupt.
func refused(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: read", what)
	}
	var e *uerr.Error
	if !errors.As(err, &e) || !errors.Is(err, uerr.ErrIO) || !strings.Contains(err.Error(), "is corrupt") {
		t.Fatalf("%s: %v, not the walk's refusal", what, err)
	}
	t.Logf("%s: %v", what, err)
}

// heapAllocs is the bytes allocated so far.
func heapAllocs() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// at is what a walk of a clean flatbuffer finds where: test code's use of the walk's
// own reads to forge an input, each of which the clean input passes.
type at struct {
	t *testing.T
	w *walker
}

func (a at) table(p uint32) table {
	tb, err := a.w.table(p, "a table")
	if err != nil {
		a.t.Fatal(err)
	}
	return tb
}

func (a at) ref(tb table, id int) uint32 {
	p, ok, err := a.w.ref(tb, id, "a field")
	if !ok || err != nil {
		a.t.Fatalf("field %d of the table at %d: %v, %v", id, tb.at, ok, err)
	}
	return p
}

// vector is where the vector at field id of tb keeps its count, and where its
// elements start.
func (a at) vector(tb table, id int) (count, start uint32) {
	v := a.ref(tb, id)
	return v, v + 4
}

// schema is the schema table of a stream's first message or a file's footer.
func (a at) schema(footer bool) table {
	root := a.table(a.w.b.u32(0))
	if footer {
		return a.table(a.ref(root, 1))
	}
	return a.table(a.ref(root, 2))
}

// field is the i-th field of the schema table s.
func (a at) field(s table, i uint32) table {
	_, start := a.vector(s, 1)
	p, err := a.w.elem(start, i, "a field")
	if err != nil {
		a.t.Fatal(err)
	}
	return a.table(p)
}

// batch is the record batch of a message, a dictionary batch's data for one.
func (a at) batch() table {
	root := a.table(a.w.b.u32(0))
	h := a.table(a.ref(root, 2))
	if a.kind(root) == messageDictionary {
		return a.table(a.ref(h, 1))
	}
	return h
}

func (a at) kind(root table) messageType {
	p, ok, _ := a.w.slot(root, 1, 1, "a header type")
	if !ok {
		return messageNone
	}
	return messageType(a.w.b.u8(p))
}

func put32(b []byte, at uint32, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
func put64(b []byte, at uint32, v uint64) { binary.LittleEndian.PutUint64(b[at:], v) }

// forgeRecord is the record forge's inputs are made from: a struct of a string view,
// a list of string views, a dictionary of text, and custom metadata on the schema and
// on a field.
func forgeRecord(t *testing.T) arrow.RecordBatch {
	t.Helper()
	mem := memory.NewGoAllocator()
	md := arrow.NewMetadata([]string{"origin"}, []string{"step 519"})
	st := arrow.StructOf(arrow.Field{Name: "v", Type: arrow.BinaryTypes.StringView, Nullable: true},
		arrow.Field{Name: "l", Type: arrow.ListOf(arrow.BinaryTypes.StringView), Nullable: true})
	dict := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int16, ValueType: arrow.BinaryTypes.StringView}
	s, _, err := array.FromJSON(mem, st, strings.NewReader(`[{"v": "a string past twelve bytes", "l": ["x", "another long string"]}, null]`))
	if err != nil {
		t.Fatal(err)
	}
	d := array.NewDictionaryArray(dict, mustJSON(t, arrow.PrimitiveTypes.Int16, `[1, 0]`),
		mustJSON(t, arrow.BinaryTypes.StringView, `["first, long enough", "second, long enough"]`))
	schema := arrow.NewSchema([]arrow.Field{{Name: "s", Type: st, Nullable: true, Metadata: md},
		{Name: "d", Type: dict, Nullable: true}}, &md)
	return array.NewRecordBatch(schema, []arrow.Array{s, d}, 2)
}

func mustJSON(t *testing.T, dt arrow.DataType, js string) arrow.Array {
	t.Helper()
	a, _, err := array.FromJSON(memory.NewGoAllocator(), dt, strings.NewReader(js))
	if err != nil {
		t.Fatal(err)
	}
	return a
}
