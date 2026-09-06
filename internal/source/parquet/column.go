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

// parentTracker is a leaf reader that can also report whether the GROUP enclosing it
// was present on each row.
//
// It exists because a struct's own validity cannot be recovered from its fields. For
// `optional group person { optional int64 age }` the definition levels say three
// different things:
//
//	def 2   person present, age present
//	def 1   person present, age NULL
//	def 0   person NULL
//
// Once the levels are consumed, def 1 and def 0 leave identical evidence behind —
// a null age either way — so the parent's answer has to be recorded while the levels
// are still in hand. Inferring it afterwards from "every field is null" is wrong for
// exactly the row that is a real struct holding nothing.
//
// Any ONE leaf can answer it: every leaf under the group shares the group's
// definition level, whatever its own optionality adds on top. So a struct asks its
// first field and the rest just read values.
type parentTracker interface {
	// trackParent starts recording `def >= level` alongside the column's own
	// validity. Called before the first read.
	trackParent(level int16)
	// parentValidity takes the accumulated bits and resets, the way finish does for
	// the column's own — one batch's worth per call.
	parentValidity() bitmap.View
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

	parentDef int16 // 0 when nothing above this column can be null
	parent    *bitmap.Builder
}

func (c *fixedCol[P, T]) trackParent(level int16) {
	c.parentDef, c.parent = level, bitmap.NewBuilder(0)
}

func (c *fixedCol[P, T]) parentValidity() bitmap.View {
	v := c.parent.Finish()
	c.parent = bitmap.NewBuilder(0)
	return v
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
			if c.parent != nil {
				// >=, not ==: the enclosing group is present for every level at or
				// above its own, including the ones where this field is null.
				c.parent.Append(defs[i+b] >= c.parentDef)
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

	parentDef int16
	parent    *bitmap.Builder
}

func (c *boolCol) trackParent(level int16) {
	c.parentDef, c.parent = level, bitmap.NewBuilder(0)
}

func (c *boolCol) parentValidity() bitmap.View {
	v := c.parent.Finish()
	c.parent = bitmap.NewBuilder(0)
	return v
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
		if c.parent != nil {
			c.parent.Append(defs[i] >= c.parentDef)
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

	parentDef int16
	parent    *bitmap.Builder
}

func (c *byteArrayCol) trackParent(level int16) {
	c.parentDef, c.parent = level, bitmap.NewBuilder(0)
}

func (c *byteArrayCol) parentValidity() bitmap.View {
	v := c.parent.Finish()
	c.parent = bitmap.NewBuilder(0)
	return v
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
		if c.parent != nil {
			c.parent.Append(defs[i] >= c.parentDef)
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

// --- struct -----------------------------------------------------------------------

// structCol reads a Struct of flat fields: one ordinary leaf reader per field, read
// in lockstep, plus the group's own validity.
//
// The lockstep is safe because a struct introduces no REPETITION — every leaf under
// it emits exactly one level per row, so `read(n)` consumes the same n rows from each.
// That is the whole difference from listCol, which needs a cursor precisely because a
// row can straddle a ReadBatch call.
//
// The struct's validity comes from field 0 through parentTracker; see the note there
// for why it cannot be recovered from the fields afterwards.
type structCol struct {
	fields []colReader
	names  []string

	// parent is field 0 when the struct is nullable, nil when it is required.
	// A required struct has no null rows to record, and its leaves carry no level
	// for the group at all.
	parent parentTracker
	rows   int
}

func (c *structCol) read(n int) (int, error) {
	rows := -1
	for i, f := range c.fields {
		got, err := f.read(n)
		if err != nil {
			return 0, err
		}
		// Every field of a struct is one value per row by construction. A
		// disagreement means the file's leaves are inconsistent, and continuing
		// would build fields that describe different rows under one validity bit.
		if rows == -1 {
			rows = got
		} else if got != rows {
			return 0, uerr.New(uerr.KindValue, "scan_parquet",
				"struct fields %q and %q disagree on row count (%d vs %d)",
				c.names[0], c.names[i], rows, got)
		}
	}
	c.rows += rows
	return rows, nil
}

func (c *structCol) finish(name string) *data.Column {
	fields := make([]*data.Column, len(c.fields))
	for i, f := range c.fields {
		fields[i] = f.finish(c.names[i])
	}
	var valid bitmap.View
	if c.parent != nil {
		valid = c.parent.parentValidity()
	} else {
		valid = bitmap.AllSet(c.rows)
	}
	c.rows = 0
	return data.NewStruct(name, fields, valid)
}

func (c *structCol) close() error {
	var first error
	for _, f := range c.fields {
		if err := f.close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// newStructReader opens one leaf reader per struct field.
//
// The group's definition level is 1 for an optional struct and 0 for a required one,
// because this step reads only structs sitting directly under the root — a required
// root contributes nothing, so the struct's own optionality is the entire level.
// Nested structs would have to walk their ancestors instead, and they are refused at
// schema time (structFieldLeaves) rather than mis-read here.
func newStructReader(f dtype.Field, leaves []int,
	open func(ci int, dt dtype.DataType) (colReader, error)) (colReader, error) {

	sub := f.Type.Fields()
	if len(sub) != len(leaves) {
		return nil, uerr.Internalf("scan_parquet: struct %q has %d fields but %d leaves",
			f.Name, len(sub), len(leaves))
	}

	c := &structCol{
		fields: make([]colReader, len(leaves)),
		names:  make([]string, len(leaves)),
	}
	for i, ci := range leaves {
		r, err := open(ci, sub[i].Type)
		if err != nil {
			return nil, err
		}
		c.fields[i] = r
		c.names[i] = sub[i].Name
	}

	if f.Nullable {
		t, ok := c.fields[0].(parentTracker)
		if !ok {
			// Every leaf reader implements it; a shape that does not could only
			// answer "never null" for the struct, which is a wrong answer rather
			// than a missing feature.
			return nil, uerr.Internalf(
				"scan_parquet: %q cannot report the presence of struct %q", c.names[0], f.Name)
		}
		t.trackParent(1)
		c.parent = t
	}
	return c, nil
}
