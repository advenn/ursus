package readfuzz_test

import (
	"encoding/binary"
	"fmt"
)

// A walk of an Arrow IPC input's flatbuffers metadata, for what is left of the finding
// of step 511 the IPC targets skip: a record batch declaring more rows than its input
// could hold, of columns that cost no input a row and that ursus holds a bit a row
// of, a Struct of none but Null fields, say (oversized). A record of Null columns, or
// of none, is read in constant space since step 529, and is not skipped.
//
// Step 511's walk here also found what arrow-go sized a slice by past its buffer, and
// a field among its own descendants, which ended the process; step 519 walks every
// footer and message in ursus before arrow-go reads it (internal/arrowmeta), so the
// targets read those inputs, and ursus refuses them.

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
	msgHeaderType, msgHeader, msgBodyLength               = 1, 2, 3
	schemaFields, schemaCustomMetadata                    = 1, 2
	fieldTypeType, fieldDictionary, fieldChildren         = 2, 4, 5
	batchLength, batchVariadic                            = 0, 4
	dictData                                              = 1
	footerSchema, footerDictionaries, footerRecordBatches = 1, 2, 3
)

// The message headers walked.
const (
	headerSchema     = 1
	headerDictionary = 2
	headerRecord     = 3
)

// typeNull is the Null type's tag in Schema.fbs's Type union.
const typeNull = 1

// nullOnly says whether the schema at s holds only columns of the Null type, not
// dictionary-encoded, or none: a record of which ursus holds as its row count (step
// 529), however many rows it declares.
func nullOnly(b fbuf, s uint32) bool {
	start, n, ok := b.vector(s, schemaFields)
	if !ok {
		return true // no fields
	}
	if b.past(start, n, 4) {
		return false
	}
	for i := range n {
		f, ok := b.elem(start, i)
		if !ok {
			return false
		}
		at, ok := b.field(f, fieldTypeType)
		if !ok {
			return false
		}
		if tag, ok := b.u8(at); !ok || tag != typeNull {
			return false
		}
		if _, ok := b.field(f, fieldDictionary); ok {
			return false
		}
	}
	return true
}

// oversized names what in b, an IPC file or stream, the targets skip as the finding
// left for the maintainer, "" for nothing: a walk of its metadata, which stops quietly
// at anything it cannot read, as ursus then refuses it.
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
		if s, ok := schemaOf(meta); ok && !w.schema {
			// arrow-go reads a stream as its first schema message's columns.
			w.schema, w.nullOnly = true, nullOnly(meta, s)
		}
		if why := w.message(meta); why != "" {
			return why
		}
		pos = next
	}
	return ""
}

// walk is what a walk of one input knows: its size, and whether its schema, once
// read, holds only Null columns or none.
type walk struct {
	input    int
	schema   bool
	nullOnly bool
}

// schemaOf is the schema a message's metadata holds, false for another message.
func schemaOf(meta fbuf) (uint32, bool) {
	root, ok := meta.u32(0)
	if !ok {
		return 0, false
	}
	at, _ := meta.field(root, msgHeaderType)
	if kind, ok := meta.u8(at); !ok || kind != headerSchema {
		return 0, false
	}
	return meta.ref(root, msgHeader)
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

// message is oversized of one message's metadata: its record batch's, a dictionary
// batch's among them.
func (w *walk) message(meta fbuf) string {
	root, _ := meta.u32(0)
	at, _ := meta.field(root, msgHeaderType)
	kind, _ := meta.u8(at)
	for _, r := range batchesOf(meta) {
		if kind == headerRecord && w.schema && w.nullOnly {
			// Read in constant space since step 529, whatever it declares.
			continue
		}
		if why := w.batch(r.b, r.t); why != "" {
			return why
		}
	}
	return ""
}

// batch says whether a record batch declares more rows than the input holds of any
// type with a buffer: a row of such a type costs a bit at least, and a Null column
// none, nor a Struct of Null fields or a FixedSizeList of Null, so a 600-byte stream
// declaring 2^40 rows had ursus copy batches of them, and allocate, until the harness
// ended the process at four times its bound. ursus holds a record of only Null
// columns, or of none, as its row count since step 529, and message lets one
// through; it still builds a validity bit a row of a Struct, a FixedSizeList and the
// rest, which are skipped, and so is a dictionary batch.
func (w *walk) batch(b fbuf, batch uint32) string {
	if at, ok := b.field(batch, batchLength); ok {
		if rows, ok := b.i64(at); ok && rows > 8*int64(w.input)+4096 {
			return fmt.Sprintf("a record batch declares %d rows in an input of %d bytes, which only "+
				"columns of no data can hold, and ursus holds a bit a row of one not Null", rows, w.input)
		}
	}
	return ""
}

// fileOversized is oversized of an IPC file: the message of each block its footer
// lists.
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
	if s, ok := footer.ref(root, footerSchema); ok {
		// arrow-go reads a file as its footer's columns.
		w.schema, w.nullOnly = true, nullOnly(footer, s)
	}
	for _, id := range []int{footerDictionaries, footerRecordBatches} {
		start, n, ok := footer.vector(root, id)
		if !ok {
			continue
		}
		for i := 0; i < n && !footer.past(start, i+1, 24); i++ {
			meta, ok := blockMessage(b, footer, start+uint32(24*i))
			if !ok {
				continue
			}
			if why := w.message(meta); why != "" {
				return why
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
// v, for the seeds of step 511's metadata findings, which step 519's walk refuses:
// what is "fields", a schema's fields;
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
