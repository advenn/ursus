package readfuzz_test

import (
	"encoding/binary"
	"fmt"
)

// A walk of an Arrow IPC input's flatbuffers metadata, for the finding of step 511
// that the IPC targets skip: arrow-go sizes slices by the lengths its flatbuffers
// declare, before reading what they hold, and a length past the buffer ends the
// process with "fatal error: runtime: out of memory", which no recover catches:
//
//   - schemaFromFB, make([]arrow.Field, schema.FieldsLength()) (arrow/ipc/metadata.go);
//   - fieldFromFB, make([]arrow.Field, field.ChildrenLength());
//   - metadataFromFB, two make([]string, CustomMetadataLength()), a schema's, a
//     field's and a record batch message's;
//   - loadCommon, make([]*memory.Buffer, 0, 2+variadicCount) of a view column
//     (arrow/ipc/file_reader.go), the count a record batch's int64.
//
// Go's flatbuffers has no verifier, so ursus would need a walk of its own, of the
// Message, Footer, Schema, Field, KeyValue, RecordBatch and DictionaryBatch tables,
// before arrow-go reads a footer or a message: Parquet's footer is measured so
// (step 423). That is left to the maintainer; oversized says which inputs meet it.

// fbuf is a flatbuffer: its bytes, every read of them checked. Positions add as
// flatbuffers' Go accessors add them, in uint32, so an offset past 2^31 points back
// before itself, as it does for arrow-go: a schema's field can be its own child.
type fbuf []byte

func (b fbuf) u32(at uint32) (uint32, bool) {
	if uint64(at)+4 > uint64(len(b)) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b[at:]), true
}

func (b fbuf) u8(at uint32) (byte, bool) {
	if uint64(at) >= uint64(len(b)) {
		return 0, false
	}
	return b[at], true
}

func (b fbuf) u16(at uint32) (uint32, bool) {
	if uint64(at)+2 > uint64(len(b)) {
		return 0, false
	}
	return uint32(binary.LittleEndian.Uint16(b[at:])), true
}

func (b fbuf) i64(at uint32) (int64, bool) {
	if uint64(at)+8 > uint64(len(b)) {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(b[at:])), true
}

// field is where field id of the table at t is, false where it is absent.
func (b fbuf) field(t uint32, id int) (uint32, bool) {
	so, ok := b.u32(t)
	if !ok {
		return 0, false
	}
	vt := uint32(int32(t) - int32(so))
	size, ok := b.u16(vt)
	if !ok || uint32(4+2*id) >= size {
		return 0, false
	}
	off, ok := b.u16(vt + uint32(4+2*id))
	if !ok || off == 0 {
		return 0, false
	}
	return t + off, true
}

// ref is where the offset at field id of the table at t points: a table, a vector
// or a string.
func (b fbuf) ref(t uint32, id int) (uint32, bool) {
	at, ok := b.field(t, id)
	if !ok {
		return 0, false
	}
	off, ok := b.u32(at)
	return at + off, ok
}

// vector is the vector at field id of the table at t: where its elements start and
// how many it declares.
func (b fbuf) vector(t uint32, id int) (start uint32, n int, ok bool) {
	v, ok := b.ref(t, id)
	if !ok {
		return 0, 0, false
	}
	m, ok := b.u32(v)
	return v + 4, int(m), ok
}

// elem is where the table the i-th offset of a vector from start points to.
func (b fbuf) elem(start uint32, i int) (uint32, bool) {
	at := start + uint32(i)*4
	off, ok := b.u32(at)
	return at + off, ok
}

// past says whether a vector of n elements of size bytes from start runs past b.
func (b fbuf) past(start uint32, n, size int) bool {
	return uint64(start)+uint64(n)*uint64(size) > uint64(len(b))
}

// The field ids of the tables walked (Schema.fbs, Message.fbs, File.fbs).
const (
	msgHeaderType, msgHeader, msgBodyLength, msgCustomMetadata = 1, 2, 3, 4
	schemaFields, schemaCustomMetadata                         = 1, 2
	fieldTypeType, fieldDictionary                             = 2, 4
	fieldChildren, fieldCustomMetadata                         = 5, 6
	batchLength, batchBuffers, batchVariadic                   = 0, 2, 4
	dictData                                                   = 1
	footerSchema, footerDictionaries, footerRecordBatches      = 1, 2, 3
)

// The view types' tags in a Field's type union (Schema.fbs).
const (
	typeBinaryView = 23
	typeUtf8View   = 24
)

// The message headers walked.
const (
	headerSchema     = 1
	headerDictionary = 2
	headerRecord     = 3
)

