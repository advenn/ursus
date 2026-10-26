package parquet

import (
	"encoding/binary"
	"math"
	"unsafe"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/uerr"
)

// The reader's own pages (step 192, item P1 of v0.6.1-scope.md).
//
// arrow-go's typed ColumnChunkReader decodes a page's definition levels into an
// int16 a row, even in a column without a null, and a dictionary string twice: its
// dictConverter copies each value's slice header out of the dictionary, and
// byteArrayCol then copies its bytes. In v0.6.0's profiles that was 25 to 31% of
// q19, q12 and h2o's j2 and j3, and the levels 3 to 10% of every query.
//
// So for the columns that are most of every file, flat and of a plain physical
// type, the reader takes the chunk's pages from arrow-go's public page reader,
// decompressed, and decodes them itself:
//
//   - the definition levels as runs, so a run of present values is one run;
//   - PLAIN values, copied as they lie, a fixed-width page with one copy;
//   - a dictionary's indices, each value gathered from the dictionary once.
//
// Every other chunk reads through arrow-go as before: a nested column, a Boolean,
// INT96 or FIXED_LEN_BYTE_ARRAY, and any encoding but PLAIN and the dictionary's.
// The chunk's metadata decides, before its first page.

// ownPages turns the reader's own page decoding on; a test turns it off to compare.
var ownPages = true

// ownable reports whether column ci of a row group is read from its pages here.
//
// A leaf inside a struct is left to arrow-go even at level 1: the struct's first
// field also answers the struct's own validity (parentTracker), which only
// fixedCol's and byteArrayCol's level-reading paths record.
func ownable(rg *metadata.RowGroupMetaData, ci int, desc *schema.Column, dt dtype.DataType) bool {
	if !ownPages || desc.MaxRepetitionLevel() != 0 || desc.MaxDefinitionLevel() > 1 ||
		len(desc.ColumnPath()) != 1 || !ownType(desc.PhysicalType(), dt.ID()) {
		return false
	}
	cc, err := rg.ColumnChunk(ci)
	if err != nil {
		return false
	}
	for _, e := range cc.Encodings() {
		switch e {
		case parquet.Encodings.Plain, parquet.Encodings.PlainDict, parquet.Encodings.RLEDict,
			parquet.Encodings.RLE:
		default:
			return false
		}
	}
	return true
}

// ownType is whether newOwnColReader maps physical type t to ursus type id, as
// newColReader does; any other pairing keeps newColReader's answer, error and all.
func ownType(t parquet.Type, id dtype.TypeID) bool {
	switch t {
	case parquet.Types.Int32:
		switch id {
		case dtype.TypeDecimal, dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeDate,
			dtype.TypeTime, dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32:
			return true
		}
	case parquet.Types.Int64:
		switch id {
		case dtype.TypeDecimal, dtype.TypeInt64, dtype.TypeDatetime, dtype.TypeDuration,
			dtype.TypeTime, dtype.TypeUint64:
			return true
		}
	case parquet.Types.Float:
		return id == dtype.TypeFloat32
	case parquet.Types.Double:
		return id == dtype.TypeFloat64
	case parquet.Types.ByteArray:
		return id == dtype.TypeString || id == dtype.TypeBinary || id == dtype.TypeCategorical
	}
	return false
}

// pageChunk walks one column chunk's pages: the dictionary, then each data page's
// levels and values.
type pageChunk struct {
	pr     file.PageReader
	maxDef int16
	size   int // a fixed value's bytes; 0 for BYTE_ARRAY
	name   string

	// The current data page.
	left  int    // rows it has not given
	defs  hybrid // its definition levels, when maxDef is 1
	dict  bool   // its values are dictionary indices
	plain []byte // a PLAIN page's values not yet read
	idx   hybrid // a dictionary page's indices

	// The chunk's dictionary: PLAIN fixed-width values, or the offsets and
	// characters of BYTE_ARRAY ones. Copied, since a page's bytes are reused.
	dictN     int
	dictFixed []byte
	dictOffs  []int32
	dictChars []byte

	lvl []uint32 // scratch: one batch's levels where a run is bit-packed
	ids []uint32 // scratch: indices

	// A String chunk marked for a dictionary's conjuncts (dictfilter.go): the
	// table, once, and the rows mark read for emit.
	tableDone bool
	keep      []bool
	pend      []int32 // a row's dictionary index, -1 for a null, -1-p for PLAIN value p
	poffs     []int32
	pchars    []byte
}

