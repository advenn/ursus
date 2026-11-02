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
}

func (m *measured) ReadAt(p []byte, off int64) (int, error) {
	if off >= m.from && off-m.from <= int64(len(m.tail)) {
		n := copy(p, m.tail[off-m.from:])
		if n < len(p) {
			return n, io.EOF
		}
		return n, nil
	}
	return m.ReaderAtSeeker.ReadAt(p, off)
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
				v, err := r.varint()
				kids = int32(int64(v>>1) ^ -int64(v&1))
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

// thriftReader reads Thrift's compact protocol.
type thriftReader struct {
	b   []byte
	pos int
}

func (r *thriftReader) varint() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		return 0, errFooter
	}
	r.pos += n
	return v, nil
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
		v, err := r.varint()
		if err != nil || v > uint64(len(r.b)) {
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
		if h == 0 {
			return nil
		}
		typ, id := h&0x0f, last+int16(h>>4)
		if h>>4 == 0 {
			v, err := r.varint()
			if err != nil {
				return err
			}
			id = int16(int64(v>>1) ^ -int64(v&1))
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
		var n uint64
		if n, err = r.varint(); err == nil && n > uint64(len(r.b)-r.pos) {
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
		var n uint64
		if n, err = r.varint(); err == nil && n > 0 {
			if n > uint64(len(r.b)) {
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
