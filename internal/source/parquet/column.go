package parquet

import (
	"encoding/binary"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// colReader reads one column chunk in row-sized slices.
//
// # Definition levels, and the trap inside them
//
// ReadBatch returns TWO counts: `total`, the number of rows (levels) it consumed,
// and `valuesRead`, the number of values it actually wrote. They differ whenever
// the column has nulls, because the values come back PACKED — with no gaps where
// the nulls are.
//
//	rows      0     1     2     3
//	defLvls   1     0     1     1
//	values    a     b     c            <- valuesRead = 3, total = 4
//	                                      b belongs at row 1? NO: at row 2.
//
// Writing values[i] to row i therefore shifts every value after the first null,
// silently, by an amount that depends on the data. It is the single most
// error-prone part of a Parquet reader, and it is invisible on any fixture whose
// columns happen to have no nulls.
//
// expand below walks the levels backwards from the packed values, which is the
// only place this mapping is implemented.
type colReader interface {
	// read consumes up to n rows and appends them. It returns the rows consumed,
	// which is 0 at the end of the chunk.
	read(n int) (int, error)
	finish(name string) *data.Column
	close() error
}

// batchReader is the ReadBatch shape shared by every typed column chunk reader.
type batchReader[P any] interface {
	ReadBatch(batchSize int64, values []P, defLvls, repLvls []int16) (int64, int, error)
	Close() error
}

// fixedCol reads a physical Parquet type P and stores it as an ursus type T.
type fixedCol[P any, T data.Fixed] struct {
	cr     batchReader[P]
	maxDef int16
	conv   func(P) T

	vals []P     // scratch: packed values from ReadBatch
	defs []int16 // scratch: definition levels

	out   []T
	valid *bitmap.Builder
	dt    dtype.DataType
}

func (c *fixedCol[P, T]) read(n int) (int, error) {
	if cap(c.vals) < n {
		c.vals = make([]P, n)
		if c.maxDef > 0 {
			c.defs = make([]int16, n)
		}
	}
	vals := c.vals[:n]
	var defs []int16
	if c.maxDef > 0 {
		defs = c.defs[:n]
	}

	total, valuesRead, err := c.cr.ReadBatch(int64(n), vals, defs, nil)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a column chunk")
	}
	rows := int(total)
	if rows == 0 {
		return 0, nil
	}

	if c.maxDef == 0 {
		// A REQUIRED column has no nulls and no levels; the values are already dense.
		for i := range valuesRead {
			c.out = append(c.out, c.conv(vals[i]))
		}
		c.valid.AppendMany(true, valuesRead)
		return valuesRead, nil
	}

	var zero T
	vi := 0
	for i := 0; i < rows; {
		// Pack validity 64 bits at a time. AppendBits is width-safe, which matters
		// because the tail word is partial for any row count not a multiple of 64.
		var word uint64
		w := min(64, rows-i)
		for b := range w {
			if defs[i+b] == c.maxDef {
				word |= 1 << uint(b)
				c.out = append(c.out, c.conv(vals[vi]))
				vi++
			} else {
				c.out = append(c.out, zero)
			}
		}
		c.valid.AppendBits(word, w)
		i += w
	}
	if vi != valuesRead {
		// The levels and the value count disagree, which means the file's levels are
		// inconsistent with its data. Catching it here names the cause; letting it
		// through would shift every later value.
		return 0, uerr.New(uerr.KindValue, "scan_parquet",
			"definition levels describe %d values but %d were read", vi, valuesRead)
	}
	return rows, nil
}

func (c *fixedCol[P, T]) finish(name string) *data.Column {
	col := data.NewFixed(name, c.dt, c.out, c.valid.Finish())
	// A fresh buffer per batch: NewFixed wraps without copying, so reusing the
	// slice would rewrite a batch the consumer still holds.
	c.out = nil
	c.valid = bitmap.NewBuilder(0)
	return col
}

func (c *fixedCol[P, T]) close() error { return c.cr.Close() }

// boolCol is separate because Bool is stored as a bitmap rather than a values
// buffer — the one ursus type outside data.Fixed.
type boolCol struct {
	cr     *file.BooleanColumnChunkReader
	maxDef int16

	vals []bool
	defs []int16

	out   *bitmap.Builder
	valid *bitmap.Builder
	n     int
}