func newPageChunk(pr file.PageReader, desc *schema.Column, size int) *pageChunk {
	return &pageChunk{pr: pr, maxDef: desc.MaxDefinitionLevel(), size: size, name: desc.Name()}
}

func (c *pageChunk) corrupt(format string, args ...any) error {
	return uerr.New(uerr.KindValue, "scan_parquet", "column %q: "+format,
		append([]any{c.name}, args...)...).Hint("the file is corrupt or was truncated")
}

// nextPage moves to the next data page, reading a dictionary page on the way. It
// returns false at the end of the chunk.
func (c *pageChunk) nextPage() (bool, error) {
	for c.pr.Next() {
		var data []byte
		var rows int
		switch p := c.pr.Page().(type) {
		case *file.DictionaryPage:
			if err := c.readDictionary(p); err != nil {
				return false, err
			}
			continue
		case *file.DataPageV1:
			data, rows = p.Data(), int(p.NumValues())
			if c.maxDef > 0 {
				if p.DefinitionLevelEncoding() != parquet.Encodings.RLE {
					return false, uerr.New(uerr.KindUnsupported, "scan_parquet",
						"column %q: definition levels encoded as %s", c.name, p.DefinitionLevelEncoding())
				}
				if len(data) < 4 {
					return false, c.corrupt("a page ends inside its levels")
				}
				n := int(binary.LittleEndian.Uint32(data))
				if n < 0 || 4+n > len(data) {
					return false, c.corrupt("a page's levels run past it")
				}
				c.defs.init(data[4:4+n], 1)
				data = data[4+n:]
			}
		case *file.DataPageV2:
			data, rows = p.Data(), int(p.NumValues())
			rl, dl := int(p.RepetitionLevelByteLen()), int(p.DefinitionLevelByteLen())
			if rl < 0 || dl < 0 || rl+dl > len(data) {
				return false, c.corrupt("a page's levels run past it")
			}
			if c.maxDef > 0 {
				c.defs.init(data[rl:rl+dl], 1)
				if dl == 0 && p.NumNulls() == 0 {
					// No levels written for a page without a null: one run of present.
					c.defs = hybrid{rle: true, val: 1, left: int(p.NumValues())}
				}
			}
			data = data[rl+dl:]
		default:
			continue // an index page
		}
		switch e := parquet.Encoding(c.pr.Page().Encoding()); e {
		case parquet.Encodings.Plain:
			c.dict, c.plain = false, data
		case parquet.Encodings.PlainDict, parquet.Encodings.RLEDict:
			if c.dictOffs == nil && c.dictFixed == nil {
				return false, c.corrupt("a page refers to a dictionary the chunk does not have")
			}
			if len(data) == 0 {
				// A page whose every row is null holds no indices.
				c.idx.init(nil, 0)
			} else {
				if data[0] > 32 {
					return false, c.corrupt("dictionary indices of %d bits", data[0])
				}
				c.idx.init(data[1:], int(data[0]))
			}
			c.dict = true
		default:
			return false, uerr.New(uerr.KindUnsupported, "scan_parquet",
				"column %q: a page encoded as %s", c.name, e)
		}
		if rows < 0 {
			return false, c.corrupt("a page of %d rows", rows)
		}
		c.left = rows
		if rows > 0 {
			return true, nil
		}
	}
	if err := c.pr.Err(); err != nil {
		return false, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading column %q", c.name)
	}
	return false, nil
}

