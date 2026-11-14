package parquet

import (
	"fmt"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/schema"
)

// A dictionary page's count, checked against its bytes before arrow-go decodes it
// (step 510).
//
// arrow-go's column reader decodes a dictionary page into a slice it makes of the
// count the page's header claims (encoding.NewDictConverter), before it has read a
// value. A count of 2^31−1 in a page of 12 bytes made a slice of 48 GB, a fatal "out
// of memory" no recover catches, from a file of 124 bytes, in every chunk arrow-go
// decodes: a Boolean, INT96 or FIXED_LEN_BYTE_ARRAY column, a nested one, one whose
// footer lists an encoding the reader's own pages do not read. The reader's own
// pages check the count (readDictionary), and those chunks cannot reach it.
//
// arrow-go reads a chunk whole, with one read at its first byte, through the file's
// ReaderAt, which is measured (footer.go): so a read there, once it returns, walks
// the chunk's page headers, and a dictionary page whose uncompressed bytes cannot
// hold its count fails the read. A PLAIN value takes at least valueBits, and a page
// header that does not parse is left to arrow-go, which says what it is.

// watchChunks has m count the dictionary pages of each column chunk md places.
func (m *measured) watchChunks(md *metadata.FileMetaData) {
	m.chunks = make(map[int64]int)
	for g := range md.NumRowGroups() {
		rg := md.RowGroup(g)
		for c := range rg.NumColumns() {
			cc, err := rg.ColumnChunk(c)
			if err != nil || c >= md.Schema.NumColumns() {
				continue
			}
			start := cc.DataPageOffset()
			if d := cc.DictionaryPageOffset(); cc.HasDictionaryPage() && d > 0 && start > d {
				start = d
			}
			m.chunks[start] = valueBits(md.Schema.Column(c))
		}
	}
}

// valueBits is the fewest bits a PLAIN value of column c takes: a BYTE_ARRAY's
// 4-byte length.
func valueBits(c *schema.Column) int {
	switch c.PhysicalType() {
	case parquet.Types.Int32, parquet.Types.Float, parquet.Types.ByteArray:
		return 32
	case parquet.Types.Int64, parquet.Types.Double:
		return 64
	case parquet.Types.Int96:
		return 96
	case parquet.Types.FixedLenByteArray:
		return 8 * max(c.TypeLength(), 1)
	}
	return 1 // a Boolean
}

// countDictionaries walks b, a column chunk's bytes, a page at a time, and refuses
// a dictionary page whose uncompressed bytes cannot hold the values its header
// counts, each of bits bits.
func countDictionaries(b []byte, bits int) error {
	for pos := 0; pos < len(b); {
		h, ok := readPageHeader(b[pos:])
		if !ok {
			return nil
		}
		if h.typ == pageDictionary && int64(h.values)*int64(bits) > int64(h.uncompressed)*8 {
			return fmt.Errorf("corrupt: a dictionary page of %d bytes says %d values", h.uncompressed, h.values)
		}
		if h.compressed < 0 || h.compressed > len(b)-pos-h.size {
			return nil
		}
		pos += h.size + h.compressed
	}
	return nil
}

// pageDictionary is a dictionary page's PageType (format/parquet.thrift).
const pageDictionary = 2

// pageHeader is what countDictionaries reads of a PageHeader: its type, its sizes,
// a dictionary page's count, and its own length.
type pageHeader struct {
	typ, uncompressed, compressed, values int
	size                                  int
}

// readPageHeader reads the PageHeader at the start of b: field 1 its type, 2 and 3
// its sizes, 7 a dictionary page's header, whose field 1 is its count.
func readPageHeader(b []byte) (h pageHeader, ok bool) {
	r := thriftReader{b: b}
	i32 := func() (int, error) {
		v, err := r.varint()
		return int(int32(int64(v>>1) ^ -int64(v&1))), err
	}
	var err error
	h.typ = -1
	err = r.fields(0, func(id int16, typ byte) (bool, error) {
		if typ == 12 && id == 7 {
			return true, r.fields(1, func(id int16, typ byte) (bool, error) {
				if id != 1 || typ != 5 {
					return false, nil
				}
				var err error
				h.values, err = i32()
				return true, err
			})
		}
		if typ != 5 {
			return false, nil
		}
		var err error
		switch id {
		case 1:
			h.typ, err = i32()
		case 2:
			h.uncompressed, err = i32()
		case 3:
			h.compressed, err = i32()
		default:
			return false, nil
		}
		return true, err
	})
	h.size = r.pos
	return h, err == nil && h.typ >= 0
}