func (c *boolCol) read(n int) (int, error) {
	if cap(c.vals) < n {
		c.vals = make([]bool, n)
		if c.maxDef > 0 {
			c.defs = make([]int16, n)
		}
	}
	vals := c.vals[:n]
	var defs []int16
	if c.maxDef > 0 {
		defs = c.defs[:n]
	}

	total, valuesRead, err := c.cr.ReadBatch(int64(n), vals, defs, nil)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a boolean chunk")
	}
	rows := int(total)
	if rows == 0 {
		return 0, nil
	}

	if c.maxDef == 0 {
		for i := range valuesRead {
			c.out.Append(vals[i])
		}
		c.valid.AppendMany(true, valuesRead)
		c.n += valuesRead
		return valuesRead, nil
	}

	vi := 0
	for i := range rows {
		if defs[i] == c.maxDef {
			c.out.Append(vals[vi])
			c.valid.Append(true)
			vi++
		} else {
			c.out.Append(false)
			c.valid.Append(false)
		}
	}
	c.n += rows
	return rows, nil
}

func (c *boolCol) finish(name string) *data.Column {
	col := data.NewBool(name, c.out.Finish(), c.valid.Finish())
	c.out = bitmap.NewBuilder(0)
	c.valid = bitmap.NewBuilder(0)
	c.n = 0
	return col
}

func (c *boolCol) close() error { return c.cr.Close() }

// byteArrayCol reads BYTE_ARRAY as String or Binary.
type byteArrayCol struct {
	cr     *file.ByteArrayColumnChunkReader
	maxDef int16
	dt     dtype.DataType

	vals []parquet.ByteArray
	defs []int16

	// Arrow's layout, accumulated directly. This used to be an []string with one
	// Go string appended per value — one heap allocation per row, and then a
	// second full copy of every byte when data.NewString turned the slice into
	// these same two buffers. A scan of a million rows with two string columns
	// spent 2.14M allocations on it, which a profile put at 35% of every object
	// the engine allocated.
	//
	// offs carries the usual n+1 entries, so it starts with a single zero and
	// gains one entry per value, null or not.
	offs  []int32
	chars []byte
	valid *bitmap.Builder
}

// appendVal records one present value. The copy is not optional: the ByteArray
// points into a decode buffer arrow-go reuses across batches.
func (c *byteArrayCol) appendVal(v parquet.ByteArray) {
	c.chars = append(c.chars, v...)
	c.offs = append(c.offs, int32(len(c.chars)))
}

// appendNull records one absent value as an empty slice — the offset does not
// advance, so it costs nothing beyond its offset entry.
func (c *byteArrayCol) appendNull() {
	c.offs = append(c.offs, int32(len(c.chars)))
}

func (c *byteArrayCol) read(n int) (int, error) {
	if cap(c.vals) < n {
		c.vals = make([]parquet.ByteArray, n)
		if c.maxDef > 0 {
			c.defs = make([]int16, n)
		}
	}
	vals := c.vals[:n]
	var defs []int16
	if c.maxDef > 0 {
		defs = c.defs[:n]
	}

	total, valuesRead, err := c.cr.ReadBatch(int64(n), vals, defs, nil)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a byte-array chunk")
	}
	rows := int(total)
	if rows == 0 {
		return 0, nil
	}

	// The mandatory leading zero, seeded once per column rather than branched on
	// per value. finish then hands offs straight over with no fixup.
	if c.offs == nil {
		c.offs = make([]int32, 1, rows+1)
	}

	if c.maxDef == 0 {
		for i := range valuesRead {
			c.appendVal(vals[i])
		}
		c.valid.AppendMany(true, valuesRead)
		return valuesRead, nil
	}

	vi := 0
	for i := range rows {
		if defs[i] == c.maxDef {
			c.appendVal(vals[vi])
			c.valid.Append(true)
			vi++
		} else {
			c.appendNull()
			c.valid.Append(false)
		}
	}
	return rows, nil
}

