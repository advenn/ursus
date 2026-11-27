package parquet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"sync"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/schema"
)

// A data page's dictionary indices, checked against its dictionary before they are
// decoded (step 517).
//
// A dictionary index is a claim: an RLE run of a few bytes can say any index, in a
// dictionary of two values. The reader's own pages check each index first
// (pageChunk.indices); the chunks arrow-go's column reader decodes, a nested column
// or a struct's field, INT96 and FIXED_LEN_BYTE_ARRAY, a chunk whose footer lists an
// encoding the own pages do not read, are checked here, so an index is checked
// against its dictionary's count whichever reader its chunk takes.
//
// Such a chunk is read through checkedPages, a page reader around arrow-go's own.
// Each page the column reader takes from it has been decompressed already, into the
// buffer the column reader decodes, and a dictionary-encoded data page's indices are
// checked there, before the column reader sees the page: an index at or past the
// dictionary's count fails the read as corrupt. The other way in, step 510's
// measured reads, would have meant decompressing each such page a second time, with
// a page header and level parser of ursus's own beside arrow-go's, which could
// disagree with it; here the page is the one the column reader reads, as it parsed it.
//
// Only the indices the column reader would read are checked, so no file it reads is
// refused: a page's first n, n its values that are not null, which its definition
// levels count; past them, a bit-packed run's padding can hold anything. A bit width
// wider than the dictionary needs is legal and checked by value. A PLAIN page after
// the dictionary's, where a writer fell back, is not checked. Each run is read as the
// hybrid encoding's decoder reads it, so an index it reaches is one this reaches;
// past a header the decoder refuses, this reads on, which checks more and never less.
//
// A page is refused by the reader's Next answering false: the column reader has not
// taken it in, so nothing decodes it, and its Err gives the reason, which batchErr
// picks up. The error names the column, the index and the dictionary's count (step
// 538), and the same pass checks a page of 0-bit indices (zeroWidthMisread).
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
	boundHeaders(pr, rg.MetaData(), ci)
	pages := &checkedPages{PageReader: pr, maxDef: desc.MaxDefinitionLevel(), maxRep: desc.MaxRepetitionLevel(),
		name: desc.Path(), phys: desc.PhysicalType(), chunkValues: math.MaxInt64}
	if cc, err := rg.MetaData().ColumnChunk(ci); err == nil {
		pages.chunkValues = cc.NumValues()
	}
	// Deprecated as "unsafe to use outside of this package"; it is what
	// RowGroupReader.Column runs, and file.Reader.BufferPool is exported for a reader
	// that makes its own column readers. arrow-go is pinned (go.mod), so its removal
	// would show as a build that fails.
	return file.NewColumnReader(desc, pages, mem, pool), nil
}

// boundHeaders lowers the largest page header pr reads to its chunk's bytes, and to
// no less than the page reader's first look at a header, 16 KiB (step 521). The page
// reader reads a header by looking at 16 KiB of the chunk and, where the header does
// not parse, at twice as much, up to 4 MiB, each look a buffer of its size. A header
// lies inside its chunk, so a look past the chunk's bytes finds nothing more, and a
// header that does not parse is refused at the first look that holds them.
func boundHeaders(pr file.PageReader, rg *metadata.RowGroupMetaData, ci int) {
	const firstLook, mostLook = 16 << 10, 4 << 20 // the page reader's defaultPageHeaderSize and defaultMaxPageHeaderSize
	cc, err := rg.ColumnChunk(ci)
	if err != nil {
		return
	}
	pr.SetMaxPageHeaderSize(int(min(max(cc.TotalCompressedSize(), firstLook), mostLook)))
}

// checkedPages is a column chunk's page reader that refuses a dictionary-encoded data
// page holding an index past its dictionary, and a delta-encoded data page the format
// does not allow (deltapage.go).
type checkedPages struct {
	file.PageReader
	maxDef, maxRep int16
	name           string
	phys           parquet.Type // the column's physical type, which picks the column reader's decoder
	chunkValues    int64        // the column chunk's values, as its footer gives them

	dictN int64 // the dictionary's values, once dict
	dict  bool  // the dictionary page has been read
	err   error
}

