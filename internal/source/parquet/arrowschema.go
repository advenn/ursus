package parquet

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"

	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/parquet/metadata"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/uerr"
)

// An Array in Parquet (step 341).
//
// Parquet has no fixed-size list: Polars and PyArrow write an Array as a LIST, and
// say what it was in the footer, under "ARROW:schema", the Arrow schema of the file
// as an IPC stream of no batch, in base64. They read it back as an Array from there,
// and so does ursus: a top-level LIST that schema calls a FixedSizeList is an Array
// of its size, each of its rows checked to have it as it is read (listCol.closeRow).
// ursus writes the key when a column is an Array, so Polars reads one back as it
// wrote it, and leaves every other file's footer as it was.
//
// The key is read here, not by arrow-go's IPC reader (step 510). The footer is the
// file's, and a file's to forge: arrow-go's ipc.NewReader trusts the lengths in the
// schema's flatbuffer, and a field claiming 2^32 children had its fieldFromFB make
// a slice of 283 GB, a fatal "out of memory" no recover catches, from a file of 906
// bytes. So the flatbuffer is walked with every offset and length checked against
// its bytes, for the two facts wanted of each top-level field: a FixedSizeList's
// size, and whether it is a dictionary of text.

// arrowSchemaKey is the footer key Arrow's writers keep their schema under.
const arrowSchemaKey = "ARROW:schema"

// arraysOf is the size of each top-level column the footer's Arrow schema calls a
// FixedSizeList, by name, and -1 for each it calls a dictionary of text, a
// Categorical (step 352); nil without one. A schema that does not decode is passed
// over: the Parquet types stand.
func arraysOf(kv metadata.KeyValueMetadata) map[string]int {
	v := kv.FindValue(arrowSchemaKey)
	if v == nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(*v)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(*v); err != nil {
			return nil
		}
	}
	fields, ok := schemaFields(raw)
	if !ok {
		return nil
	}
	var out map[string]int
	for _, f := range fields {
		if f.size != 0 {
			if out == nil {
				out = make(map[string]int)
			}
			out[f.name] = f.size
		}
	}
	return out
}

// arrowField is what arraysOf reads of a top-level field of an Arrow schema: its
// name, and its size as arraysOf counts it, 0 for any field it passes over.
type arrowField struct {
	name string
	size int
}

// The Arrow format's numbers (format/Message.fbs and Schema.fbs): a message's header
// that is a Schema, and the types of a field that arraysOf reads.
const (
	fbHeaderSchema      = 1
	fbTypeUtf8          = 5
	fbTypeFixedSizeList = 16
	fbTypeLargeUtf8     = 20
	fbTypeUtf8View      = 24
)

// schemaFields reads the top-level fields of the Arrow schema in raw, an IPC stream
// whose first message is the schema, as Arrow's writers keep it under arrowSchemaKey.
// ok is false for bytes that are not one: an offset or a length past the bytes, a
// first message that is not a schema.
//
// A message is a 32-bit length, after the continuation marker 0xFFFFFFFF where the
// writer wrote one, and a flatbuffer of that length. Its Message table's header is a
// union, its type field 1 and its table field 2; a Schema table's fields are its
// field 1; a Field table's name is field 0, its type a union of fields 2 and 3, its
// dictionary field 4, and a FixedSizeList table's listSize field 0.
func schemaFields(raw []byte) (_ []arrowField, ok bool) {
	b := flatbuf(raw)
	at := 0
	n, ok := b.u32(at)
	if ok && n == 0xFFFFFFFF {
		at = 4
		n, ok = b.u32(at)
	}
	if !ok || n == 0 || n > len(raw)-at-4 {
		return nil, false
	}
	b = b[at+4 : at+4+n]

	msg, ok := b.deref(0)
	if !ok {
		return nil, false
	}
	if typ, ok := b.byteField(msg, 1); !ok || typ != fbHeaderSchema {
		return nil, false
	}
	schema, ok := b.tableField(msg, 2)
	if !ok || schema == 0 {
		return nil, false
	}
	vec, ok := b.refField(schema, 1)
	if !ok || vec == 0 {
		return nil, ok
	}
	count, ok := b.vector(vec, 4)
	if !ok {
		return nil, false
	}
	out := make([]arrowField, 0, count)
	for i := range count {
		f, ok := b.deref(vec + 4 + 4*i)
		if !ok {
			return nil, false
		}
		var af arrowField
		if s, ok := b.refField(f, 0); !ok {
			return nil, false
		} else if s != 0 {
			if af.name, ok = b.str(s); !ok {
				return nil, false
			}
		}
		typ, ok := b.byteField(f, 2)
		if !ok {
			return nil, false
		}
		dict, ok := b.tableField(f, 4)
		if !ok {
			return nil, false
		}
		switch {
		case typ == fbTypeFixedSizeList:
			t, ok := b.tableField(f, 3)
			if !ok || t == 0 {
				return nil, false
			}
			size, ok := b.i32Field(t, 0)
			if !ok {
				return nil, false
			}
			af.size = int(size)
		case dict != 0 && (typ == fbTypeUtf8 || typ == fbTypeLargeUtf8 || typ == fbTypeUtf8View):
			af.size = categorical
		}
		out = append(out, af)
	}
	return out, true
}