func (c *byteArrayCol) finish(name string) *data.Column {
	col := data.NewStringParts(name, c.offs, c.chars, c.valid.Finish())
	c.offs, c.chars = nil, nil
	c.valid = bitmap.NewBuilder(0)
	return col.WithDType(c.dt)
}

func (c *byteArrayCol) close() error { return c.cr.Close() }

// --- decimal conversions --------------------------------------------------------

// decimalFromBytes decodes a big-endian two's-complement integer of up to 16
// bytes, which is how Parquet stores a DECIMAL in a (FIXED_LEN_)BYTE_ARRAY.
//
// Sign extension is the part worth stating: a 4-byte -1 is ff ff ff ff, and
// zero-extending it to 128 bits would produce 4294967295 rather than -1.
func decimalFromBytes(b []byte) (i128.Int128, error) {
	if len(b) == 0 {
		return i128.Zero, nil
	}
	if len(b) > 16 {
		return i128.Zero, uerr.New(uerr.KindValue, "scan_parquet",
			"a DECIMAL value is %d bytes, which does not fit in 128 bits", len(b))
	}

	var buf [16]byte
	if b[0]&0x80 != 0 {
		for i := range buf {
			buf[i] = 0xff // sign-extend a negative value
		}
	}
	copy(buf[16-len(b):], b)

	return i128.Int128{
		Hi: int64(binary.BigEndian.Uint64(buf[0:8])),
		Lo: binary.BigEndian.Uint64(buf[8:16]),
	}, nil
}

// listCol reads a repeated column into offsets plus a child column.
//
// # Rows do not line up with ReadBatch
//
// Every other reader here can treat one level as one row. A repeated column
// cannot: ReadBatch's budget is in LEVELS, one row holds as many levels as it has
// elements, and a row may therefore straddle two calls. That is the whole reason
// lists were not readable before this.
//
// The fix is a persistent cursor. Levels are refilled a block at a time and
// consumed across calls; a row is closed only when the NEXT rep == 0 arrives, or
// at end of chunk. `open` survives a refill, so a row split across two ReadBatch
// calls is assembled correctly — which is exactly the case a fixture with short
// lists never produces, so the test uses a list longer than the read block.
//
// # The four states of a row
//
// For the standard three-level encoding
//
//	optional group tags (LIST) { repeated group list { optional T element } }
//
// maxDef is 3 and maxRep is 1, and definition level alone distinguishes:
//
//	def == 3        an element is present
//	def == 2        an element that is NULL, inside a present list
//	def == 1        the list is present and EMPTY  <- no element consumed
//	def == 0        the list itself is null
//
// The two that get confused are the last two: both append no elements and leave
// the offset unmoved, and only the validity bit tells them apart. That is the same
// trap as an empty string against a null string, and it fails the same way — one
// row quietly holding the wrong answer.
// listElems owns the typed buffer ReadBatch fills, and knows how to append one
// element or one null. The level machine above it never sees an element type.
//
// The split exists because listCol.read is fifty lines of repetition-level state
// machine of which exactly three were element-specific: the typed value buffer,
// appending a present value, and appending a null. Everything else — the
// persistent cursor, the row boundaries, the four row states, the offsets — is
// element-agnostic, and was being re-instantiated per element type for no reason.
//
// It also means List(Bool) and List(Decimal) are one more accumulator each rather
// than another instantiation of the whole machine.
type listElems interface {
	// readLevels fills defs and reps, and its own value buffer, returning the
	// number of LEVELS read. Zero means end of chunk.
	readLevels(defs, reps []int16) (int, error)
	// appendVal appends the next buffered value, advancing its own cursor.
	appendVal()
	// appendNull appends a null ELEMENT — one that occupies a slot inside a
	// present list, as distinct from a list that is empty or null.
	appendNull()
	// count is the number of ELEMENTS accumulated, which is what the list's
	// offsets are built from.
	//
	// Worth stating because it is the one place the implementations differ in a
	// way that is easy to get wrong: a byte-array accumulator holds TWO offset
	// arrays, one counting elements and one counting bytes, and the list wants
	// the first.
	count() int
	// resetCursor is called after a refill, when the value cursor restarts.
	resetCursor()
	finish(name string, elem dtype.DataType) *data.Column
	close() error
}