// oversized names what in b, an IPC file or stream, arrow-go would size a slice by
// past what its buffer holds, "" for nothing: a walk of its metadata, which stops
// quietly at anything it cannot read, as arrow-go then refuses or panics on it.
func oversized(b []byte, file bool) string {
	if file {
		return fileOversized(b)
	}
	w := walk{input: len(b)}
	for pos := 0; pos < len(b); {
		meta, next, ok := message(b, pos)
		if !ok {
			return ""
		}
		if why := w.message(meta); why != "" {
			return why
		}
		pos = next
	}
	return ""
}

// walk is what a walk of one input knows: its size, and how many view columns its
// schema has, for which arrow-go reads a record batch's variadic counts.
type walk struct {
	input int
	views int
}

// message is the metadata of the stream message at pos and where the next one starts,
// false at the end or where the framing cannot be read.
func message(b fbuf, pos int) (meta fbuf, next int, ok bool) {
	if pos < 0 || pos > len(b) {
		return nil, 0, false
	}
	m, ok := b.u32(uint32(pos))
	if !ok {
		return nil, 0, false
	}
	start := pos + 4
	if m == 0xffffffff {
		if m, ok = b.u32(uint32(pos + 4)); !ok {
			return nil, 0, false
		}
		start = pos + 8
	}
	n := int(m)
	if n == 0 || int32(m) < 0 || start+n > len(b) {
		return nil, 0, false
	}
	meta = b[start : start+n]
	root, ok := meta.u32(0)
	if !ok {
		return nil, 0, false
	}
	body := int64(0)
	if at, ok := meta.field(root, msgBodyLength); ok {
		body, _ = meta.i64(at)
	}
	if body < 0 || body > int64(len(b)) {
		return meta, len(b), true
	}
	return meta, start + n + int(body), true
}

// message is oversized of one message's metadata.
func (w *walk) message(meta fbuf) string {
	root, ok := meta.u32(0)
	if !ok {
		return ""
	}
	if why := metadataOversized(meta, root, msgCustomMetadata, "a message"); why != "" {
		return why
	}
	at, _ := meta.field(root, msgHeaderType)
	kind, ok := meta.u8(at)
	if !ok {
		return ""
	}
	header, ok := meta.ref(root, msgHeader)
	if !ok {
		return ""
	}
	switch kind {
	case headerSchema:
		w.views = viewColumns(meta, header)
		return schemaOversized(meta, header)
	case headerRecord:
		return w.batch(meta, header, w.views)
	case headerDictionary:
		if data, ok := meta.ref(header, dictData); ok {
			return w.batch(meta, data, 0)
		}
	}
	return ""
}

// viewColumns is how many columns of a view type the schema at schema has, nested
// ones among them, but none dictionary-encoded, whose views are a dictionary batch's:
// a record batch has a variadic count for each.
func viewColumns(b fbuf, schema uint32) int {
	start, n, ok := b.vector(schema, schemaFields)
	if !ok || b.past(start, n, 4) {
		return 0
	}
	return viewsIn(b, start, n, 0)
}

func viewsIn(b fbuf, start uint32, n, depth int) int {
	views := 0
	for i := 0; i < n && depth <= maxSchemaDepth; i++ {
		f, ok := b.elem(start, i)
		if !ok {
			return views
		}
		at, _ := b.field(f, fieldTypeType)
		_, dict := b.field(f, fieldDictionary)
		if t, ok := b.u8(at); ok && !dict && (t == typeBinaryView || t == typeUtf8View) {
			views++
		}
		if cs, cn, ok := b.vector(f, fieldChildren); ok && !b.past(cs, cn, 4) {
			views += viewsIn(b, cs, cn, depth+1)
		}
	}
	return views
}

// metadataOversized says whether the custom metadata at field id of the table at t,
// of what, declares more key-values than its buffer holds offsets.
func metadataOversized(b fbuf, t uint32, id int, what string) string {
	if start, n, ok := b.vector(t, id); ok && b.past(start, n, 4) {
		return fmt.Sprintf("%s's custom metadata declares %d key-values in %d bytes (metadataFromFB)", what, n, len(b))
	}
	return ""
}

func schemaOversized(b fbuf, schema uint32) string {
	if why := metadataOversized(b, schema, schemaCustomMetadata, "a schema"); why != "" {
		return why
	}
	start, n, ok := b.vector(schema, schemaFields)
	if !ok {
		return ""
	}
	if b.past(start, n, 4) {
		return fmt.Sprintf("a schema declares %d fields in %d bytes (schemaFromFB)", n, len(b))
	}
	return fieldsOversized(b, start, n, 0)
}