// flatbuf is a flatbuffer, every read of it checked against its length.
type flatbuf []byte

func (b flatbuf) u32(at int) (int, bool) {
	if at < 0 || at > len(b)-4 {
		return 0, false
	}
	return int(binary.LittleEndian.Uint32(b[at:])), true
}

func (b flatbuf) u16(at int) (int, bool) {
	if at < 0 || at > len(b)-2 {
		return 0, false
	}
	return int(binary.LittleEndian.Uint16(b[at:])), true
}

// deref follows the offset at at, to the table, vector or string it points at.
func (b flatbuf) deref(at int) (int, bool) {
	off, ok := b.u32(at)
	if !ok || off == 0 || off >= len(b)-at {
		return 0, false
	}
	return at + off, true
}

// field is where table t holds its field i, or 0 where it leaves it out.
func (b flatbuf) field(t, i int) (int, bool) {
	so, ok := b.u32(t)
	if !ok {
		return 0, false
	}
	vt := t - int(int32(uint32(so)))
	size, ok := b.u16(vt)
	if !ok || size < 4 || size > len(b)-vt {
		return 0, false
	}
	if 4+2*i >= size {
		return 0, true
	}
	off, ok := b.u16(vt + 4 + 2*i)
	if !ok || off == 0 {
		return 0, ok
	}
	if off >= len(b)-t {
		return 0, false
	}
	return t + off, true
}

// byteField is table t's field i as a byte, 0 where it is left out.
func (b flatbuf) byteField(t, i int) (int, bool) {
	at, ok := b.field(t, i)
	if !ok || at == 0 {
		return 0, ok
	}
	return int(b[at]), true
}

// i32Field is table t's field i as a 32-bit integer, 0 where it is left out.
func (b flatbuf) i32Field(t, i int) (int32, bool) {
	at, ok := b.field(t, i)
	if !ok || at == 0 {
		return 0, ok
	}
	v, ok := b.u32(at)
	return int32(uint32(v)), ok
}

// refField is where table t's field i, an offset, points: 0 where it is left out.
func (b flatbuf) refField(t, i int) (int, bool) {
	at, ok := b.field(t, i)
	if !ok || at == 0 {
		return 0, ok
	}
	return b.deref(at)
}

// tableField is refField of a field that is a table.
func (b flatbuf) tableField(t, i int) (int, bool) { return b.refField(t, i) }

// vector is the length of the vector at v, whose elements are size bytes each, all
// of them within the bytes.
func (b flatbuf) vector(v, size int) (int, bool) {
	n, ok := b.u32(v)
	if !ok || n > (len(b)-v-4)/size {
		return 0, false
	}
	return n, true
}

// str is the string at s.
func (b flatbuf) str(s int) (string, bool) {
	n, ok := b.vector(s, 1)
	if !ok {
		return "", false
	}
	return string(b[s+4 : s+4+n]), true
}

// categorical marks, in arraysOf, a column the footer calls a dictionary of text.
const categorical = -1

// hasArray reports whether s holds an Array or a Categorical at its top level, which
// the footer's Arrow schema names.
func hasArray(s *dtype.Schema) bool {
	for _, f := range s.All() {
		if f.Type.ID() == dtype.TypeArray || f.Type.ID() == dtype.TypeCategorical {
			return true
		}
	}
	return false
}

// arrowSchemaValue is s's Arrow schema as Arrow's writers keep it in a footer.
func arrowSchemaValue(s *dtype.Schema) (string, error) {
	as, err := arrowout.Schema(s)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	w := ipc.NewWriter(&b, ipc.WithSchema(as))
	if err := w.Close(); err != nil {
		return "", uerr.Wrap(err, uerr.KindInternal, "sink_parquet", "writing the Arrow schema")
	}
	return base64.StdEncoding.EncodeToString(b.Bytes()), nil
}
