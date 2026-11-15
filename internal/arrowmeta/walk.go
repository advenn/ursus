// Package arrowmeta walks Arrow IPC metadata, its flatbuffers, before arrow-go reads
// it (step 519).
//
// arrow-go v18.7.0 trusts every length an IPC footer or message declares: Go's
// flatbuffers has no verifier, and arrow-go sizes slices by what the metadata says
// before it reads what the slices would hold. A forged file of 0.5 to 2 KB ended the
// process six ways (step 511, §5), with "fatal error: runtime: out of memory" or
// "stack overflow", which no recover catches:
//
//   - schemaFromFB, make([]arrow.Field, schema.FieldsLength()) (arrow/ipc/metadata.go);
//   - fieldFromFB, make([]arrow.Field, field.ChildrenLength());
//   - metadataFromFB, two make([]string, CustomMetadataLength()), of a schema's, a
//     field's or a message's key-values;
//   - loadCommon, make([]*memory.Buffer, 0, 2+variadicCount) of a view column
//     (arrow/ipc/file_reader.go), in a record batch and in a dictionary batch;
//   - fieldFromFB recursing once a level, on a field among its own descendants, which
//     an offset past 2^31 makes, since flatbuffers' positions add in uint32.
//
// So every footer and message is walked here first, in the bytes arrow-go is about to
// read, and refused as corrupt, KindIO, if the walk finds what arrow-go would trust:
//
//   - every table, its vtable, every field's bytes, and every offset to a table, a
//     vector or a string lies within the bytes, positions added as flatbuffers' Go
//     accessors add them, wrapping, so the walk reads what arrow-go reads;
//   - every offset points forward, past itself, so no field is its own descendant,
//     and fields nest no deeper than MaxDepth;
//   - every vector's count is what its bytes can hold, elements of their size;
//   - the fields, key-values and strings a walk visits, which a shared table can
//     visit more than once, cost no more than a bound linear in the bytes, so a few
//     kilobytes cannot make arrow-go build millions;
//   - a record batch's buffers lie within its body, and its variadic counts, one for
//     each view column it holds, are no more than its buffers.
//
// File serves an IPC file to arrow-go's file reader, its footer walked when it is
// opened and each block's message as arrow-go reads it; NewMessageReader reads a
// stream's messages for arrow-go's stream reader, each walked before it is handed on.
// SchemaFields walks the Arrow schema a Parquet footer keeps (step 510).
package arrowmeta

import (
	"encoding/binary"
	"fmt"
)

// MaxDepth is how deeply fields may nest, a top-level field at 1: arrow-go's
// fieldFromFB recurses once a level, with no limit of its own, and a schema 16,000
// deep overflowed the 8 MiB stack the fuzz targets run on (step 511). A List nested
// 1,000 deep is 1,001 fields; ursus reads one (TestScanIPCDeepSchema).
const MaxDepth = 2_000

// maxBuffer bounds the bytes one walk takes: IPC lengths are 32-bit, and below 2^31
// no position that flatbuffers' uint32 arithmetic wraps lands inside the bytes but
// before the offset it was read from, so a backward offset is seen as one.
const maxBuffer = 1<<31 - 1

// The walk's budget: what each field and each key-value it visits costs, a string
// its bytes, against budgetPerByte times the bytes walked plus budgetSlack. Each is
// several times what one costs in the bytes, which flatbuffers' builders never
// share, so no well-formed metadata comes near it; a field reached twice, which a
// forged offset makes, costs twice, as it costs arrow-go twice.
const (
	fieldCost     = 64
	keyValueCost  = 32
	budgetPerByte = 16
	budgetSlack   = 1 << 20
)

// Type is a field's type, the tag of Schema.fbs's Type union.
type Type uint8

// The Type union's tags (format/Schema.fbs).
const (
	TypeNone Type = iota
	TypeNull
	TypeInt
	TypeFloatingPoint
	TypeBinary
	TypeUtf8
	TypeBool
	TypeDecimal
	TypeDate
	TypeTime
	TypeTimestamp
	TypeInterval
	TypeList
	TypeStruct
	TypeUnion
	TypeFixedSizeBinary
	TypeFixedSizeList
	TypeMap
	TypeDuration
	TypeLargeBinary
	TypeLargeUtf8
	TypeLargeList
	TypeRunEndEncoded
	TypeBinaryView
	TypeUtf8View
	TypeListView
	TypeLargeListView
)