// fieldsOversized walks n fields whose offsets start at start, depth levels down.
func fieldsOversized(b fbuf, start uint32, n, depth int) string {
	if depth > maxSchemaDepth {
		return fmt.Sprintf("a schema nests past %d levels, or a field is its own descendant, "+
			"which fieldFromFB recurses on until the stack overflows", maxSchemaDepth)
	}
	for i := range n {
		f, ok := b.elem(start, i)
		if !ok {
			return ""
		}
		if why := metadataOversized(b, f, fieldCustomMetadata, "a field"); why != "" {
			return why
		}
		cs, cn, ok := b.vector(f, fieldChildren)
		if !ok {
			continue
		}
		if b.past(cs, cn, 4) {
			return fmt.Sprintf("a field declares %d children in %d bytes (fieldFromFB)", cn, len(b))
		}
		if why := fieldsOversized(b, cs, cn, depth+1); why != "" {
			return why
		}
	}
	return ""
}

// maxSchemaDepth is the deepest schema oversized walks: arrow-go's fieldFromFB
// recurses once a level, with no limit, so a field among its own descendants, which
// an offset past 2^31 makes, overflows any stack, and a schema 16,000 deep the 8 MiB
// the targets run on. TestScanIPCDeepSchema reads one 1,000 deep.
const maxSchemaDepth = 2_000

// batch says whether a record batch declares a view column more variadic buffers
// than it has buffers, or more rows than the input holds of any type but Null: a row
// of every other type costs a bit at least, and a Null column none, nor a record of
// no columns, so a 600-byte stream declaring 2^40 rows had ursus copy batches of
// them, and allocate, until the harness ended the process at four times its bound.
// arrow-go reads a variadic count for each of views view columns, in the vector or
// past its declared length.
func (w *walk) batch(b fbuf, batch uint32, views int) string {
	if at, ok := b.field(batch, batchLength); ok {
		if rows, ok := b.i64(at); ok && rows > 8*int64(w.input)+4096 {
			return fmt.Sprintf("a record batch declares %d rows in an input of %d bytes, which only "+
				"columns of no data, Null or none, can hold, and ursus copies", rows, w.input)
		}
	}
	_, buffers, _ := b.vector(batch, batchBuffers)
	start, n, ok := b.vector(batch, batchVariadic)
	if !ok {
		return ""
	}
	for i := range max(n, views) {
		v, ok := b.i64(start + uint32(8*i))
		if !ok {
			break
		}
		if v < 0 || v > int64(buffers) {
			return fmt.Sprintf("a record batch declares %d variadic buffers and has %d (loadCommon)", v, buffers)
		}
	}
	return ""
}

// fileOversized is oversized of an IPC file: its footer's schema, and the message of
// each block its footer lists.
func fileOversized(b fbuf) string {
	if len(b) < 10 || string(b[len(b)-6:]) != "ARROW1" {
		return ""
	}
	m, _ := b.u32(uint32(len(b) - 10))
	size := int(m)
	if int32(m) <= 0 || size > len(b)-10 {
		return ""
	}
	footer := b[len(b)-10-size : len(b)-10]
	root, ok := footer.u32(0)
	if !ok {
		return ""
	}
	w := walk{input: len(b)}
	if schema, ok := footer.ref(root, footerSchema); ok {
		if why := schemaOversized(footer, schema); why != "" {
			return why
		}
		w.views = viewColumns(footer, schema)
	}
	for _, id := range []int{footerDictionaries, footerRecordBatches} {
		start, n, ok := footer.vector(root, id)
		if !ok {
			continue
		}
		// arrow-go reads each block it is asked for, however many the vector
		// declares; a dictionary block's message as a dictionary batch, whatever
		// its header's type says (readDictionary).
		for i := 0; i < n && !footer.past(start, i+1, 24); i++ {
			meta, ok := blockMessage(b, footer, start+uint32(24*i))
			if !ok {
				continue
			}
			if why := w.message(meta); why != "" {
				return why
			}
			if id != footerDictionaries {
				continue
			}
			root, _ := meta.u32(0)
			if h, ok := meta.ref(root, msgHeader); ok {
				if data, ok := meta.ref(h, dictData); ok {
					if why := w.batch(meta, data, 0); why != "" {
						return why
					}
				}
			}
		}
	}
	return ""
}

