package parquet

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/apache/arrow-go/v18/parquet"

	"github.com/advenn/ursus/internal/uerr"
)

// A footer's schema, measured before it is decoded (step 423).
//
// A Parquet schema is a flat list of elements in its footer, each group saying how
// many children follow it, and the tree it describes is as deep as its groups nest:
// a crafted footer of a few megabytes, groups each holding one, nests them hundreds
// of thousands deep. Nesting is capped: the footer is walked here first, with a
// stack of its own, and a schema nested past maxSchemaDepth is refused before
// anything recurses on it.

// maxSchemaDepth is the deepest a column's schema may nest, its leaf counted: the
// JSON readers' limit (step 412).
const maxSchemaDepth = 1_000

// thriftDepth bounds how deeply one value of a footer may nest in structs and lists:
// a real footer's do some ten deep, and a value past it is corrupt.
const thriftDepth = 64

// A footer's lists, sized against their bytes (step 521).
//
// A Thrift list's header carries a count of its elements, and a count is a claim. So
// the walk reads every list of the footer, and refuses one claiming more elements
// than the bytes after its header hold, each element taking a byte at least. A
// footer is walked wherever it lies, one too long to leave the file's leading magic
// room as well.
//
// Every list as parquet.thrift types it: a reader generated from parquet.thrift takes
// a list's elements for the type parquet.thrift gives them, whatever type the list's
// header says. A list of row groups said to hold binaries is read as row groups, and
// a list of 10^8 column chunks inside one is a list such a reader reads, which a walk
// by the header's types skips as the bytes of a binary. footerFields is where
// parquet.thrift has a list: the structs that hold one, in a field of their own or of
// a struct they hold, and the type each such field is read as. A field not read so,
// another field or one whose header gives it another type, is skipped as its header
// says, by the walk as by such a reader (skip).

// footerStruct is a struct of parquet.thrift as the walk reads it.
type footerStruct uint8

const (
	plainStruct   footerStruct = iota // holds no list the walk reads, walked as its bytes say
	schemaElement                     // holds no list; its num_children are kept (schemaDepth)
	fileMetaData
	rowGroup
	columnChunk
	columnMetaData
	sizeStatistics
	geospatialStatistics
	columnCryptoMetaData
	encryptionWithColumnKey
)

// footerField is a field read as a list, of elements of compact type elem, or as a
// struct; of is the struct, or a list's elements' where they are structs.
type footerField struct {
	list bool
	elem byte
	of   footerStruct
}

// The compact protocol's types the walk reads.
const (
	thriftI32    = 5
	thriftI64    = 6
	thriftBinary = 8
	thriftList   = 9
	thriftStruct = 12
)

// footerFields is, for each footerStruct, its fields parquet.thrift has as lists or
// as structs that hold one: every list reachable from FileMetaData, the page
// indexes', read apart, aside.
var footerFields = [...]map[int16]footerField{
	fileMetaData: {
		2: {list: true, elem: thriftStruct, of: schemaElement}, // schema
		4: {list: true, elem: thriftStruct, of: rowGroup},      // row_groups
		5: {list: true, elem: thriftStruct},                    // key_value_metadata
		7: {list: true, elem: thriftStruct},                    // column_orders
	},
	rowGroup: {
		1: {list: true, elem: thriftStruct, of: columnChunk}, // columns
		4: {list: true, elem: thriftStruct},                  // sorting_columns
	},
	columnChunk: {
		3: {elem: thriftStruct, of: columnMetaData},       // meta_data
		8: {elem: thriftStruct, of: columnCryptoMetaData}, // crypto_metadata
	},
	columnMetaData: {
		2:  {list: true, elem: thriftI32},                  // encodings
		3:  {list: true, elem: thriftBinary},               // path_in_schema
		8:  {list: true, elem: thriftStruct},               // key_value_metadata
		13: {list: true, elem: thriftStruct},               // encoding_stats
		16: {elem: thriftStruct, of: sizeStatistics},       // size_statistics
		17: {elem: thriftStruct, of: geospatialStatistics}, // geospatial_statistics
	},
	sizeStatistics: {
		2: {list: true, elem: thriftI64}, // repetition_level_histogram
		3: {list: true, elem: thriftI64}, // definition_level_histogram
	},
	geospatialStatistics:    {2: {list: true, elem: thriftI32}},                     // geospatial_types
	columnCryptoMetaData:    {2: {elem: thriftStruct, of: encryptionWithColumnKey}}, // ENCRYPTION_WITH_COLUMN_KEY
	encryptionWithColumnKey: {1: {list: true, elem: thriftBinary}},                  // path_in_schema
}

var (
	errFooter     = errors.New("the footer is not Thrift's compact protocol")
	errFooterList = errors.New("a list in the footer claims more elements than the bytes after it hold")
)