// nested says whether arrow-go builds t from its field's children: a type of any
// other tag passes them over, though it still reads them (fieldFromFB).
func (t Type) nested() bool {
	switch t {
	case TypeList, TypeStruct, TypeUnion, TypeFixedSizeList, TypeMap, TypeLargeList,
		TypeRunEndEncoded, TypeListView, TypeLargeListView:
		return true
	}
	return false
}

// view says whether a column of t has variadic buffers, a count of them in each
// batch that holds it.
func (t Type) view() bool { return t == TypeBinaryView || t == TypeUtf8View }

// messageType is a message's header, the tag of Message.fbs's MessageHeader union.
type messageType uint8

const (
	messageNone messageType = iota
	messageSchema
	messageDictionary
	messageRecord
	messageTensor
	messageSparseTensor
)

func (m messageType) String() string {
	switch m {
	case messageSchema:
		return "Schema"
	case messageDictionary:
		return "DictionaryBatch"
	case messageRecord:
		return "RecordBatch"
	case messageTensor:
		return "Tensor"
	case messageSparseTensor:
		return "SparseTensor"
	}
	return fmt.Sprintf("header type %d", uint8(m))
}

// Field is a top-level field of a schema the walk read, as much of it as Parquet's
// footer reading asks (step 510): its name and type, a FixedSizeList's size, and
// whether it is dictionary-encoded.
type Field struct {
	Name       string
	Type       Type
	ListSize   int32 // a FixedSizeList's size, 0 for any other type
	Dictionary bool
}

// Schema is what a walk of a Schema table found: its top-level fields, and what the
// batches after it are walked against.
type Schema struct {
	Fields []Field

	// views is how many view columns a record batch of the schema holds, nested
	// ones among them, but none of a dictionary's values, which a dictionary batch
	// holds: arrow-go reads a variadic count for each.
	views int

	// dicts is how many view columns each dictionary's values hold, by id. Its
	// length is how many dictionaries arrow-go reads before a stream's first record
	// batch (Mapper.NumDicts).
	dicts map[int64]int
}

// corrupt is what a walk found wrong.
type corrupt string

func (c corrupt) Error() string { return string(c) }

// fb is a flatbuffer, read only where has says its bytes are.
type fb []byte

func (b fb) has(at uint32, n uint64) bool { return uint64(at)+n <= uint64(len(b)) }
func (b fb) u8(at uint32) uint8           { return b[at] }
func (b fb) u16(at uint32) uint16         { return binary.LittleEndian.Uint16(b[at:]) }
func (b fb) u32(at uint32) uint32         { return binary.LittleEndian.Uint32(b[at:]) }
func (b fb) i64(at uint32) int64          { return int64(binary.LittleEndian.Uint64(b[at:])) }

// walker walks one flatbuffer.
type walker struct {
	b    fb
	left int64 // the budget left
	// zstd is where each buffer of a batch walked lies in its body, if the batch is
	// compressed by Zstandard: its frames are checked once the body is read
	// (checkZstd).
	zstd zstdBuffers
}

// zstdBuffers is a Zstandard batch's buffers: what the batch is, and where each lies
// in its body.
type zstdBuffers struct {
	what  string
	spans []span
}

// span is a buffer of a batch, by its index, and its place in the batch's body.
type span struct {
	buf    uint32
	off, n int64
}

func newWalker(b []byte) (*walker, error) {
	if len(b) > maxBuffer {
		return nil, corrupt(fmt.Sprintf("%d bytes of metadata, more than an IPC length can declare", len(b)))
	}
	return &walker{b: b, left: budgetPerByte*int64(len(b)) + budgetSlack}, nil
}

func (w *walker) fail(format string, args ...any) error {
	return corrupt(fmt.Sprintf(format, args...))
}

// spend takes n from the budget, for what.
func (w *walker) spend(n int64, what string) error {
	if w.left -= n; w.left < 0 {
		return w.fail("its %s cost more than %d bytes of metadata can, a table reached more than once",
			what, len(w.b))
	}
	return nil
}

// table is a table: where it is, and its vtable and the vtable's size.
type table struct {
	at, vt uint32
	size   uint16
}