// blockMessage is the metadata of the message the footer's Block at at points to,
// as arrow-go's fileBlock.NewMessage reads it: the block's metaDataLength bytes at
// its offset, less a prefix of eight bytes after a continuation marker, of none
// after a zero, and of four otherwise.
func blockMessage(b, footer fbuf, at uint32) (fbuf, bool) {
	off, ok := footer.i64(at)
	m, ok2 := footer.u32(at + 8)
	size := int64(int32(m))
	if !ok || !ok2 || off < 0 || size < 4 || off+size > int64(len(b)) {
		return nil, false
	}
	meta := b[off : off+size]
	prefix := 4
	switch binary.LittleEndian.Uint32(meta) {
	case 0:
		prefix = 0
	case 0xffffffff:
		prefix = 8
	}
	if len(meta) < prefix+4 {
		return nil, false
	}
	return meta[prefix:], true
}

// patchMeta is b, an IPC file or stream, with one length its metadata declares made
// v, for the seeds of oversized's finding: what is "fields", a schema's fields;
// "children", the first field with children's; "metadata", the schema's custom
// metadata; or "variadic", the first record batch's first variadic count; or, v
// unused, "cycle", the first field with children made its own child. It is nil where
// b has no such length.
func patchMeta(b []byte, file bool, what string, v uint64) []byte {
	b = append([]byte(nil), b...)
	var schemas []schemaAt
	var batches []batchAt
	if file {
		fb := fbuf(b)
		size, _ := fb.u32(uint32(len(b) - 10))
		footer := fb[len(b)-10-int(size) : len(b)-10]
		root, _ := footer.u32(0)
		if s, ok := footer.ref(root, footerSchema); ok {
			schemas = append(schemas, schemaAt{footer, s})
		}
		start, n, _ := footer.vector(root, footerRecordBatches)
		for i := range n {
			if meta, ok := blockMessage(fb, footer, start+uint32(24*i)); ok {
				batches = append(batches, batchesOf(meta)...)
			}
		}
	} else {
		for pos := 0; pos < len(b); {
			meta, next, ok := message(b, pos)
			if !ok {
				break
			}
			root, _ := meta.u32(0)
			at, _ := meta.field(root, msgHeaderType)
			if kind, ok := meta.u8(at); ok && kind == headerSchema {
				if h, ok := meta.ref(root, msgHeader); ok {
					schemas = append(schemas, schemaAt{meta, h})
				}
			}
			batches = append(batches, batchesOf(meta)...)
			pos = next
		}
	}
	put := func(buf fbuf, at uint32, size int) []byte {
		if size == 4 {
			binary.LittleEndian.PutUint32(buf[at:], uint32(v))
		} else {
			binary.LittleEndian.PutUint64(buf[at:], v)
		}
		return b
	}
	switch what {
	case "fields", "metadata":
		id := schemaFields
		if what == "metadata" {
			id = schemaCustomMetadata
		}
		for _, s := range schemas {
			if start, _, ok := s.b.vector(s.t, id); ok {
				return put(s.b, start-4, 4)
			}
		}
	case "children":
		for _, s := range schemas {
			start, n, _ := s.b.vector(s.t, schemaFields)
			for i := range n {
				f, _ := s.b.elem(start, i)
				if cs, cn, ok := s.b.vector(f, fieldChildren); ok && cn > 0 {
					return put(s.b, cs-4, 4)
				}
			}
		}
	case "cycle":
		// The first field with children made its own first child: its offset in
		// the children vector pointing back, past 2^31, at the field.
		for _, s := range schemas {
			start, n, _ := s.b.vector(s.t, schemaFields)
			for i := range n {
				f, _ := s.b.elem(start, i)
				if cs, cn, ok := s.b.vector(f, fieldChildren); ok && cn > 0 {
					binary.LittleEndian.PutUint32(s.b[cs:], f-cs)
					return b
				}
			}
		}
	case "variadic":
		for _, r := range batches {
			if start, n, ok := r.b.vector(r.t, batchVariadic); ok && n > 0 {
				return put(r.b, start, 8)
			}
		}
	}
	return nil
}

type schemaAt struct {
	b fbuf
	t uint32
}

type batchAt = schemaAt

// batchesOf is the record batch of a message's metadata, a dictionary batch's among
// them.
func batchesOf(meta fbuf) []batchAt {
	root, ok := meta.u32(0)
	if !ok {
		return nil
	}
	at, _ := meta.field(root, msgHeaderType)
	kind, ok := meta.u8(at)
	if !ok {
		return nil
	}
	h, ok := meta.ref(root, msgHeader)
	if !ok {
		return nil
	}
	switch kind {
	case headerRecord:
		return []batchAt{{meta, h}}
	case headerDictionary:
		if d, ok := meta.ref(h, dictData); ok {
			return []batchAt{{meta, d}}
		}
	}
	return nil
}
