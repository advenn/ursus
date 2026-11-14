package parquet

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"sync"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"
)

// A data page's dictionary indices, checked against its dictionary before arrow-go
// decodes them (step 517, F9 of step 510's record).
//
// arrow-go's column reader looks a dictionary index up through
// encoding.(*dictConverter[T]).ensure, which makes a slice of index+1 values before
// anything checks the index against the dictionary: IsValidSingle, IsValid and Fill
// each call it first. An RLE run saying index 2^30 in a dictionary of two INT64
// values made a slice of 8 GiB, from a file of 128 bytes, and a
// FIXED_LEN_BYTE_ARRAY's or a BYTE_ARRAY's slice headers three times that, a fatal
// "out of memory" no recover catches. It reached every chunk arrow-go decodes: a
// nested column or a struct's field, INT96 and FIXED_LEN_BYTE_ARRAY, a chunk whose
// footer lists an encoding the reader's own pages do not read. The own pages check
// each index first (pageChunk.indices), and those chunks do not come here.
//
// So such a chunk is read through checkedPages, a page reader around arrow-go's own.
// Each page arrow-go's column reader takes from it has been decompressed already, by
// arrow-go, into the buffer arrow-go decodes, and a dictionary-encoded data page's
// indices are checked there, before the column reader sees the page: an index at or
// past the dictionary's count fails the read as corrupt. The other way in, step
// 510's measured reads, would have meant decompressing each such page a second time,
// with a page header and level parser of ursus's own beside arrow-go's, which could
// disagree with it; here the page is the one arrow-go reads, as arrow-go parsed it.
//
// Only the indices arrow-go would read are checked, so no file it reads is refused:
// a page's first n, n its values that are not null, which its definition levels
// count; past them, a bit-packed run's padding can hold anything. A bit width wider
// than the dictionary needs is legal and checked by value. A PLAIN page after the
// dictionary's, where a writer fell back, is not checked. Each run is read as
// arrow-go's RleDecoder and bit reader read it, so an index arrow-go reaches is one
// this reaches; past a header RleDecoder refuses, where arrow-go fails the page, this
// reads on, which checks more and never less.
//
// A page is refused by the reader's Next answering false: arrow-go has not taken it
// in, so nothing decodes it, and its column reader's Err gives the reason, which
// arrow-go's ReadBatch drops at a batch's first page and batchErr picks up.
//
// One pass over the page's definition levels and its indices, for a
// dictionary-encoded data page only. The reader's state is one column chunk's, read
// on one goroutine: a parallel scan's workers each make their own, and nothing is
// shared but the file's buffer pool, which is arrow-go's own sync.Pool.

// checkedColumn is the column reader arrow-go's RowGroupReader.Column makes for
// column ci, its pages read through checkedPages: the same reader, with the same
// allocator and buffer pool, made with the constructor RowGroupReader.Column wraps.
func checkedColumn(rg *file.RowGroupReader, ci int, desc *schema.Column, mem memory.Allocator,
	pool *sync.Pool) (file.ColumnChunkReader, error) {
	pr, err := rg.GetColumnPageReader(ci)
	if err != nil {
		return nil, err
	}
	pages := &checkedPages{PageReader: pr, maxDef: desc.MaxDefinitionLevel(), maxRep: desc.MaxRepetitionLevel(),
		name: desc.Path()}
	// Deprecated as "unsafe to use outside of this package"; it is what
	// RowGroupReader.Column runs, and file.Reader.BufferPool is exported for a reader
	// that makes its own column readers. arrow-go is pinned (go.mod), so its removal
	// would show as a build that fails.
	return file.NewColumnReader(desc, pages, mem, pool), nil
}

// checkedPages is a column chunk's page reader that refuses a dictionary-encoded data
// page holding an index past its dictionary.
type checkedPages struct {
	file.PageReader
	maxDef, maxRep int16
	name           string

	dictN int64 // the dictionary's values, once dict
	dict  bool  // the dictionary page has been read
	err   error
}