// table checks the table at at, what in refusals.
func (w *walker) table(at uint32, what string) (table, error) {
	if !w.b.has(at, 4) {
		return table{}, w.fail("%s at byte %d runs past the %d bytes", what, at, len(w.b))
	}
	// As flatbuffers' Table.Offset finds it: the table's position less its
	// soffset, in 32 bits.
	vt := uint32(int32(at) - int32(w.b.u32(at)))
	if !w.b.has(vt, 4) {
		return table{}, w.fail("%s at byte %d has its vtable at byte %d, past the %d bytes", what, at, vt, len(w.b))
	}
	size := w.b.u16(vt)
	if size < 4 || size%2 != 0 || !w.b.has(vt, uint64(size)) {
		return table{}, w.fail("%s at byte %d has a vtable of %d bytes at byte %d, in %d", what, at, size, vt, len(w.b))
	}
	return table{at: at, vt: vt, size: size}, nil
}

// slot is where field id of t is, its n bytes within the bytes; false where t leaves
// it out.
func (w *walker) slot(t table, id int, n uint64, what string) (uint32, bool, error) {
	vo := uint32(4 + 2*id)
	if vo >= uint32(t.size) {
		return 0, false, nil
	}
	off := w.b.u16(t.vt + vo)
	if off == 0 {
		return 0, false, nil
	}
	at := t.at + uint32(off)
	if !w.b.has(at, n) {
		return 0, false, w.fail("%s at byte %d runs past the %d bytes", what, at, len(w.b))
	}
	return at, true, nil
}

// scalar checks that field id of t, n bytes, is within the bytes where it is there.
func (w *walker) scalar(t table, id int, n uint64, what string) error {
	_, _, err := w.slot(t, id, n, what)
	return err
}

// follow is where the offset at at points, as flatbuffers' Indirect adds it, in 32
// bits: a table, a vector or a string, four bytes of it at least within the bytes.
// It must point past at. A builder writes what an offset refers to before the
// offset, at a higher position, so an offset pointing back, which a value past 2^31
// makes as the sum wraps, is forged; and a field's children, reached only forward,
// cannot include the field.
func (w *walker) follow(at uint32, what string) (uint32, error) {
	to := at + w.b.u32(at)
	if to <= at {
		return 0, w.fail("%s at byte %d points back to byte %d: offsets point forward, "+
			"and a field cannot be its own descendant", what, at, to)
	}
	if !w.b.has(to, 4) {
		return 0, w.fail("%s at byte %d points to byte %d, past the %d bytes", what, at, to, len(w.b))
	}
	return to, nil
}

// ref is where field id of t, an offset, points; false where t leaves it out.
func (w *walker) ref(t table, id int, what string) (uint32, bool, error) {
	at, ok, err := w.slot(t, id, 4, what)
	if !ok || err != nil {
		return 0, false, err
	}
	to, err := w.follow(at, what)
	return to, err == nil, err
}

// vector is the vector at field id of t, of elements of size bytes: where its
// elements start, and how many it holds, all of them within the bytes.
func (w *walker) vector(t table, id int, size uint64, what string) (start, n uint32, ok bool, err error) {
	v, ok, err := w.ref(t, id, what)
	if !ok || err != nil {
		return 0, 0, false, err
	}
	n = w.b.u32(v)
	if uint64(n)*size > uint64(len(w.b))-uint64(v)-4 {
		return 0, 0, false, w.fail("%s declare %d elements of %d bytes at byte %d, in %d bytes",
			what, n, size, v, len(w.b))
	}
	return v + 4, n, true, nil
}

// elem is where the i-th offset of a vector of offsets from start points.
func (w *walker) elem(start, i uint32, what string) (uint32, error) {
	return w.follow(start+4*i, what)
}

// text is the string at field id of t: where its bytes start and how many.
func (w *walker) text(t table, id int, what string) (start, n uint32, err error) {
	s, ok, err := w.ref(t, id, what)
	if !ok || err != nil {
		return 0, 0, err
	}
	n = w.b.u32(s)
	if uint64(n) > uint64(len(w.b))-uint64(s)-4 {
		return 0, 0, w.fail("%s at byte %d declares %d bytes, in %d", what, s, n, len(w.b))
	}
	return s + 4, n, w.spend(int64(n), "strings")
}

// root is the table the flatbuffer's first four bytes point to.
func (w *walker) root(what string) (table, error) {
	if !w.b.has(0, 4) {
		return table{}, w.fail("%s of %d bytes", what, len(w.b))
	}
	at, err := w.follow(0, what)
	if err != nil {
		return table{}, err
	}
	return w.table(at, what)
}