// listCol reads a repeated column into offsets plus a child column.
//
// # Rows do not line up with ReadBatch
//
// Every other reader here can treat one level as one row. A repeated column
// cannot: ReadBatch's budget is in LEVELS, one row holds as many levels as it has
// elements, and a row may therefore straddle two calls. That is the whole reason
// lists were not readable before step 29.
//
// The fix is a persistent cursor. Levels are refilled a block at a time and
// consumed across calls; a row is closed only when the NEXT rep == 0 arrives, or
// at end of chunk. `open` survives a refill, so a row split across two ReadBatch
// calls is assembled correctly — which is exactly the case a fixture with short
// lists never produces, so the test uses a list longer than the read block.
//
// # The four states of a row
//
// For the standard three-level encoding
//
//	optional group tags (LIST) { repeated group list { optional T element } }
//
// maxDef is 3 and maxRep is 1, and definition level alone distinguishes:
//
//	def == 3        an element is present
//	def == 2        an element that is NULL, inside a present list
//	def == 1        the list is present and EMPTY  <- no element consumed
//	def == 0        the list itself is null
//
// The two that get confused are the last two: both append no elements and leave
// the offset unmoved, and only the validity bit tells them apart. That is the same
// trap as an empty string against a null string, and it fails the same way — one
// row quietly holding the wrong answer.
type listCol struct {
	elems  listElems
	maxDef int16 // element present
	maxRep int16
	dt     dtype.DataType // the LIST type; the child gets its element

	// Level scratch, and a cursor into it that survives across read calls.
	defs []int16
	reps []int16
	nLev int // levels currently in scratch
	pos  int // next unconsumed level
	eof  bool

	open bool // a row has been started and not yet closed

	offs  []int32 // n+1 entries; the leading zero is planted once
	valid *bitmap.Builder
}

const listLevelBlock = 4096

func (c *listCol) refill() error {
	if c.eof {
		return nil
	}
	if cap(c.defs) < listLevelBlock {
		c.defs = make([]int16, listLevelBlock)
		c.reps = make([]int16, listLevelBlock)
	}
	total, err := c.elems.readLevels(c.defs[:listLevelBlock], c.reps[:listLevelBlock])
	if err != nil {
		return err
	}
	c.nLev, c.pos = total, 0
	c.elems.resetCursor()
	if total == 0 {
		c.eof = true
	}
	return nil
}

func (c *listCol) read(n int) (int, error) {
	if c.offs == nil {
		c.offs = make([]int32, 1, n+1)
	}
	rows := 0
	for rows < n {
		if c.pos >= c.nLev {
			if err := c.refill(); err != nil {
				return 0, err
			}
			if c.eof {
				break
			}
		}
		for ; c.pos < c.nLev; c.pos++ {
			def, rep := c.defs[c.pos], c.reps[c.pos]
			if rep == 0 {
				// A new row starts here. Close the previous one first — and stop if
				// the caller has all it asked for, leaving the cursor on this level
				// so the next call resumes exactly here.
				if c.open {
					c.offs = append(c.offs, int32(c.elems.count()))
					rows++
					if rows == n {
						c.open = false
						goto done
					}
				}
				c.open = true
				c.valid.Append(def > 0) // def 0 is a null list
			}
			if def == c.maxDef {
				c.elems.appendVal()
			} else if def == c.maxDef-1 && c.maxDef >= 2 {
				// A null ELEMENT inside a present list. It occupies a slot; an empty
				// list does not, which is the whole distinction.
				c.elems.appendNull()
			}
			// Anything shallower is an empty or null list: no element, and the
			// offset does not move.
		}
	}
	// End of chunk closes whatever row is still open.
	if c.eof && c.open {
		c.offs = append(c.offs, int32(c.elems.count()))
		c.open = false
		rows++
	}
done:
	return rows, nil
}

func (c *listCol) finish(name string) *data.Column {
	child := c.elems.finish("item", c.dt.Inner())
	col := data.NewList(name, c.offs, child, c.valid.Finish())
	// Fresh buffers per batch, for the reason fixedCol gives: the constructors
	// wrap without copying, so reusing them would rewrite a batch the consumer is
	// still holding.
	c.offs = nil
	c.valid = bitmap.NewBuilder(0)
	return col
}