// measured is ra with its end, the footer and the 8 bytes after it, read once into
// tail: the file reader's two reads there are answered from it, so measuring the
// footer adds no byte and no read to the file reader's, each a ranged GET where the
// file is in an object store.
type measured struct {
	parquet.ReaderAtSeeker
	from int64 // where tail begins in the file
	tail []byte

	// chunks holds where each column chunk begins, and what its pages are checked
	// for, once watchChunks has read them from the footer: a read from there is a
	// chunk's, and its page headers are checked (dictpages.go).
	chunks map[int64]chunkWatch
}

func (m *measured) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= m.from && off-m.from <= int64(len(m.tail)) {
		n = copy(p, m.tail[off-m.from:])
		if n < len(p) {
			err = io.EOF
		}
	} else {
		n, err = m.ReaderAtSeeker.ReadAt(p, off)
	}
	if w, ok := m.chunks[off]; ok && n > 0 {
		if e := countDictionaries(p[:n], w); e != nil {
			return n, e
		}
	}
	return n, err
}

// measureFooter reads ra's footer and refuses one whose schema nests past
// maxSchemaDepth, or that cannot be walked, a list in it claiming more than its bytes
// among them, or whose schema's tree claims more children than it holds (step 533).
// A file too short, one not ending in a plain footer or one whose footer is longer
// than the file less its last 8 bytes, is left to arrow-go's file reader, which says
// what it is. Every other footer is walked, one leaving the file's leading magic no
// room as well (step 521). It returns the reader the file reader is to read from.
func measureFooter(ra parquet.ReaderAtSeeker) (parquet.ReaderAtSeeker, error) {
	size, err := ra.Seek(0, io.SeekEnd)
	if err != nil || size <= 8 {
		return ra, nil
	}
	if _, err := ra.Seek(0, io.SeekStart); err != nil {
		return ra, nil
	}
	// The file reader's two reads, the last 8 bytes and then the footer, made here first.
	end := make([]byte, 8)
	if _, err := ra.ReadAt(end, size-8); err != nil && !errors.Is(err, io.EOF) {
		return ra, nil
	}
	if string(end[4:]) != "PAR1" {
		return ra, nil // an encrypted footer's PARE, or no Parquet file
	}
	n := int64(binary.LittleEndian.Uint32(end))
	if n <= 0 || n > size-8 {
		return ra, nil // a footer length the file reader refuses itself
	}
	tail := make([]byte, n+8)
	if _, err := ra.ReadAt(tail[:n], size-8-n); err != nil && !errors.Is(err, io.EOF) {
		return ra, nil
	}
	copy(tail[n:], end)
	depth, err := schemaDepth(tail[:n])
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "corrupt: walking the footer")
	}
	if depth > maxSchemaDepth {
		return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
			"a column is nested deeper than %d levels", maxSchemaDepth)
	}
	return &measured{ReaderAtSeeker: ra, from: size - 8 - n, tail: tail}, nil
}

// schemaDepth is how deep a FileMetaData's schema nests, its leaf counted, the root
// not, walking the whole of it (walk): its second field, a list of SchemaElements,
// each a group's num_children, its fifth field.
func schemaDepth(b []byte) (int, error) {
	r := thriftReader{b: b}
	if err := r.walk(fileMetaData, 0); err != nil {
		return 0, err
	}
	// Each element's depth is the groups it is within, the root's children at 1.
	var open []int32 // each open group's children yet to come, the innermost last
	deepest := 0
	for _, kids := range r.kids {
		deepest = max(deepest, len(open))
		if len(open) > 0 {
			open[len(open)-1]--
		}
		if kids > 0 {
			open = append(open, kids)
		}
		for len(open) > 0 && open[len(open)-1] == 0 {
			open = open[:len(open)-1]
		}
	}
	return deepest, nil
}

// thriftReader reads Thrift's compact protocol as Apache Thrift's TCompactProtocol
// reads it, which a footer and a page header are decoded with: where it reads a
// value, this reads the same value, and where it reads past a struct, this does too.
// A walk that read otherwise would check another value than the one decoded (step
// 520): a count written in a varint of 10 bytes, which binary.Uvarint calls an
// overflow, Thrift reads cut to 32 bits, and so does this.
type thriftReader struct {
	b   []byte
	pos int

	kids []int32 // the schema's elements' num_children, an element's 0 where it says none

	// onList, where it is set, is told where each list header the walk reads lies.
	onList func(start, end int)
}

// walk moves past a struct of kind s, read as parquet.thrift types it: a field
// footerFields names for s, its header giving it the type parquet.thrift gives it, is
// read as that, a list's elements as parquet.thrift types them; any other field is
// skipped as its header says (skip).
func (r *thriftReader) walk(s footerStruct, depth int) error {
	if depth > thriftDepth {
		return errFooter
	}
	return r.fields(depth, func(id int16, typ byte) (bool, error) {
		if s == schemaElement && id == 5 && typ == thriftI32 {
			kids, err := r.i32()
			if err == nil && kids < 0 {
				err = errFooter
			}
			r.kids[len(r.kids)-1] = kids
			return true, err
		}
		var f footerField
		ok := false
		if int(s) < len(footerFields) {
			f, ok = footerFields[s][id]
		}
		switch {
		case ok && f.list && typ == thriftList:
			if f.of == schemaElement {
				r.kids = r.kids[:0] // a second schema is the one a Thrift reader keeps
			}
			_, size, err := r.listHeader()
			for i := 0; i < size && err == nil; i++ {
				switch {
				case f.elem != thriftStruct:
					err = r.skip(f.elem, depth+1, false)
				case f.of == schemaElement:
					r.kids = append(r.kids, 0)
					fallthrough
				default:
					err = r.walk(f.of, depth+1)
				}
			}
			return true, err
		case ok && !f.list && typ == thriftStruct:
			return true, r.walk(f.of, depth+1)
		}
		return false, nil
	})
}