// keyValues walks the custom metadata at field id of t, a vector of KeyValue
// tables, each a key and a value.
func (w *walker) keyValues(t table, id int, what string) error {
	start, n, _, err := w.vector(t, id, 4, what)
	if err != nil {
		return err
	}
	for i := range n {
		if err := w.spend(keyValueCost, "key-values"); err != nil {
			return err
		}
		at, err := w.elem(start, i, what)
		if err != nil {
			return err
		}
		kv, err := w.table(at, "a key-value")
		if err != nil {
			return err
		}
		if _, _, err := w.text(kv, 0, "a key"); err != nil {
			return err
		}
		if _, _, err := w.text(kv, 1, "a value"); err != nil {
			return err
		}
	}
	return nil
}

// schema walks the Schema table at at.
func (w *walker) schema(at uint32) (*Schema, error) {
	t, err := w.table(at, "a schema")
	if err != nil {
		return nil, err
	}
	if err := w.scalar(t, 0, 2, "a schema's endianness"); err != nil {
		return nil, err
	}
	s := &Schema{dicts: map[int64]int{}}
	start, n, _, err := w.vector(t, 1, 4, "a schema's fields")
	if err != nil {
		return nil, err
	}
	for i := range n {
		f, err := w.elem(start, i, "a schema's field")
		if err != nil {
			return nil, err
		}
		views, err := w.field(f, 1, s)
		if err != nil {
			return nil, err
		}
		s.views += views
	}
	if err := w.keyValues(t, 2, "a schema's custom metadata"); err != nil {
		return nil, err
	}
	if _, _, _, err := w.vector(t, 3, 8, "a schema's features"); err != nil {
		return nil, err
	}
	return s, nil
}

// field walks the Field table at at, depth levels down, a top-level field at 1, into
// s: its name, its type, its dictionary encoding, its children and its custom
// metadata, as fieldFromFB reads them. It is how many view columns a batch holding
// it counts: those of its type, and of its children where its type is built of
// them, but none for a dictionary-encoded field, whose values a dictionary batch
// holds; those are its dictionary's, in s.
func (w *walker) field(at uint32, depth int, s *Schema) (int, error) {
	if depth > MaxDepth {
		return 0, w.fail("fields nest past %d levels, which arrow-go's fieldFromFB recurses on", MaxDepth)
	}
	if err := w.spend(fieldCost, "fields"); err != nil {
		return 0, err
	}
	t, err := w.table(at, "a field")
	if err != nil {
		return 0, err
	}
	nameAt, nameLen, err := w.text(t, 0, "a field's name")
	if err != nil {
		return 0, err
	}
	if err := w.scalar(t, 1, 1, "a field's nullable"); err != nil {
		return 0, err
	}
	typ := TypeNone
	if p, ok, err := w.slot(t, 2, 1, "a field's type tag"); err != nil {
		return 0, err
	} else if ok {
		typ = Type(w.b.u8(p))
	}
	tt, ok, err := w.ref(t, 3, "a field's type")
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, w.fail("a field at byte %d has no type", at)
	}
	size, err := w.typeTable(typ, tt)
	if err != nil {
		return 0, err
	}
	enc, dict, err := w.ref(t, 4, "a field's dictionary encoding")
	if err != nil {
		return 0, err
	}
	var id int64
	if dict {
		if id, err = w.encoding(enc); err != nil {
			return 0, err
		}
	}
	start, n, _, err := w.vector(t, 5, 4, "a field's children")
	if err != nil {
		return 0, err
	}
	views := 0
	for i := range n {
		c, err := w.elem(start, i, "a field's child")
		if err != nil {
			return 0, err
		}
		v, err := w.field(c, depth+1, s)
		if err != nil {
			return 0, err
		}
		if typ.nested() {
			views += v
		}
	}
	if err := w.keyValues(t, 6, "a field's custom metadata"); err != nil {
		return 0, err
	}
	if typ.view() {
		views++
	}
	if depth == 1 {
		s.Fields = append(s.Fields, Field{Name: string(w.b[nameAt : nameAt+nameLen]), Type: typ,
			ListSize: size, Dictionary: dict})
	}
	if dict {
		s.dicts[id] = max(s.dicts[id], views)
		return 0, nil
	}
	return views, nil
}

