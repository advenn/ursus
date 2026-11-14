package parquet

import (
	"github.com/apache/arrow-go/v18/parquet/file"

	"github.com/advenn/ursus/dtype"
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

// lazyValid is a column's validity, built only once a null arrives.
//
// Until then every row is valid and only counted, and a batch with no null at all
// finishes with no bitmap: the no-storage form, which every kernel downstream takes
// as its fast path, where a bitmap of ones that says the same thing is not. The
// files PDS-H reads declare every column optional and hold no null in any of them.
type lazyValid struct {
	b       *bitmap.Builder // nil until the first null
	pending int             // valid rows not yet in b
}

func (v *lazyValid) appendValid(n int) {
	if v.b == nil {
		v.pending += n
		return
	}
	v.b.AppendMany(true, n)
}

// appendWord appends the low w bits of word, LSB first.
func (v *lazyValid) appendWord(word uint64, w int) {
	if word == maskBits(w) {
		v.appendValid(w)
		return
	}
	if v.b == nil {
		v.b = bitmap.NewBuilder(v.pending + w)
		v.b.AppendMany(true, v.pending)
		v.pending = 0
	}
	v.b.AppendBits(word, w)
}

func (v *lazyValid) finish() bitmap.View {
	if v.b == nil {
		n := v.pending
		v.pending = 0
		return bitmap.AllSet(n)
	}
	out := v.b.Finish()
	v.b = nil
	return out
}

func maskBits(w int) uint64 {
	if w >= 64 {
		return ^uint64(0)
	}
	return uint64(1)<<uint(w) - 1
}

// appendDefs appends the validity defs say, where def == maxDef is a present value.
func (v *lazyValid) appendDefs(defs []int16, maxDef int16) {
	for i := 0; i < len(defs); i += 64 {
		w := min(64, len(defs)-i)
		var word uint64
		for b, d := range defs[i : i+w] {
			if d == maxDef {
				word |= 1 << uint(b)
			}
		}
		v.appendWord(word, w)
	}
}

// batchReader is the ReadBatch shape shared by every typed column chunk reader.
type batchReader[P any] interface {
	ReadBatch(batchSize int64, values []P, defLvls, repLvls []int16) (int64, int, error)
	Err() error
	Close() error
}

// spacedReader is a chunk read from its pages (step 192): values in their rows'
// places, and validity appended directly, with no level a row.
type spacedReader[P any] interface {
	readSpaced(n int, out []P, valid *lazyValid) (int, error)
	// keptSegment reads up to n rows of one page and appends to dst the rows of sel,
	// positions from base, where every row is present; else it returns their levels
	// and their present values in vals (pageFixed.keptSegment).
	keptSegment(n int, sel []int32, base int, dst []P, vals *[]P) (k, used int, out []P, lvl []uint32, err error)
	close() error
}

// fixedCol reads a physical Parquet type P and stores it as an ursus type T.
type fixedCol[P any, T data.Fixed] struct {
	cr     batchReader[P]
	own    spacedReader[P] // set instead of cr when the chunk is read from its pages
	maxDef int16
	conv   func(P) T
	// direct is set when P and T are one type and conv is the identity: Int32,
	// Int64, Float32, Float64, Date, Datetime, Duration and Time. Values are then
	// read straight into out, with no scratch buffer and no call per value.
	direct bool

	vals []P     // scratch: packed values from ReadBatch
	defs []int16 // scratch: definition levels
	kept []P     // scratch: the kept rows' values before conv, readKept's

	out   []T
	valid lazyValid
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
	if c.own != nil {
		return c.readOwn(n)
	}
	if c.direct {
		return c.readDirect(n)
	}
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
	if err := batchErr(c.cr, total, err); err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a column chunk")
	}
	rows := int(total)
	if rows == 0 {
		return 0, nil
	}

	if c.maxDef == 0 {
		// A REQUIRED column has no nulls and no levels; the values are already dense.
		c.out = growExact(c.out, valuesRead)
		for i := range valuesRead {
			c.out = append(c.out, c.conv(vals[i]))
		}
		c.valid.appendValid(valuesRead)
		return valuesRead, nil
	}

	var zero T
	vi := 0
	c.out = growExact(c.out, rows)
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
		c.valid.appendWord(word, w)
		i += w
	}
	c.trackParentDefs(defs[:rows])
	if vi != valuesRead {
		// The levels and the value count disagree, which means the file's levels are
		// inconsistent with its data. Catching it here names the cause; letting it
		// through would shift every later value.
		return 0, uerr.New(uerr.KindValue, "scan_parquet",
			"definition levels describe %d values but %d were read", vi, valuesRead)
	}
	return rows, nil
}