// readDictionary copies a dictionary page's values: PLAIN, whatever the page says,
// since PLAIN_DICTIONARY on a dictionary page means PLAIN.
func (c *pageChunk) readDictionary(p *file.DictionaryPage) error {
	switch parquet.Encoding(p.Encoding()) {
	case parquet.Encodings.Plain, parquet.Encodings.PlainDict:
	default:
		return uerr.New(uerr.KindUnsupported, "scan_parquet",
			"column %q: a dictionary encoded as %s", c.name, parquet.Encoding(p.Encoding()))
	}
	data, n := p.Data(), int(p.NumValues())
	if n < 0 {
		return c.corrupt("a dictionary of %d values", n)
	}
	c.dictN = n
	if c.size > 0 {
		if n*c.size > len(data) {
			return c.corrupt("a dictionary page ends inside its values")
		}
		c.dictFixed = append([]byte(nil), data[:n*c.size]...)
		return nil
	}
	offs := make([]int32, 1, n+1)
	chars := make([]byte, 0, max(len(data)-4*n, 0))
	for range n {
		if len(data) < 4 {
			return c.corrupt("a dictionary page ends inside its values")
		}
		l := int(binary.LittleEndian.Uint32(data))
		if l < 0 || 4+l > len(data) {
			return c.corrupt("a dictionary value runs past its page")
		}
		chars = append(chars, data[4:4+l]...)
		offs = append(offs, int32(len(chars)))
		data = data[4+l:]
	}
	// Sixteen bytes of padding past the last value, so gather can copy a short
	// value as two words without reading past the allocation.
	chars = append(chars, make([]byte, gatherPad)...)
	c.dictOffs, c.dictChars = offs, chars[:len(chars)-gatherPad]
	return nil
}

// levels reads the next k rows' definition levels. all says every row is present,
// and then nothing is written; otherwise c.lvl[:k] holds each row's level, 0 or 1,
// and present counts the ones.
func (c *pageChunk) levels(k int) (present int, all bool, err error) {
	if c.maxDef == 0 {
		return k, true, nil
	}
	// The common case: one RLE run of present rows covers them all.
	if c.defs.left >= k && c.defs.rle && c.defs.val == 1 {
		c.defs.left -= k
		return k, true, nil
	}
	if cap(c.lvl) < k {
		c.lvl = make([]uint32, k)
	}
	lvl := c.lvl[:k]
	for i := 0; i < k; {
		val, n, ok, err := c.defs.run(k-i, lvl[i:], "definition level")
		if err != nil {
			return 0, false, err
		}
		if ok {
			if val > 1 {
				return 0, false, c.corrupt("a definition level of %d", val)
			}
			for j := i; j < i+n; j++ {
				lvl[j] = val
			}
			if val == 1 {
				present += n
			}
		} else {
			for _, v := range lvl[i : i+n] {
				if v > 1 {
					return 0, false, c.corrupt("a definition level of %d", v)
				}
				present += int(v)
			}
		}
		i += n
	}
	return present, present == k, nil
}

// indices reads the next n dictionary indices into c.ids, each checked against the
// dictionary.
func (c *pageChunk) indices(n int) ([]uint32, error) {
	if cap(c.ids) < n {
		c.ids = make([]uint32, n)
	}
	ids := c.ids[:n]
	if n == 0 {
		return ids, nil
	}
	if err := c.idx.read(ids, "dictionary index"); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if int(id) >= c.dictN {
			return nil, c.corrupt("dictionary index %d of %d values", id, c.dictN)
		}
	}
	return ids, nil
}

// pageFixed reads a fixed-width chunk of physical type P from its pages.
type pageFixed[P int32 | int64 | float32 | float64] struct {
	*pageChunk
}

// readSpaced reads up to n rows into out, a present row's value in its place and a
// null's slot zero, and appends their validity. It returns the rows read, fewer
// than n only at the end of the chunk.
func (r pageFixed[P]) readSpaced(n int, out []P, valid *lazyValid) (int, error) {
	rows := 0
	for rows < n {
		if r.left == 0 {
			more, err := r.nextPage()
			if err != nil {
				return 0, err
			}
			if !more {
				break
			}
		}
		k := min(n-rows, r.left)
		present, all, err := r.levels(k)
		if err != nil {
			return 0, err
		}
		dst := out[rows : rows+k]
		if all {
			if err := r.values(dst); err != nil {
				return 0, err
			}
			valid.appendValid(k)
		} else {
			// The present values land at the front, and are spread into place from the
			// back, where none is overwritten before it moves.
			if err := r.values(dst[:present]); err != nil {
				return 0, err
			}
			vi := present - 1
			for i := k - 1; i >= 0; i-- {
				if r.lvl[i] == 1 {
					dst[i] = dst[vi]
					vi--
				} else {
					dst[i] = 0
				}
			}
			appendLevels(valid, r.lvl[:k])
		}
		r.left -= k
		rows += k
	}
	return rows, nil
}