// Next refuses a page by answering false, with the reason in Err. arrow-go then
// reads no more of the chunk, and its count of the last page's values stands: a
// refusal from anywhere later, once arrow-go has taken the page in, leaves it
// counting the page's values and decoding them with what it had (batchErr).
func (c *checkedPages) Next() bool {
	if c.err != nil || !c.PageReader.Next() {
		return false
	}
	switch p := c.PageReader.Page().(type) {
	case *file.DictionaryPage:
		c.dictN, c.dict = int64(p.NumValues()), true
	case *file.DataPageV1, *file.DataPageV2:
		if !dictEncoded(parquet.Encoding(p.Encoding())) {
			break
		}
		if !c.dict {
			// No dictionary page came first: arrow-go reads one out of turn, through
			// GetDictionaryPage, before it decodes this page, so it is read here first.
			// Where there is none, arrow-go refuses the page itself.
			d, err := c.PageReader.GetDictionaryPage()
			if err != nil || d == nil {
				break
			}
			c.dictN, c.dict = int64(d.NumValues()), true
		}
		if c.err = c.check(p); c.err != nil {
			return false
		}
	}
	return true
}

func (c *checkedPages) Err() error {
	if c.err != nil {
		return c.err
	}
	return c.PageReader.Err()
}

func (c *checkedPages) Reset(r parquet.BufferedReader, nrows int64, codec compress.Compression, ctx *file.CryptoContext) {
	c.dictN, c.dict, c.err = 0, false, nil
	c.PageReader.Reset(r, nrows, codec, ctx)
}

// batchErr is a ReadBatch's error, or, where the batch read nothing and answered
// none, the error the chunk's reader stopped on. arrow-go's ReadBatch drops an error
// that ends the chunk before the batch reads a level, a page checkedPages refuses or
// one that does not decompress, and answers that it read nothing
// (columnChunkReader.readBatch); ColumnChunkReader.Err holds it, as that interface
// says. Read again, a reader whose page failed once arrow-go had counted its values
// would decode them with what it had: the read ends here instead.
func batchErr(cr interface{ Err() error }, total int64, err error) error {
	if err == nil && total == 0 {
		return cr.Err()
	}
	return err
}

// dictEncoded is whether a data page's values are dictionary indices.
func dictEncoded(e parquet.Encoding) bool {
	switch e {
	case parquet.Encodings.PlainDict, parquet.Encodings.RLEDict:
		return true
	}
	return false
}

// check refuses data page p if an index arrow-go would read of it is at or past the
// dictionary's count. A page arrow-go refuses before it decodes a value, levels it
// cannot read, is left to it.
func (c *checkedPages) check(p file.Page) error {
	data, n := p.Data(), int(p.NumValues())
	var defs []byte // the definition levels' runs, to their page's end, as arrow-go reads them
	packedDefs := false
	switch q := p.(type) {
	case *file.DataPageV1:
		// initLevelDecodersV1: the repetition levels, then the definition levels,
		// each where its column has them.
		if c.maxRep > 0 {
			k, _, _, ok := levelsV1(parquet.Encoding(q.RepetitionLevelEncoding()), c.maxRep, n, data)
			if !ok {
				return nil
			}
			data = data[k:]
		}
		if c.maxDef > 0 {
			k, runs, packed, ok := levelsV1(parquet.Encoding(q.DefinitionLevelEncoding()), c.maxDef, n, data)
			if !ok {
				return nil
			}
			defs, packedDefs, data = runs, packed, data[k:]
		}
	case *file.DataPageV2:
		// initLevelDecodersV2: the repetition levels' bytes, whatever the column,
		// then the definition levels'; their decoder reads on to the page's end.
		rl, dl := int(q.RepetitionLevelByteLen()), int(q.DefinitionLevelByteLen())
		if rl < 0 || dl < 0 || rl+dl > len(data) {
			return nil
		}
		defs, data = data[rl:], data[rl+dl:]
	default:
		return nil
	}
	if len(data) == 0 {
		return nil // no indices: arrow-go reads none
	}
	width := int(data[0])
	if width >= 64 {
		return nil // arrow-go refuses the width
	}
	values := n
	if c.maxDef > 0 {
		values = presentValues(defs, packedDefs, bits.Len16(uint16(c.maxDef)), c.maxDef, n)
	}
	idx, ok := pastIndex(data[1:], width, values, c.dictN)
	if !ok {
		return nil
	}
	return fmt.Errorf("corrupt: column %s: a data page's dictionary index %d is past its dictionary of %d values",
		c.name, idx, c.dictN)
}

