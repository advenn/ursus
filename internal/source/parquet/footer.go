package parquet

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/apache/arrow-go/v18/parquet"

	"github.com/advenn/ursus/internal/uerr"
)

// A footer's schema, measured before arrow-go reads it (step 423).
//
// A Parquet schema is a flat list of elements in its footer, each group saying how
// many children follow it, and arrow-go's schema.FromParquet builds the tree from it
// by recursing once a level, as ursus's own naming of a column's type did. A crafted
// footer of a few megabytes, groups each holding one, overflowed the goroutine's
// stack in either, a fatal error no recover catches, and ended the process. So the
// footer is walked here first, with a stack of its own, and a schema nested past
// maxSchemaDepth is refused before anything recurses on it.

// maxSchemaDepth is the deepest a column's schema may nest, its leaf counted: the
// JSON readers' limit (step 412).
const maxSchemaDepth = 1_000

// thriftDepth bounds how deeply one value of a footer may nest in structs and lists:
// a real footer's do some ten deep, and a value past it is corrupt.
const thriftDepth = 64

var errFooter = errors.New("the footer is not Thrift's compact protocol")

// measured is ra with its end, the footer and the 8 bytes after it, read once into
// tail: arrow-go's two reads there are answered from it, so measuring the footer reads
// no byte and makes no read more than arrow-go did, each a ranged GET where the file
// is in an object store.
type measured struct {
	parquet.ReaderAtSeeker
	from int64 // where tail begins in the file
	tail []byte

	// chunks holds where each column chunk begins, and the fewest bits a value of
	// its column takes, once watchChunks has read them from the footer: a read from
	// there is a chunk's, and its dictionary pages are counted (dictpages.go).
	chunks map[int64]int
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
	if bits, ok := m.chunks[off]; ok && n > 0 {
		if e := countDictionaries(p[:n], bits); e != nil {
			return n, e
		}
	}
	return n, err
}

// measureFooter reads ra's footer and refuses one whose schema nests past
// maxSchemaDepth, or that cannot be walked. A file too short or not ending in a plain
// footer is left to arrow-go, which says what it is. It returns the reader arrow-go
// is to read from.
func measureFooter(ra parquet.ReaderAtSeeker) (parquet.ReaderAtSeeker, error) {
	size, err := ra.Seek(0, io.SeekEnd)
	if err != nil || size < 12 {
		return ra, nil
	}
	if _, err := ra.Seek(0, io.SeekStart); err != nil {
		return ra, nil
	}
	// arrow-go's two reads, the last 8 bytes and then the footer, made here first.
	end := make([]byte, 8)
	if _, err := ra.ReadAt(end, size-8); err != nil && !errors.Is(err, io.EOF) {
		return ra, nil
	}
	if string(end[4:]) != "PAR1" {
		return ra, nil // an encrypted footer's PARE, or no Parquet file
	}
	n := int64(binary.LittleEndian.Uint32(end))
	if n <= 0 || n > size-12 {
		return ra, nil
	}
	tail := make([]byte, n+8)
	if _, err := ra.ReadAt(tail[:n], size-8-n); err != nil && !errors.Is(err, io.EOF) {
		return ra, nil
	}
	copy(tail[n:], end)
	depth, err := schemaDepth(tail[:n])
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "corrupt: reading the footer's schema")
	}
	if depth > maxSchemaDepth {
		return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
			"a column is nested deeper than %d levels", maxSchemaDepth)
	}
	return &measured{ReaderAtSeeker: ra, from: size - 8 - n, tail: tail}, nil
}

// schemaDepth is how deep a FileMetaData's schema nests, its leaf counted, the root
// not: its second field, a list of SchemaElements, each a group's num_children, its
// fifth field.
func schemaDepth(b []byte) (int, error) {
	r := thriftReader{b: b}
	children := []int32(nil)
	err := r.fields(0, func(id int16, typ byte) (bool, error) {
		if id != 2 || typ != 9 {
			return false, nil
		}
		et, size, err := r.listHeader()
		if err != nil || et != 12 {
			return true, errFooter
		}
		for range size {
			var kids int32
			err := r.fields(1, func(id int16, typ byte) (bool, error) {
				if id != 5 || typ != 5 {
					return false, nil
				}
				var err error
				kids, err = r.i32()
				return true, err
			})
			if err != nil {
				return true, err
			}
			if kids < 0 {
				return true, errFooter
			}
			children = append(children, kids)
		}
		return true, nil
	})
	if err != nil {
		return 0, err
	}
	// Each element's depth is the groups it is within, the root's children at 1.
	var open []int32 // each open group's children yet to come, the innermost last
	deepest := 0
	for _, kids := range children {
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
// reads it, which arrow-go reads a footer and a page header with: where it reads a
// value, this reads the same value, and where it reads past a struct, this does too.
// A walk that read otherwise checked a value arrow-go does not read (step 520, F11 of
// its record): a dictionary page's count written in a varint of 10 bytes, which
// binary.Uvarint calls an overflow and Thrift reads, cut to 32 bits, as 896,532,479,
// was not counted, and arrow-go made a slice of it.
type thriftReader struct {
	b   []byte
	pos int
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

func (r *thriftReader) listHeader() (byte, int, error) {
	h, err := r.byte()
	if err != nil {
		return 0, 0, err
	}
	size := int(h >> 4)
	if size == 15 {
		v, err := r.varint32()
		if err != nil || v < 0 || int(v) > len(r.b) {
			return 0, 0, errFooter // more elements than bytes to hold them
		}
		size = int(v)
	}
	return h & 0x0f, size, nil
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
			if n < 0 || int(n) > len(r.b) {
				return errFooter
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