// keptSegment reads up to n rows of one page, as segment does, and where every row
// is present appends to dst only the rows of sel, positions counted from base,
// decoding no other: a PLAIN value loaded from the page where it lies, a dictionary
// index looked up. It returns the rows read, 0 at the end of the chunk, and how many
// of sel fell among them. Where a row is null it appends nothing: it returns the
// rows' levels, and their present values packed into *vals, grown to hold them, for
// the caller to place.
func (r pageFixed[P]) keptSegment(n int, sel []int32, base int, dst []P, vals *[]P) (k, used int, out []P, lvl []uint32, err error) {
	if r.left == 0 {
		more, err := r.nextPage()
		if err != nil || !more {
			return 0, 0, dst, nil, err
		}
	}
	k = min(n, r.left)
	present, all, err := r.levels(k)
	if err != nil {
		return 0, 0, dst, nil, err
	}
	for used < len(sel) && int(sel[used]) < base+k {
		used++
	}
	if !all {
		if cap(*vals) < present {
			*vals = make([]P, present)
		}
		if err := r.values((*vals)[:present]); err != nil {
			return 0, 0, dst, nil, err
		}
		r.left -= k
		return k, used, dst, r.lvl[:k], nil
	}
	if r.dict {
		ids, err := r.indices(k)
		if err != nil {
			return 0, 0, dst, nil, err
		}
		dict := unsafe.Slice((*P)(unsafe.Pointer(unsafe.SliceData(r.dictFixed))), r.dictN)
		for _, s := range sel[:used] {
			dst = append(dst, dict[ids[int(s)-base]])
		}
	} else {
		bytes := k * r.size
		if bytes > len(r.plain) {
			return 0, 0, dst, nil, r.corrupt("a page ends inside its values")
		}
		dst = gatherPlain(dst, r.plain[:bytes], sel[:used], base)
		r.plain = r.plain[bytes:]
	}
	r.left -= k
	return k, used, dst, nil, nil
}

// gatherPlain appends the PLAIN values of plain at sel's positions, counted from base:
// each loaded from its bytes, little-endian, wherever on the page they lie.
func gatherPlain[P int32 | int64 | float32 | float64](dst []P, plain []byte, sel []int32, base int) []P {
	switch d := any(dst).(type) {
	case []int64:
		for _, s := range sel {
			d = append(d, int64(binary.LittleEndian.Uint64(plain[(int(s)-base)*8:])))
		}
		return any(d).([]P)
	case []float64:
		for _, s := range sel {
			d = append(d, math.Float64frombits(binary.LittleEndian.Uint64(plain[(int(s)-base)*8:])))
		}
		return any(d).([]P)
	case []int32:
		for _, s := range sel {
			d = append(d, int32(binary.LittleEndian.Uint32(plain[(int(s)-base)*4:])))
		}
		return any(d).([]P)
	case []float32:
		for _, s := range sel {
			d = append(d, math.Float32frombits(binary.LittleEndian.Uint32(plain[(int(s)-base)*4:])))
		}
		return any(d).([]P)
	}
	panic("unreachable")
}

// values reads len(dst) present values of the current page.
func (r pageFixed[P]) values(dst []P) error {
	if len(dst) == 0 {
		return nil
	}
	if !r.dict {
		bytes := len(dst) * r.size
		if bytes > len(r.plain) {
			return r.corrupt("a page ends inside its values")
		}
		// Parquet's PLAIN is little-endian, as every platform ursus builds for is.
		copy(unsafe.Slice((*byte)(unsafe.Pointer(&dst[0])), bytes), r.plain[:bytes])
		r.plain = r.plain[bytes:]
		return nil
	}
	ids, err := r.indices(len(dst))
	if err != nil {
		return err
	}
	dict := unsafe.Slice((*P)(unsafe.Pointer(unsafe.SliceData(r.dictFixed))), r.dictN)
	for i, id := range ids {
		dst[i] = dict[id]
	}
	return nil
}