// levelsV1 finds a V1 page's levels of one kind at the start of data, n of them, as
// arrow-go's LevelDecoder.SetData does: their bytes in the page, and the runs its
// decoder reads, which go on past those bytes. ok is false where arrow-go refuses
// the page.
func levelsV1(enc parquet.Encoding, maxLvl int16, n int, data []byte) (size int, runs []byte, packed, ok bool) {
	switch enc {
	case parquet.Encodings.RLE:
		if len(data) < 4 {
			return 0, nil, false, false
		}
		k := int32(binary.LittleEndian.Uint32(data))
		if k < 0 || int(k) > len(data)-4 {
			return 0, nil, false, false
		}
		return 4 + int(k), data[4:], false, true
	case parquet.Encodings.BitPacked:
		w := bits.Len16(uint16(maxLvl))
		k := (n*w + 7) / 8
		if k > len(data) {
			return 0, nil, false, false
		}
		return k, data, true, true
	}
	return 0, nil, false, false
}

// presentValues bounds the values of a page of n levels that are not null, those whose
// definition level is maxDef, from its definition levels: the runs of the hybrid
// encoding, or bit-packed values where packed, the deprecated BIT_PACKED as arrow-go
// reads it. A level it cannot read is counted present: arrow-go stops at the first
// run it cannot read, so a level past there yields it no value, and counting one
// costs only a check of an index arrow-go would not have read.
func presentValues(runs []byte, packed bool, width int, maxDef int16, n int) int {
	if packed {
		hits, read := packedCount(runs, width, n, maxDef)
		return hits + n - read
	}
	r := hybridRuns{b: runs}
	hits, read := 0, 0
	for read < n {
		h, ok := r.header()
		if !ok {
			break
		}
		count := uint32(h >> 1)
		if h&1 == 0 {
			if count == 0 || count > math.MaxInt32 {
				break
			}
			v, ok := r.value(width)
			if !ok {
				break
			}
			k := min(int(count), n-read)
			if int16(v) == maxDef {
				hits += k
			}
			read += k
			continue
		}
		if count == 0 || count > math.MaxInt32/8 {
			break
		}
		k := min(int(count)*8, n-read)
		got, done := packedCount(r.rest(), width, k, maxDef)
		hits, read = hits+got, read+done
		if done < k || !r.skip(int(count)*width) {
			break
		}
	}
	return hits + n - read
}

// pastIndex reads the dictionary indices of a page, runs of the hybrid encoding of
// width bits, as arrow-go's dictionary decoder takes them for the page's first n
// values, and returns the first at or past dictN.
//
// Past a header arrow-go's RleDecoder.Next refuses, a zero count or one past what an
// int32 holds, arrow-go fails the page on the values it came short of; this reads on
// from where RleDecoder would, which checks more and never less. An RLE run's value
// is the whole bytes it is stored in, unmasked, as arrow-go takes it; of width 0 it
// is 0, where arrow-go takes the byte after the header, which can grow its
// dictionary by at most 256 values. A bit-packed run's values past the page's end are
// 0, as arrow-go's bit reader gives them, and a bit-packed run wider than 32 bits is
// an error there.
func pastIndex(b []byte, width, n int, dictN int64) (idx uint64, past bool) {
	r := hybridRuns{b: b}
	for at := 0; at < n; {
		h, ok := r.header()
		if !ok {
			if r.pos >= len(r.b) {
				return 0, false
			}
			continue // an overlong header: RleDecoder's next read starts after what it took
		}
		count := uint32(h >> 1)
		if h&1 == 0 {
			if count == 0 || count > math.MaxInt32 {
				continue
			}
			v, ok := r.value(width)
			if !ok {
				continue // arrow-go does not take the bytes of a value that is not there
			}
			if v >= uint64(dictN) {
				return v, true
			}
			at += int(count)
			continue
		}
		if count == 0 || count > math.MaxInt32/8 {
			continue
		}
		if width > 32 {
			return 0, false
		}
		k := min(int(count)*8, n-at)
		if m, ok := packedMax(r.rest(), width, k); ok && m >= uint64(dictN) {
			return m, true
		}
		at += k
		if !r.skip(int(count) * width) {
			return 0, false
		}
	}
	return 0, false
}