// readDirect is read for a column stored as it is written. The values arrive
// packed, the nulls left out, so a chunk with nulls is spread into place from the
// back, where no value is overwritten before it is moved.
func (c *fixedCol[P, T]) readDirect(n int) (int, error) {
	start := len(c.out)
	c.out = growExact(c.out, n)
	out := c.out[start : start+n]
	vals := any(out).([]P)
	var defs []int16
	if c.maxDef > 0 {
		if cap(c.defs) < n {
			c.defs = make([]int16, n)
		}
		defs = c.defs[:n]
	}
	total, valuesRead, err := c.cr.ReadBatch(int64(n), vals, defs, nil)
	if err := batchErr(c.cr, total, err); err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a column chunk")
	}
	rows := int(total)
	if c.maxDef == 0 {
		rows = valuesRead
	}
	c.out = c.out[:start+rows]
	if rows == 0 {
		return 0, nil
	}
	if c.maxDef == 0 || valuesRead == rows {
		c.valid.appendValid(rows)
		if c.maxDef > 0 {
			c.trackParentDefs(defs[:rows])
		}
		return rows, nil
	}
	var zero T
	vi := valuesRead - 1
	for i := rows - 1; i >= 0; i-- {
		if defs[i] == c.maxDef {
			if vi < 0 {
				break
			}
			out[i] = out[vi]
			vi--
		} else {
			out[i] = zero
		}
	}
	if vi != -1 {
		return 0, uerr.New(uerr.KindValue, "scan_parquet",
			"definition levels describe %d values but %d were read", valuesRead-vi-1, valuesRead)
	}
	c.valid.appendDefs(defs[:rows], c.maxDef)
	c.trackParentDefs(defs[:rows])
	return rows, nil
}

// readOwn is read for a chunk read from its pages: straight into out when P is T,
// and through the scratch values and conv otherwise, where a null's zero converts
// to T's zero.
func (c *fixedCol[P, T]) readOwn(n int) (int, error) {
	start := len(c.out)
	c.out = growExact(c.out, n)
	if c.direct {
		rows, err := c.own.readSpaced(n, any(c.out[start:start+n]).([]P), &c.valid)
		if err != nil {
			return 0, err
		}
		c.out = c.out[:start+rows]
		return rows, nil
	}
	if cap(c.vals) < n {
		c.vals = make([]P, n)
	}
	rows, err := c.own.readSpaced(n, c.vals[:n], &c.valid)
	if err != nil {
		return 0, err
	}
	for _, v := range c.vals[:rows] {
		c.out = append(c.out, c.conv(v))
	}
	return rows, nil
}

// trackParentDefs records, for a field of a struct, whether the struct was present
// on each row: >=, not ==, because the enclosing group is present for every level at
// or above its own, including the ones where this field is null.
func (c *fixedCol[P, T]) trackParentDefs(defs []int16) {
	if c.parent == nil {
		return
	}
	for _, d := range defs {
		c.parent.Append(d >= c.parentDef)
	}
}