// typeTable walks the table at at of a field's type, typ, as concreteTypeFromFB reads
// it: the fields of typ's table. It is a FixedSizeList's size, 0 for any other type.
func (w *walker) typeTable(typ Type, at uint32) (int32, error) {
	t, err := w.table(at, "a field's type")
	if err != nil {
		return 0, err
	}
	scalars := func(sizes ...uint64) error {
		for id, n := range sizes {
			if err := w.scalar(t, id, n, "a field's type"); err != nil {
				return err
			}
		}
		return nil
	}
	switch typ {
	case TypeNull, TypeBinary, TypeUtf8, TypeBool, TypeList, TypeStruct, TypeLargeBinary, TypeLargeUtf8,
		TypeLargeList, TypeRunEndEncoded, TypeBinaryView, TypeUtf8View, TypeListView, TypeLargeListView:
		return 0, nil
	case TypeInt:
		return 0, scalars(4, 1) // bitWidth, is_signed
	case TypeFloatingPoint, TypeDate, TypeInterval, TypeDuration:
		return 0, scalars(2) // precision, or unit
	case TypeDecimal:
		return 0, scalars(4, 4, 4) // precision, scale, bitWidth
	case TypeTime:
		return 0, scalars(2, 4) // unit, bitWidth
	case TypeTimestamp:
		if err := scalars(2); err != nil { // unit
			return 0, err
		}
		_, _, err := w.text(t, 1, "a timestamp's time zone")
		return 0, err
	case TypeUnion:
		if err := scalars(2); err != nil { // mode
			return 0, err
		}
		_, _, _, err := w.vector(t, 1, 4, "a union's type ids")
		return 0, err
	case TypeFixedSizeBinary:
		return 0, scalars(4) // byteWidth
	case TypeMap:
		return 0, scalars(1) // keysSorted
	case TypeFixedSizeList:
		p, ok, err := w.slot(t, 0, 4, "a FixedSizeList's size")
		if !ok || err != nil {
			return 0, err
		}
		return int32(w.b.u32(p)), nil
	}
	return 0, w.fail("a field's type is tag %d, which no Arrow type has", typ)
}

// encoding walks a DictionaryEncoding table and returns its dictionary's id.
func (w *walker) encoding(at uint32) (int64, error) {
	t, err := w.table(at, "a dictionary encoding")
	if err != nil {
		return 0, err
	}
	var id int64
	if p, ok, err := w.slot(t, 0, 8, "a dictionary's id"); err != nil {
		return 0, err
	} else if ok {
		id = w.b.i64(p)
	}
	if it, ok, err := w.ref(t, 1, "a dictionary's index type"); err != nil {
		return 0, err
	} else if ok {
		if _, err := w.typeTable(TypeInt, it); err != nil {
			return 0, err
		}
	}
	if err := w.scalar(t, 2, 1, "a dictionary's isOrdered"); err != nil {
		return 0, err
	}
	return id, w.scalar(t, 3, 2, "a dictionary's kind")
}

// message walks the Message table at the root of the bytes: its version, its header
// (its type and where its table is), its body's length and its custom metadata.
func (w *walker) message() (kind messageType, header uint32, body int64, err error) {
	t, err := w.root("a message")
	if err != nil {
		return 0, 0, 0, err
	}
	if err := w.scalar(t, 0, 2, "a message's version"); err != nil {
		return 0, 0, 0, err
	}
	if p, ok, err := w.slot(t, 1, 1, "a message's header type"); err != nil {
		return 0, 0, 0, err
	} else if ok {
		kind = messageType(w.b.u8(p))
	}
	header, ok, err := w.ref(t, 2, "a message's header")
	if err != nil {
		return 0, 0, 0, err
	}
	if !ok {
		return 0, 0, 0, w.fail("a %s message has no header", kind)
	}
	if p, ok, err := w.slot(t, 3, 8, "a message's body length"); err != nil {
		return 0, 0, 0, err
	} else if ok {
		body = w.b.i64(p)
	}
	return kind, header, body, w.keyValues(t, 4, "a message's custom metadata")
}