func (c *listCol) close() error { return c.elems.close() }

// fixedElems accumulates fixed-width list elements.
type fixedElems[P any, T data.Fixed] struct {
	cr   batchReader[P]
	conv func(P) T

	vals  []P
	vpos  int
	out   []T
	valid *bitmap.Builder
}

func (e *fixedElems[P, T]) readLevels(defs, reps []int16) (int, error) {
	if cap(e.vals) < len(defs) {
		e.vals = make([]P, len(defs))
	}
	total, _, err := e.cr.ReadBatch(int64(len(defs)), e.vals[:len(defs)], defs, reps)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"reading a repeated column")
	}
	return int(total), nil
}

func (e *fixedElems[P, T]) appendVal() {
	e.out = append(e.out, e.conv(e.vals[e.vpos]))
	e.valid.Append(true)
	e.vpos++
}

func (e *fixedElems[P, T]) appendNull() {
	var zero T
	e.out = append(e.out, zero)
	e.valid.Append(false)
}

func (e *fixedElems[P, T]) count() int   { return len(e.out) }
func (e *fixedElems[P, T]) resetCursor() { e.vpos = 0 }
func (e *fixedElems[P, T]) close() error { return e.cr.Close() }

func (e *fixedElems[P, T]) finish(name string, elem dtype.DataType) *data.Column {
	col := data.NewFixed(name, elem, e.out, e.valid.Finish())
	e.out = nil
	e.valid = bitmap.NewBuilder(0)
	return col
}

// byteArrayElems accumulates String or Binary list elements.
//
// It is byteArrayCol's arena, unchanged: characters appended into one buffer and
// an offset pushed per element, finished through data.NewStringParts. Step 22
// established that shape to kill a per-value allocation, and it is reused rather
// than rewritten.
//
// The two offset arrays are the thing to keep straight. THIS one counts BYTES and
// belongs to the string child; the one listCol builds counts ELEMENTS and belongs
// to the list. count() returns the second, which is why it is len(offs)-1 and not
// len(chars).
type byteArrayElems struct {
	cr *file.ByteArrayColumnChunkReader

	vals []parquet.ByteArray
	vpos int

	offs  []int32
	chars []byte
	valid *bitmap.Builder
}

func (e *byteArrayElems) readLevels(defs, reps []int16) (int, error) {
	if cap(e.vals) < len(defs) {
		e.vals = make([]parquet.ByteArray, len(defs))
	}
	total, _, err := e.cr.ReadBatch(int64(len(defs)), e.vals[:len(defs)], defs, reps)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"reading a repeated byte-array column")
	}
	return int(total), nil
}

// seed plants the mandatory leading zero once, so finish hands offs straight over.
func (e *byteArrayElems) seed() {
	if e.offs == nil {
		e.offs = make([]int32, 1, listLevelBlock+1)
	}
}

func (e *byteArrayElems) appendVal() {
	e.seed()
	// The copy is not optional: the ByteArray points into a decode buffer arrow-go
	// reuses across batches.
	e.chars = append(e.chars, e.vals[e.vpos]...)
	e.offs = append(e.offs, int32(len(e.chars)))
	e.valid.Append(true)
	e.vpos++
}

func (e *byteArrayElems) appendNull() {
	e.seed()
	// The byte offset does not advance — a null element is an empty slice — but an
	// offset entry is still pushed, because the element occupies a slot.
	e.offs = append(e.offs, int32(len(e.chars)))
	e.valid.Append(false)
}

func (e *byteArrayElems) count() int {
	if e.offs == nil {
		return 0
	}
	return len(e.offs) - 1
}

func (e *byteArrayElems) resetCursor() { e.vpos = 0 }
func (e *byteArrayElems) close() error { return e.cr.Close() }

func (e *byteArrayElems) finish(name string, elem dtype.DataType) *data.Column {
	e.seed()
	col := data.NewStringParts(name, e.offs, e.chars, e.valid.Finish())
	e.offs, e.chars = nil, nil
	e.valid = bitmap.NewBuilder(0)
	return col.WithDType(elem)
}
