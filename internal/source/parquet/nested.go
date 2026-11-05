package parquet

import (
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

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
	dt     dtype.DataType // the LIST type, or an Array's; the child gets its element

	present bool // the open row is a list, not a null one

	// emptyDef is the level at which the list is present and EMPTY, and nullElem
	// the level of a null element, or -1 when elements cannot be null. Both depend
	// on the SHAPE, not only on maxDef: the comment above describes an optional
	// list of optional elements, and a list of REQUIRED elements has no null-element
	// level at all — its empty list sits at maxDef-1, which the old rule read as a
	// null element, so [] came back as [null] (audit.md §5, I6). A REQUIRED list has
	// no null-list level, so its empty list is def 0, which the old rule read as a
	// null list, in a column declared non-nullable (I13). newListReader derives both.
	emptyDef int16
	nullElem int16

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
					if err := c.closeRow(); err != nil {
						return 0, err
					}
					rows++
					if rows == n {
						c.open = false
						goto done
					}
				}
				c.open = true
				c.present = def >= c.emptyDef
				c.valid.Append(c.present) // shallower is a null list
			}
			if def == c.maxDef {
				c.elems.appendVal()
			} else if def == c.nullElem {
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
		if err := c.closeRow(); err != nil {
			return 0, err
		}
		c.open = false
		rows++
	}
done:
	return rows, nil
}

// closeRow ends the open row at the elements read so far. An Array's present row has
// its size of them, or the file is not the Array its Arrow schema says it is.
func (c *listCol) closeRow() error {
	end := int32(c.elems.count())
	if c.dt.ID() == dtype.TypeArray && c.present {
		if k := end - c.offs[len(c.offs)-1]; k != int32(c.dt.Size()) {
			return uerr.New(uerr.KindValue, "scan_parquet",
				"a column of %s, as the file's Arrow schema has it, holds a row of %d elements", c.dt, k).
				Hint("cast it to a List, or rewrite the file: every row of an Array has its size")
		}
	}
	c.offs = append(c.offs, end)
	return nil
}

func (c *listCol) finish(name string) *data.Column {
	child := c.elems.finish("item", c.dt.Inner())
	col := data.NewList(name, c.offs, child, c.valid.Finish()).WithDType(c.dt)
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
	// Kept, as fixedCol keeps its own: NewFixed copies.
	e.out = e.out[:0]
	e.valid = bitmap.NewBuilder(0)
	return col
}

// nullElems accumulates the elements of a List(Null): every one is null, so only
// their count is kept, read from the levels.
type nullElems struct {
	levels  func(defs, reps []int16) (int, error)
	closeCR func() error
	n       int
}

func (e *nullElems) readLevels(defs, reps []int16) (int, error) { return e.levels(defs, reps) }

// appendVal is a present element, which a NULL column cannot hold. A file that
// claims one has no value to give, so it is read as the null it must be.
func (e *nullElems) appendVal()   { e.n++ }
func (e *nullElems) appendNull()  { e.n++ }
func (e *nullElems) count() int   { return e.n }
func (e *nullElems) resetCursor() {}
func (e *nullElems) close() error { return e.closeCR() }

func (e *nullElems) finish(name string, elem dtype.DataType) *data.Column {
	col := data.NewNull(name, elem, e.n)
	e.n = 0
	return col
}

// boolElems accumulates Bool list elements, into a bitmap rather than a buffer of
// values, which is the one way a Bool column differs from a fixed-width one.
type boolElems struct {
	cr *file.BooleanColumnChunkReader

	vals  []bool
	vpos  int
	bits  *bitmap.Builder
	valid *bitmap.Builder
	n     int
}

func (e *boolElems) readLevels(defs, reps []int16) (int, error) {
	if cap(e.vals) < len(defs) {
		e.vals = make([]bool, len(defs))
	}
	total, _, err := e.cr.ReadBatch(int64(len(defs)), e.vals[:len(defs)], defs, reps)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"reading a repeated boolean column")
	}
	return int(total), nil
}

func (e *boolElems) appendVal() {
	e.bits.Append(e.vals[e.vpos])
	e.valid.Append(true)
	e.vpos++
	e.n++
}

func (e *boolElems) appendNull() {
	e.bits.Append(false)
	e.valid.Append(false)
	e.n++
}

func (e *boolElems) count() int   { return e.n }
func (e *boolElems) resetCursor() { e.vpos = 0 }
func (e *boolElems) close() error { return e.cr.Close() }

func (e *boolElems) finish(name string, _ dtype.DataType) *data.Column {
	col := data.NewBool(name, e.bits.Finish(), e.valid.Finish())
	e.bits, e.valid, e.n = bitmap.NewBuilder(0), bitmap.NewBuilder(0), 0
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
// optional is whether the file being read declares the struct itself optional —
// which is not f.Nullable when several files are read as one.
func newStructReader(f dtype.Field, leaves []int, optional bool,
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

	if optional {
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