// batch walks the RecordBatch table at at, of a body of body bytes, holding views
// view columns: its nodes, its buffers, each within the body, its compression, and
// its variadic counts. arrow-go reads a count for each view column, in the vector
// or past its end, and makes a slice of that many buffers, before it finds them
// missing; so the vector holds one for each, and none is more than the buffers.
func (w *walker) batch(at uint32, views int, body int64, what string) error {
	t, err := w.table(at, what)
	if err != nil {
		return err
	}
	if err := w.scalar(t, 0, 8, what+"'s length"); err != nil {
		return err
	}
	if _, _, _, err := w.vector(t, 1, 16, what+"'s nodes"); err != nil {
		return err
	}
	bs, bn, _, err := w.vector(t, 2, 16, what+"'s buffers")
	if err != nil {
		return err
	}
	for i := range bn {
		off, n := w.b.i64(bs+16*i), w.b.i64(bs+16*i+8)
		if off < 0 || n < 0 || off > body || n > body-off {
			return w.fail("%s's buffer %d of %d bytes at %d lies outside its body of %d", what, i, n, off, body)
		}
	}
	if c, ok, err := w.ref(t, 3, what+"'s compression"); err != nil {
		return err
	} else if ok {
		ct, err := w.table(c, what+"'s compression")
		if err != nil {
			return err
		}
		p, ok, err := w.slot(ct, 0, 1, "a compression codec")
		if err != nil {
			return err
		}
		if ok && w.b.u8(p) > 1 { // LZ4_FRAME, ZSTD
			return w.fail("%s is compressed by codec %d, which Arrow does not have", what, w.b.u8(p))
		}
		if ok && w.b.u8(p) == 1 {
			w.zstd.what = what
			for i := range bn {
				if off, n := w.b.i64(bs+16*i), w.b.i64(bs+16*i+8); n > 0 {
					w.zstd.spans = append(w.zstd.spans, span{i, off, n})
				}
			}
		}
		if err := w.scalar(ct, 1, 1, "a compression method"); err != nil {
			return err
		}
	}
	vs, vn, ok, err := w.vector(t, 4, 8, what+"'s variadic buffer counts")
	if err != nil || !ok {
		// Without the vector arrow-go reads each count as 0.
		return err
	}
	if int(vn) < views {
		return w.fail("%s has %d variadic buffer counts for its %d view columns", what, vn, views)
	}
	var sum int64
	for i := range vn {
		v := w.b.i64(vs + 8*i)
		if v < 0 || v > int64(bn) {
			return w.fail("%s declares %d variadic buffers for a view column, and has %d buffers", what, v, bn)
		}
		if int(i) < views {
			if sum += v; sum > int64(bn) {
				return w.fail("%s declares %d variadic buffers for its view columns, and has %d buffers", what, sum, bn)
			}
		}
	}
	return nil
}

// dictionaryBatch walks the DictionaryBatch table at at, of a body of body bytes:
// its id, and its data, a record batch of the dictionary's values, against s.
func (w *walker) dictionaryBatch(at uint32, s *Schema, body int64) error {
	t, err := w.table(at, "a dictionary batch")
	if err != nil {
		return err
	}
	var id int64
	if p, ok, err := w.slot(t, 0, 8, "a dictionary batch's id"); err != nil {
		return err
	} else if ok {
		id = w.b.i64(p)
	}
	data, ok, err := w.ref(t, 1, "a dictionary batch's data")
	if err != nil {
		return err
	}
	if !ok {
		return w.fail("a dictionary batch at byte %d has no data", at)
	}
	if err := w.scalar(t, 2, 1, "a dictionary batch's isDelta"); err != nil {
		return err
	}
	return w.batch(data, s.dicts[id], body, "a dictionary batch")
}

// SchemaFields is the top-level fields of the Arrow schema in raw, an IPC stream
// whose first message is a Schema, as Arrow's writers keep it in a Parquet footer
// under "ARROW:schema". Its first message is walked whole; an error says what was
// found wrong, bytes that are not one.
//
// A message is a 32-bit length, after the continuation marker 0xFFFFFFFF where the
// writer wrote one (Arrow 0.15 and after), and a flatbuffer of that length.
func SchemaFields(raw []byte) ([]Field, error) {
	b := fb(raw)
	var at uint32
	if !b.has(0, 4) {
		return nil, corrupt(fmt.Sprintf("an Arrow schema of %d bytes", len(raw)))
	}
	n := b.u32(0)
	if n == 0xFFFFFFFF {
		at = 4
		if !b.has(at, 4) {
			return nil, corrupt(fmt.Sprintf("an Arrow schema of %d bytes", len(raw)))
		}
		n = b.u32(at)
	}
	if n == 0 || uint64(n) > uint64(len(raw))-uint64(at)-4 {
		return nil, corrupt(fmt.Sprintf("an Arrow schema's message declares %d bytes, in %d", n, len(raw)))
	}
	w, err := newWalker(raw[at+4 : at+4+n])
	if err != nil {
		return nil, err
	}
	kind, header, _, err := w.message()
	if err != nil {
		return nil, err
	}
	if kind != messageSchema {
		return nil, w.fail("an Arrow schema's message is a %s", kind)
	}
	s, err := w.schema(header)
	if err != nil {
		return nil, err
	}
	return s.Fields, nil
}