func (r pageFixed[P]) close() error { return r.pr.Close() }

// appendLevels appends a validity bit a level, 64 at a time.
func appendLevels(valid *lazyValid, lvl []uint32) {
	for i := 0; i < len(lvl); i += 64 {
		w := min(64, len(lvl)-i)
		var word uint64
		for b, v := range lvl[i : i+w] {
			word |= uint64(v) << uint(b)
		}
		valid.appendWord(word, w)
	}
}

// pageStrings reads a BYTE_ARRAY chunk from its pages into byteArrayCol's layout.
type pageStrings struct {
	*pageChunk
}

// appendStrings reads up to n rows into c's offsets, characters and validity, and
// returns the rows read, fewer than n only at the end of the chunk.
func (r pageStrings) appendStrings(n int, c *byteArrayCol) (int, error) {
	if c.offs == nil {
		c.offs = make([]int32, 1, n+1)
	}
	c.offs = growExact(c.offs, n)
	rows := 0
	for rows < n {
		if r.left == 0 {
			more, err := r.nextPage()
			if err != nil {
				return 0, err
			}
			if !more {
				break
			}
		}
		k := min(n-rows, r.left)
		present, all, err := r.levels(k)
		if err != nil {
			return 0, err
		}
		if r.dict {
			err = r.gather(c, k, present, all)
		} else {
			err = r.plainValues(c, k, present)
		}
		if err != nil {
			return 0, err
		}
		if all {
			c.valid.appendValid(k)
		} else {
			appendLevels(&c.valid, r.lvl[:k])
		}
		r.left -= k
		rows += k
	}
	return rows, nil
}

// gatherPad is the slack gather's word copies need: past the dictionary's last
// value, and past the column's characters.
const gatherPad = 16

// gather appends k rows of a dictionary page: each present row's value copied once,
// from the dictionary, with the characters counted first so they grow once.
//
// A value of sixteen bytes or fewer, most of a dictionary's, is copied as two
// 8-byte words, whatever its length: the bytes past it are the dictionary's next
// value or its padding, and land past the column's end, where the next value
// overwrites them. A call to memmove a value cost more than the copy.
func (r pageStrings) gather(c *byteArrayCol, k, present int, all bool) error {
	ids, err := r.indices(present)
	if err != nil {
		return err
	}
	offs := r.dictOffs
	src := r.dictChars[:len(r.dictChars)+gatherPad]
	total := 0
	for _, id := range ids {
		total += int(offs[id+1] - offs[id])
	}
	c.chars = growExact(c.chars, total+gatherPad)
	dst := c.chars[:cap(c.chars)]
	at := len(c.chars)
	put := func(id uint32) {
		lo, hi := int(offs[id]), int(offs[id+1])
		if hi-lo <= 16 {
			binary.LittleEndian.PutUint64(dst[at:], binary.LittleEndian.Uint64(src[lo:]))
			binary.LittleEndian.PutUint64(dst[at+8:], binary.LittleEndian.Uint64(src[lo+8:]))
		} else {
			copy(dst[at:], src[lo:hi])
		}
		at += hi - lo
	}
	if all {
		for _, id := range ids {
			put(id)
			c.offs = append(c.offs, int32(at))
		}
	} else {
		vi := 0
		for _, l := range r.lvl[:k] {
			if l == 1 {
				put(ids[vi])
				vi++
			}
			c.offs = append(c.offs, int32(at))
		}
	}
	c.chars = c.chars[:at]
	return nil
}