// varint reads a varint as Thrift's readVarint64 does: up to 10 bytes, the bits past
// 64 dropped; a tenth byte that says another follows is an error.
func (r *thriftReader) varint() (uint64, error) {
	var v uint64
	for i := range 10 {
		if r.pos >= len(r.b) {
			return 0, errFooter
		}
		b := r.b[r.pos]
		r.pos++
		v |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return v, nil
		}
	}
	return 0, errFooter
}

// varint32 is a varint cut to its low 32 bits, as Thrift's readVarint32 reads an
// i16's, an i32's, a list's size, a map's and a binary's length.
func (r *thriftReader) varint32() (int32, error) {
	v, err := r.varint()
	return int32(v), err
}

// i32 reads an i32 as Thrift's ReadI32 does: zigzag of varint32's 32 bits.
func (r *thriftReader) i32() (int32, error) {
	n, err := r.varint32()
	return int32(uint32(n)>>1) ^ -(n & 1), err
}

func (r *thriftReader) byte() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, errFooter
	}
	r.pos++
	return r.b[r.pos-1], nil
}

// listHeader reads a list's or a set's header: its elements' type and count. A count
// past the bytes after the header is refused, an element taking one at least.
func (r *thriftReader) listHeader() (byte, int, error) {
	start := r.pos
	h, err := r.byte()
	if err != nil {
		return 0, 0, err
	}
	size := uint64(h >> 4)
	if size == 15 {
		v, err := r.varint32()
		if err != nil || v < 0 {
			return 0, 0, errFooter
		}
		size = uint64(v)
	}
	if size > uint64(len(r.b)-r.pos) {
		return 0, 0, errFooterList
	}
	if r.onList != nil {
		r.onList(start, r.pos)
	}
	return h & 0x0f, int(size), nil
}

// fields walks a struct's fields at depth, handing each to visit, which reads its
// value and says so, or leaves it to be skipped, where skip bounds the depth.
func (r *thriftReader) fields(depth int, visit func(id int16, typ byte) (bool, error)) error {
	var last int16
	for {
		h, err := r.byte()
		if err != nil {
			return err
		}
		if h&0x0f == 0 {
			return nil // STOP, whatever its high bits, as Thrift reads it
		}
		typ, id := h&0x0f, last+int16(h>>4)
		if h>>4 == 0 {
			v, err := r.i32()
			if err != nil {
				return err
			}
			id = int16(v)
		}
		last = id
		read, err := visit(id, typ)
		if err != nil {
			return err
		}
		if !read {
			if err := r.skip(typ, depth+1, true); err != nil {
				return err
			}
		}
	}
}

// skip moves past a value of type typ; a Boolean field's value is in its type.
func (r *thriftReader) skip(typ byte, depth int, field bool) error {
	if depth > thriftDepth {
		return errFooter
	}
	var err error
	switch typ {
	case 1, 2:
		if !field {
			_, err = r.byte()
		}
	case 3:
		_, err = r.byte()
	case 4, 5, 6:
		_, err = r.varint()
	case 7:
		if r.pos += 8; r.pos > len(r.b) {
			err = errFooter
		}
	case 8:
		var n int32
		if n, err = r.varint32(); err == nil && (n < 0 || int(n) > len(r.b)-r.pos) {
			err = errFooter
		}
		r.pos += int(n)
	case 9, 10:
		var et byte
		var size int
		if et, size, err = r.listHeader(); err == nil {
			for range size {
				if err = r.skip(et, depth+1, false); err != nil {
					break
				}
			}
		}
	case 11:
		var n int32
		if n, err = r.varint32(); err == nil && n != 0 {
			if n < 0 {
				return errFooter
			}
			if int(n) > (len(r.b)-r.pos)/2 {
				return errFooterList // an entry is two bytes at least
			}
			var kv byte
			if kv, err = r.byte(); err == nil {
				for range n {
					if err = r.skip(kv>>4, depth+1, false); err != nil {
						break
					}
					if err = r.skip(kv&0x0f, depth+1, false); err != nil {
						break
					}
				}
			}
		}
	case 12:
		err = r.fields(depth, func(int16, byte) (bool, error) { return false, nil })
	case 13: // a UUID
		if r.pos += 16; r.pos > len(r.b) {
			err = errFooter
		}
	default:
		err = errFooter
	}
	return err
}