// hybridRuns walks the runs of Parquet's RLE / bit-packed hybrid encoding as
// arrow-go's RleDecoder and BitReader read them.
type hybridRuns struct {
	b   []byte
	pos int
}

// header reads a run's header, a ULEB128 of at most ten bytes, as
// encoding/binary.ReadUvarint reads one a byte at a time: one that does not end, or
// overflows, is not a header, and the bytes it read are taken.
func (r *hybridRuns) header() (uint64, bool) {
	var x uint64
	var s uint
	for i := range binary.MaxVarintLen64 {
		if r.pos >= len(r.b) {
			return 0, false
		}
		c := r.b[r.pos]
		r.pos++
		if c < 0x80 {
			if i == binary.MaxVarintLen64-1 && c > 1 {
				return 0, false
			}
			return x | uint64(c)<<s, true
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, false
}

// value reads an RLE run's value: the (width+7)/8 little-endian bytes after its
// header, taken only where they are all there.
func (r *hybridRuns) value(width int) (uint64, bool) {
	k := (width + 7) / 8
	if k > len(r.b)-r.pos {
		return 0, false
	}
	var v uint64
	for i := range k {
		v |= uint64(r.b[r.pos+i]) << (8 * i)
	}
	r.pos += k
	return v, true
}

// rest is the bytes from the current run's first value.
func (r *hybridRuns) rest() []byte { return r.b[r.pos:] }

// skip moves past a bit-packed run of k bytes, reporting false where the page ends
// inside it, and no run follows.
func (r *hybridRuns) skip(k int) bool {
	if k > len(r.b)-r.pos {
		r.pos = len(r.b)
		return false
	}
	r.pos += k
	return true
}

// packedMax is the largest of the first n values bit-packed width bits each, from
// 1 to 32, least significant bit first, in b, a value past b's end 0; ok is false
// for n of 0.
func packedMax(b []byte, width, n int) (m uint64, ok bool) {
	if n <= 0 {
		return 0, false
	}
	if width == 0 {
		return 0, true
	}
	mask := uint64(1)<<width - 1
	per := 57 / width // values a load of eight bytes holds whole, past a shift of up to 7
	bit, i := 0, 0
	for i < n && bit>>3+8 <= len(b) {
		word := binary.LittleEndian.Uint64(b[bit>>3:]) >> (bit & 7)
		k := min(per, n-i)
		for range k {
			m = max(m, word&mask)
			word >>= width
		}
		bit += k * width
		i += k
	}
	for ; i < n && bit>>3 < len(b); i++ {
		var word uint64
		for j, c := range b[bit>>3 : min(bit>>3+8, len(b))] {
			word |= uint64(c) << (8 * j)
		}
		m = max(m, word>>(bit&7)&mask)
		bit += width
	}
	return m, true
}

// packedCount counts the values equal to want among the first n bit-packed width
// bits each, from 1 to 16, least significant bit first, in b, and how many of the n
// lie whole in b; it stops at the first that does not.
func packedCount(b []byte, width, n int, want int16) (hits, read int) {
	if width == 0 {
		if want == 0 {
			return n, n
		}
		return 0, n
	}
	n = min(n, len(b)*8/width)
	mask := uint64(1)<<width - 1
	per := 57 / width
	bit := 0
	for read < n {
		var word uint64
		if bit>>3+8 <= len(b) {
			word = binary.LittleEndian.Uint64(b[bit>>3:])
		} else {
			for j, c := range b[bit>>3:] {
				word |= uint64(c) << (8 * j)
			}
		}
		word >>= bit & 7
		k := min(per, n-read)
		for range k {
			if int16(word&mask) == want {
				hits++
			}
			word >>= width
		}
		bit += k * width
		read += k
	}
	return hits, read
}