// plainValues appends k rows of a PLAIN page, each present value's four-byte
// length and then its bytes, copied once. The characters are counted first, so
// they grow once: the column keeps what append's doubling would leave over.
func (r pageStrings) plainValues(c *byteArrayCol, k, present int) error {
	total, at := 0, 0
	for range present {
		if at+4 > len(r.plain) {
			return r.corrupt("a page ends inside its values")
		}
		l := int(binary.LittleEndian.Uint32(r.plain[at:]))
		if l < 0 || at+4+l > len(r.plain) {
			return r.corrupt("a value runs past its page")
		}
		total += l
		at += 4 + l
	}
	c.chars = growExact(c.chars, total)
	all := present == k
	for i := range k {
		if !all && r.lvl[i] == 0 {
			c.offs = append(c.offs, int32(len(c.chars)))
			continue
		}
		l := int(binary.LittleEndian.Uint32(r.plain))
		c.chars = append(c.chars, r.plain[4:4+l]...)
		c.offs = append(c.offs, int32(len(c.chars)))
		r.plain = r.plain[4+l:]
	}
	return nil
}

func (r pageStrings) close() error { return r.pr.Close() }

// newOwnColReader is newColReader for a chunk ownable says is read from its pages.
func newOwnColReader(pr file.PageReader, desc *schema.Column, dt dtype.DataType) (colReader, error) {
	maxDef := desc.MaxDefinitionLevel()
	name := desc.Name()
	switch desc.PhysicalType() {
	case parquet.Types.ByteArray:
		return &byteArrayCol{pages: pageStrings{newPageChunk(pr, desc, 0)}, maxDef: maxDef, dt: dt}, nil
	case parquet.Types.Int32:
		src := pageFixed[int32]{newPageChunk(pr, desc, 4)}
		if dt.ID() == dtype.TypeDecimal {
			return ownFixed[int32, i128.Int128](src, maxDef, dt,
				func(v int32) i128.Int128 { return i128.FromInt64(int64(v)) }), nil
		}
		switch dt.ID() {
		case dtype.TypeInt8:
			return ownFixed[int32, int8](src, maxDef, dt, func(v int32) int8 { return int8(v) }), nil
		case dtype.TypeInt16:
			return ownFixed[int32, int16](src, maxDef, dt, func(v int32) int16 { return int16(v) }), nil
		case dtype.TypeInt32, dtype.TypeDate:
			return ownFixed[int32, int32](src, maxDef, dt, identity[int32]), nil
		case dtype.TypeTime:
			return ownFixed[int32, int64](src, maxDef, dt, func(v int32) int64 { return int64(v) }), nil
		case dtype.TypeUint8:
			return ownFixed[int32, uint8](src, maxDef, dt, func(v int32) uint8 { return uint8(v) }), nil
		case dtype.TypeUint16:
			return ownFixed[int32, uint16](src, maxDef, dt, func(v int32) uint16 { return uint16(v) }), nil
		case dtype.TypeUint32:
			return ownFixed[int32, uint32](src, maxDef, dt, func(v int32) uint32 { return uint32(v) }), nil
		}
	case parquet.Types.Int64:
		src := pageFixed[int64]{newPageChunk(pr, desc, 8)}
		switch dt.ID() {
		case dtype.TypeDecimal:
			return ownFixed[int64, i128.Int128](src, maxDef, dt, i128.FromInt64), nil
		case dtype.TypeInt64, dtype.TypeDatetime, dtype.TypeDuration, dtype.TypeTime:
			return ownFixed[int64, int64](src, maxDef, dt, identity[int64]), nil
		case dtype.TypeUint64:
			return ownFixed[int64, uint64](src, maxDef, dt, func(v int64) uint64 { return uint64(v) }), nil
		}
	case parquet.Types.Float:
		return ownFixed[float32, float32](pageFixed[float32]{newPageChunk(pr, desc, 4)}, maxDef, dt,
			identity[float32]), nil
	case parquet.Types.Double:
		return ownFixed[float64, float64](pageFixed[float64]{newPageChunk(pr, desc, 8)}, maxDef, dt,
			identity[float64]), nil
	}
	pr.Close()
	return nil, uerr.New(uerr.KindUnsupported, "scan_parquet",
		"column %q (%s, stored as %s) has no reader", name, dt, desc.PhysicalType())
}