// Next refuses a page by answering false, with the reason in Err, before the column
// reader takes the page in: it then reads no more of the chunk, and none of the
// page's values is counted or decoded (batchErr).
func (c *checkedPages) Next() bool {
	if c.err != nil || !c.PageReader.Next() {
		return false
	}
	switch p := c.PageReader.Page().(type) {
	case *file.DictionaryPage:
		c.dictN, c.dict = int64(p.NumValues()), true
	case *file.DataPageV1, *file.DataPageV2:
		e := parquet.Encoding(p.Encoding())
		if deltaEncoded(e, c.phys) {
			if c.err = c.checkDelta(p, e); c.err != nil {
				return false
			}
			break
		}
		if !dictEncoded(e) {
			break
		}
		if !c.dict {
			// No dictionary page came first: the column reader reads one out of turn,
			// through GetDictionaryPage, before it decodes this page, so it is read here
			// first. Where there is none, the column reader refuses the page itself.
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
// none, the error the chunk's reader stopped on, which ColumnChunkReader.Err holds,
// as that interface says: a page checkedPages refuses, or one that does not
// decompress, ends the chunk before the batch reads a level. The read ends at that
// error, and the chunk's reader is not read again.
func batchErr(cr interface{ Err() error }, total int64, err error) error {
	if err == nil && total == 0 {
		err = cr.Err()
	}
	return truncated(err)
}

// truncated is a page reader's error as a scan may return it, io.EOF made
// io.ErrUnexpectedEOF (step 521). A page whose header is whole and none of whose
// bytes is in its chunk is cut short, and io.ReadFull answers io.EOF for it, no byte
// read; a scan answers io.EOF at its end, which the engine takes an error wrapping
// for. Made io.ErrUnexpectedEOF, the error fails the read as cut short, where io.EOF
// would end it at the rows before the page.
func truncated(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
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

// pageLevels is a data page as the column reader takes it apart: its values' bytes,
// after its levels, and its definition levels' runs, to the page's end.
type pageLevels struct {
	data   []byte // the values' bytes
	n      int    // the page's levels, its NumValues
	defs   []byte // the definition levels' runs, to their page's end, as the column reader reads them
	packed bool   // defs are the deprecated BIT_PACKED
}

// levels takes data page p apart as the column reader does. ok is false where the
// column reader refuses the page's levels before it decodes a value; such a page is
// left to it.
func (c *checkedPages) levels(p file.Page) (l pageLevels, ok bool) {
	l.data, l.n = p.Data(), int(p.NumValues())
	switch q := p.(type) {
	case *file.DataPageV1:
		// initLevelDecodersV1: the repetition levels, then the definition levels,
		// each where its column has them.
		if c.maxRep > 0 {
			k, _, _, ok := levelsV1(parquet.Encoding(q.RepetitionLevelEncoding()), c.maxRep, l.n, l.data)
			if !ok {
				return l, false
			}
			l.data = l.data[k:]
		}
		if c.maxDef > 0 {
			k, runs, packed, ok := levelsV1(parquet.Encoding(q.DefinitionLevelEncoding()), c.maxDef, l.n, l.data)
			if !ok {
				return l, false
			}
			l.defs, l.packed, l.data = runs, packed, l.data[k:]
		}
	case *file.DataPageV2:
		// initLevelDecodersV2: the repetition levels' bytes, whatever the column,
		// then the definition levels'; their decoder reads on to the page's end.
		rl, dl := int(q.RepetitionLevelByteLen()), int(q.DefinitionLevelByteLen())
		if rl < 0 || dl < 0 || rl+dl > len(l.data) {
			return l, false
		}
		l.defs, l.data = l.data[rl:], l.data[rl+dl:]
	default:
		return l, false
	}
	return l, true
}

// present bounds the values of page l that are not null, the values the column reader
// decodes of it (presentValues): its levels, where its column has no definition level.
func (c *checkedPages) present(l pageLevels) int {
	if c.maxDef == 0 {
		return l.n
	}
	return presentValues(l.defs, l.packed, bits.Len16(uint16(c.maxDef)), c.maxDef, l.n)
}

// check refuses data page p if an index the column reader would read of it is at or
// past the dictionary's count. A page the column reader refuses before it decodes a
// value, levels it cannot read, is left to it.
func (c *checkedPages) check(p file.Page) error {
	l, ok := c.levels(p)
	if !ok {
		return nil
	}
	data := l.data
	if len(data) == 0 {
		return nil // no indices: the column reader reads none
	}
	width := int(data[0])
	if width >= 64 {
		return nil // the column reader refuses the width
	}
	values := c.present(l)
	if width == 0 {
		if b, ok := zeroWidthMisread(data[1:], values); ok {
			return fmt.Errorf("corrupt: column %s: a data page's dictionary indices are 0 bits wide, each 0, "+
				"but a run of them is followed by a byte, %d, that it does not hold", c.name, b)
		}
		return nil
	}
	idx, ok := pastIndex(data[1:], width, values, c.dictN)
	if !ok {
		return nil
	}
	return fmt.Errorf("corrupt: column %s: a data page's dictionary index %d is past its dictionary of %d values",
		c.name, idx, c.dictN)
}

// levelsV1 finds a V1 page's levels of one kind at the start of data, n of them, as
// the column reader's LevelDecoder.SetData does: their bytes in the page, and the
// runs its decoder reads, which go on past those bytes. ok is false where the column
// reader refuses the page.
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
// encoding, or bit-packed values where packed, the deprecated BIT_PACKED as the
// column reader reads it. A level it cannot read is counted present: the column
// reader stops at the first run it cannot read, so a level past there yields it no
// value, and counting one costs only a check of an index it would not have read.
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

// zeroWidthMisread reports a run of a page's dictionary indices of 0 bits, among
// those of the page's first n values, followed by a byte that is not 0, and that
// byte (step 521). An index of 0 bits is 0, as pyarrow 25.0.1, DuckDB 1.5.5 and the
// reader's own pages read it, and an RLE run of them stores its value in no byte. A
// byte after the run's header that is not 0, a stray byte or the header of a run
// after it, is one a decoder could take for the run's value, every value of the run
// then another index than 0; the page is refused rather than read two ways. Runs are
// walked as pastIndex walks them; a bit-packed run of 0 bits holds no byte and reads
// as 0s.
func zeroWidthMisread(b []byte, n int) (byte, bool) {
	r := hybridRuns{b: b}
	for at := 0; at < n; {
		h, ok := r.header()
		if !ok {
			if r.pos >= len(r.b) {
				return 0, false
			}
			continue
		}
		count := uint32(h >> 1)
		if h&1 == 1 {
			if count == 0 || count > math.MaxInt32/8 {
				continue
			}
			at += min(int(count)*8, n-at)
			continue
		}
		if count == 0 || count > math.MaxInt32 {
			continue
		}
		if r.pos < len(r.b) && r.b[r.pos] != 0 {
			return r.b[r.pos], true
		}
		at += int(count)
	}
	return 0, false
}

// pastIndex reads the dictionary indices of a page, runs of the hybrid encoding of
// width bits, as the column reader's dictionary decoder takes them for the page's
// first n values, and returns the first at or past dictN.
//
// Past a header the decoder refuses, a zero count or one past what an int32 holds,
// this reads on from where the decoder would, which checks more and never less. An
// RLE run's value is taken as the whole bytes it is stored in, unmasked; width 0 is
// zeroWidthMisread's. A bit-packed run's values past the page's end are taken as 0,
// and a bit-packed run wider than 32 bits is refused by the decoder.
func pastIndex(b []byte, width, n int, dictN int64) (idx uint64, past bool) {
	r := hybridRuns{b: b}
	for at := 0; at < n; {
		h, ok := r.header()
		if !ok {
			if r.pos >= len(r.b) {
				return 0, false
			}
			continue // an overlong header: the decoder's next read starts after what it took
		}
		count := uint32(h >> 1)
		if h&1 == 0 {
			if count == 0 || count > math.MaxInt32 {
				continue
			}
			v, ok := r.value(width)
			if !ok {
				continue // the bytes of a value that is not there are not taken
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

// hybridRuns walks the runs of Parquet's RLE / bit-packed hybrid encoding as the
// column reader's RleDecoder and BitReader read them.
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
// lie whole in b; it stops at the first that does not. Values of 1 bit, an OPTIONAL
// column's levels, are counted by their bits set, eight bytes a load.
func packedCount(b []byte, width, n int, want int16) (hits, read int) {
	if width == 0 {
		if want == 0 {
			return n, n
		}
		return 0, n
	}
	n = min(n, len(b)*8/width)
	if width == 1 {
		ones, i := 0, 0
		for ; (i+8)*8 <= n; i += 8 {
			ones += bits.OnesCount64(binary.LittleEndian.Uint64(b[i:]))
		}
		for ; (i+1)*8 <= n; i++ {
			ones += bits.OnesCount8(b[i])
		}
		if r := n - i*8; r > 0 {
			ones += bits.OnesCount8(b[i] & (1<<r - 1))
		}
		switch want {
		case 1:
			return ones, n
		case 0:
			return n - ones, n
		}
		return 0, n
	}
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