func (c *fixedCol[P, T]) finish(name string) *data.Column {
	// The column takes the values slice, and the next batch's are decoded into a new
	// one. Until step 163 the slice was kept and NewFixed copied it into an Arrow
	// buffer each batch, so every value was written twice. Step 137 had found it
	// dropped every batch as well, allocated twice; keeping it fixed that, and this
	// the copy. TestReadingAllocatesAboutWhatItProduces checks an earlier batch is
	// intact after the later ones.
	col := data.NewFixedOwned(name, c.dt, c.out, c.valid.finish())
	c.out = nil
	return col
}

func (c *fixedCol[P, T]) close() error {
	if c.own != nil {
		return c.own.close()
	}
	return c.cr.Close()
}

// keptReader is a colReader that can append only the rows a predicate keeps, where
// it would append every row read and have them taken afterwards (step 377, R2 of
// v0.7-scope.md): the columns beside a predicate's are read after it, and only each
// kept row's value is appended, where every row's was copied into a column allocated
// for all of them, and the kept ones copied again by the take. keeps says whether
// this chunk can be: one read from its own pages, of no struct.
type keptReader interface {
	keeps() bool
	// readKept reads the next len(keep) rows and appends those keep keeps, sel
	// their positions, ascending; it returns the rows read, fewer only at the end of
	// the chunk.
	readKept(keep []bool, sel []int32) (int, error)
}

func (c *fixedCol[P, T]) keeps() bool { return c.own != nil && c.parent == nil }

func (c *fixedCol[P, T]) readKept(keep []bool, sel []int32) (int, error) {
	n := len(keep)
	c.out = growExact(c.out, len(sel))
	var zero T
	rows, si := 0, 0
	for rows < n {
		var dst []P
		if c.direct {
			dst = any(c.out).([]P)
		} else {
			dst = c.kept[:0]
		}
		k, used, dst, lvl, err := c.own.keptSegment(n-rows, sel[si:], rows, dst, &c.vals)
		if err != nil {
			return 0, err
		}
		if k == 0 {
			break
		}
		switch {
		case lvl == nil && c.direct:
			c.out = any(dst).([]T)
			c.valid.appendValid(used)
		case lvl == nil:
			for _, v := range dst {
				c.out = append(c.out, c.conv(v))
			}
			c.kept = dst[:0]
			c.valid.appendValid(used)
		default:
			// A page with nulls: its present values are packed in c.vals, placed a
			// row at a time.
			kp, vals := keep[rows:rows+k], c.vals
			vi := 0
			var word uint64
			w := 0
			for j := range k {
				present := lvl[j] == 1
				if kp[j] {
					if present {
						c.out = append(c.out, c.conv(vals[vi]))
						word |= 1 << uint(w)
					} else {
						c.out = append(c.out, zero)
					}
					if w++; w == 64 {
						c.valid.appendWord(word, 64)
						word, w = 0, 0
					}
				}
				if present {
					vi++
				}
			}
			if w > 0 {
				c.valid.appendWord(word, w)
			}
		}
		si += used
		rows += k
	}
	return rows, nil
}

func (c *byteArrayCol) keeps() bool { return c.pages.pageChunk != nil && c.parent == nil }

// readKept marks the rows without copying a string, as a predicate's column is
// marked, but with no table of its own, and copies the kept ones.
func (c *byteArrayCol) readKept(keep []bool, sel []int32) (int, error) {
	got, err := c.pages.mark(len(keep), keep, nil, 0, c.dt)
	if err != nil {
		return 0, err
	}
	c.pages.emit(c, sel)
	return got, nil
}

func ownFixed[P int32 | int64 | float32 | float64, T data.Fixed](src pageFixed[P], maxDef int16,
	dt dtype.DataType, conv func(P) T) colReader {
	_, direct := any([]P(nil)).([]T)
	return &fixedCol[P, T]{own: src, maxDef: maxDef, dt: dt, conv: conv, direct: direct}
}

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
	if err := batchErr(c.cr, total, err); err != nil {
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

// byteArrayCol is bytearray.go; the list and struct readers are nested.go.
