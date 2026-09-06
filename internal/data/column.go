// Package data holds ursus's runtime column representation.
//
// # Column is contiguous, chunking lives above it
//
// A Column is a single flat run of values, not a chunked array. Every kernel
// therefore takes a plain []T and never has to reason about chunk boundaries —
// which removes an entire category of off-by-one bug from the layer where such
// bugs are most expensive.
//
// Chunking is not lost, it is moved up: a query result is a sequence of Batches,
// so concatenation is still O(1) at the level where concatenation happens. The
// cost of this choice is one file (the evaluator) if we ever need a chunked
// column, and the benefit is that ~100 kernels stay simple. That trade is
// obviously right at this stage and worth revisiting only with a benchmark.
//
// # Ownership
//
// Columns are immutable once built and safe to share between goroutines. There is
// no Release: see internal/arrowx for why Arrow's refcounting is advisory under
// the Go allocator.
package data

import (
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/arrowx"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/uerr"
)

// Primitive is the set of Go types that back a fixed-width column AND support
// Go's comparison and arithmetic operators.
//
// Generic kernels that write `a < b` or `a + b` must constrain to Primitive.
type Primitive interface {
	~int8 | ~int16 | ~int32 | ~int64 |
		~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// Fixed is every type that occupies a constant number of bytes in a values
// buffer — Primitive plus Int128.
//
// The split exists because Int128 is a STRUCT. It is 16 bytes with no pointers, so
// it stores exactly like any other numeric type and Values/NewFixed/Take work on it
// unchanged; but `a < b` and `a + b` do not compile for it, so anything using an
// operator needs Primitive instead and reaches Int128 through i128's methods.
//
// Go's type system enforces this: adding Int128 to a constraint whose functions use
// operators is a compile error, not a runtime surprise.
type Fixed interface {
	Primitive | i128.Int128
}

// Column is one named, typed, nullable run of values.
//
// Exactly one payload representation is populated, determined by dt:
//
//	fixed-width types  → fixed        (a flat values buffer)
//	Bool               → bits         (a packed bitmap, NOT one byte per value)
//	String, Binary     → offs + chars (int32 offsets plus a character buffer)
//	List               → offs + child (the same offsets, into a child COLUMN)
//	Struct             → fields       (N COLUMNS, no payload buffer at all)
//
// Bool having a distinct representation is not an accident of implementation: a
// Boolean column has TWO bitmaps, values and validity, and keeping them the same
// shape is what makes `null > 5` expressible as "value bit 0, validity bit 0".
//
// List reuses offs rather than inventing a second offsets field, so the n+1
// entries with a leading zero — and everything that already knows that shape,
// Slice included — carry over unchanged. What differs is only what the offsets
// index INTO: a flat character buffer for String, a whole Column for List, which
// is what lets the element be any type the child can be, nested lists included.
//
// A Struct's children are a DIFFERENT relationship and get their own field. A list
// has one child of some other length that its offsets index into; a struct has N
// children each exactly as long as the struct, addressed by the same row number and
// sharing one validity bit. Storing both in one slice would mean every use site
// needed a comment saying which meaning applied.
type Column struct {
	name  string
	dt    dtype.DataType
	len   int
	valid bitmap.View

	fixed  *memory.Buffer // fixed-width payload
	bits   bitmap.View    // Bool payload
	offs   *memory.Buffer // []int32, len+1 entries
	chars  *memory.Buffer // String/Binary character data
	child  *Column        // List elements; offs indexes into this
	fields []*Column      // Struct fields, each of length len
}

// Name returns the column name.
func (c *Column) Name() string { return c.name }

// DType returns the logical type.
func (c *Column) DType() dtype.DataType { return c.dt }

// Len returns the number of values.
func (c *Column) Len() int { return c.len }

// Validity returns the validity bitmap. A set bit means valid (non-null).
func (c *Column) Validity() bitmap.View { return c.valid }

// NullCount returns the number of nulls.
func (c *Column) NullCount() int { return c.valid.CountUnset() }

// IsValid reports whether row i is non-null.
func (c *Column) IsValid(i int) bool { return c.valid.Get(i) }

// IsPayloadFree reports whether the column carries no payload buffer at all.
//
// Only NewNull produces one, and it is a genuine value — "which makes a typed null
// literal free" — but it can only ever be an ANSWER, never an OPERAND. Values
// refuses it, StringAccessor.Get panics on it, and both are reachable from ordinary
// queries; kernel.NullColumn exists to build the operand-shaped equivalent.
//
// That rule was previously enforced only by remembering it. A kernel handed a
// column from an unknown source can now ask.
func (c *Column) IsPayloadFree() bool {
	return c.fixed == nil && c.offs == nil && c.bits.Len() == 0 &&
		c.child == nil && c.fields == nil
}

// Rename returns a copy with a new name. O(1): the payload is shared.
func (c *Column) Rename(name string) *Column {
	d := *c
	d.name = name
	return &d
}

// WithDType returns a copy reinterpreted as a different logical type with the
// same physical layout — Int64 to Datetime, say. It does NOT convert values.
func (c *Column) WithDType(dt dtype.DataType) *Column {
	d := *c
	d.dt = dt
	return &d
}

// --- construction ------------------------------------------------------------

// NewFixed builds a fixed-width column over vals.
//
// The values slice must alias a buffer obtained from arrowx so that alignment and
// lifetime are right; NewFixedBuffer is the lower-level entry point.
func NewFixed[T Fixed](name string, dt dtype.DataType, vals []T, valid bitmap.View) *Column {
	buf := arrowx.NewBuffer(len(vals) * int(sizeOf[T]()))
	copy(unsafeData[T](buf.Bytes()), vals)
	return NewFixedBuffer(name, dt, buf, len(vals), valid)
}

// NewFixedBuffer builds a fixed-width column directly over an existing buffer.
// This is the path a kernel takes: allocate the output buffer, write into it, and
// wrap it with no copy.
func NewFixedBuffer(name string, dt dtype.DataType, buf *memory.Buffer, n int, valid bitmap.View) *Column {
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dt, len: n, valid: valid, fixed: buf}
}

// NewBool builds a Boolean column from a packed payload bitmap.
func NewBool(name string, bits bitmap.View, valid bitmap.View) *Column {
	n := bits.Len()
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dtype.Bool, len: n, valid: valid, bits: bits}
}

// NewString builds a String column from Go strings.
func NewString(name string, vals []string, valid bitmap.View) *Column {
	total := 0
	for _, s := range vals {
		total += len(s)
	}
	offs := arrowx.NewBuffer((len(vals) + 1) * 4)
	chars := arrowx.NewBuffer(total)

	o := unsafeData[int32](offs.Bytes())
	d := chars.Bytes()
	pos := int32(0)
	for i, s := range vals {
		o[i] = pos
		copy(d[pos:], s)
		pos += int32(len(s))
	}
	o[len(vals)] = pos

	if valid.Len() == 0 && len(vals) > 0 {
		valid = bitmap.AllSet(len(vals))
	}
	return &Column{
		name: name, dt: dtype.String, len: len(vals),
		valid: valid, offs: offs, chars: chars,
	}
}

// NewStringParts builds a String column from an offsets slice and a character
// slice that a reader has already accumulated.
//
// It exists so a decoder can build Arrow's layout DIRECTLY instead of going
// through []string. The Parquet reader used to append one Go string per value —
// one heap allocation per row, 2.14M of them for a two-string-column scan of a
// million rows — and then hand the whole slice to NewString, which copied every
// byte a second time. Both costs vanish if the decoder appends into offs and
// chars as it goes, which is what this constructor is for.
//
// offs must have n+1 entries with offs[0] == 0 and offs[n] == len(chars), the
// ordinary Arrow invariant. The int32 write goes through unsafeData rather than
// encoding/binary because the buffer is native-endian, which is what every reader
// of it assumes.
func NewStringParts(name string, offs []int32, chars []byte, valid bitmap.View) *Column {
	n := len(offs) - 1
	if n < 0 {
		n = 0
	}
	ob := arrowx.NewBuffer(len(offs) * 4)
	cb := arrowx.NewBuffer(len(chars))
	copy(unsafeData[int32](ob.Bytes()), offs)
	copy(cb.Bytes(), chars)

	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dtype.String, len: n, valid: valid, offs: ob, chars: cb}
}

// NewStringBuffers builds a String column over existing offset and character
// buffers, without copying.
func NewStringBuffers(name string, offs, chars *memory.Buffer, n int, valid bitmap.View) *Column {
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dtype.String, len: n, valid: valid, offs: offs, chars: chars}
}

// NewList builds a List column from offsets and a child column holding every
// element of every row, concatenated.
//
// offs must have n+1 entries with offs[0] == 0 and offs[n] == child.Len() — the
// ordinary Arrow invariant, and the same one NewStringParts documents, because
// this is the same offsets buffer pointing at a different kind of payload.
//
// # An empty list and a null list are not the same row
//
// Both leave the offset unmoved: offs[i] == offs[i+1] either way. Only the
// validity bit tells them apart, exactly as it does for an empty string against a
// null string. Getting it wrong is not a crash — it is one row quietly holding
// the wrong answer, and the failure surfaces wherever the row is finally read
// rather than where it was built.
//
// The type is DERIVED from the child rather than passed in. A List whose declared
// element type disagrees with the column actually holding the elements would be a
// lie no caller could detect, and there is no case where the two should differ.
func NewList(name string, offs []int32, child *Column, valid bitmap.View) *Column {
	n := len(offs) - 1
	if n < 0 {
		n = 0
	}
	ob := arrowx.NewBuffer(len(offs) * 4)
	copy(unsafeData[int32](ob.Bytes()), offs)

	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{
		name:  name,
		dt:    dtype.List(child.DType()),
		len:   n,
		valid: valid,
		offs:  ob,
		child: child,
	}
}

// Child returns a List column's element column, or nil.
//
// The result is the elements of EVERY row concatenated; row i occupies
// child[offs[i]:offs[i+1]]. Use Lists to walk it without doing that arithmetic by
// hand.
func (c *Column) Child() *Column { return c.child }

// Lists returns an accessor over a List column's rows.
func (c *Column) Lists() ListAccessor {
	return ListAccessor{offs: c.RawOffsets(), child: c.child, valid: c.valid, n: c.len}
}

// ListAccessor reads one row's element range at a time.
//
// It returns a RANGE rather than a sliced column because slicing per row would
// allocate per row, and every caller so far — rendering, Take, Concat — wants the
// bounds so it can copy a span in one move.
type ListAccessor struct {
	offs  []byte
	child *Column
	valid bitmap.View
	n     int
}

// Get returns the half-open element range for row i, and whether the row is
// non-null.
//
// The range is ALWAYS the real one, including for a null row — where it is empty,
// because a null list consumes no elements and leaves the offset unmoved. That
// matters to any caller walking a column end to end: returning a zeroed range for
// a null row would collapse every offset after it, and Concat did exactly that
// before this note existed.
//
// So a present-but-empty list and a null list return the SAME range, and `ok` is
// the only thing that separates them. That is the distinction, not an accident.
func (a ListAccessor) Get(i int) (start, end int32, ok bool) {
	o := unsafeData[int32](a.offs)
	return o[i], o[i+1], a.valid.Get(i)
}

// Child is the column the ranges index into.
func (a ListAccessor) Child() *Column { return a.child }

// NewStruct builds a Struct column from its field columns.
//
// Every field must be as long as the struct itself: a struct row IS row i of each
// field, and the struct's own validity is the only thing they share. That is not
// checked here — the constructors do not validate — but it is the invariant every
// reader below relies on.
//
// # A null struct is not a struct of nulls
//
// The two hold identical field values (none) and differ only in this column's
// validity bit, exactly as an empty list differs from a null list. It matters
// because the difference cannot be recovered afterwards: a struct whose fields all
// happen to be null is a REAL struct, and inferring "null struct" from "every field
// is null" turns it into an absent one, silently and for one row in a fixture that
// does not deliberately contain it.
//
// The type is DERIVED from the fields, for the reason NewList gives: a declared type
// that disagreed with the columns actually holding the values would be a lie no
// caller could detect.
func NewStruct(name string, fields []*Column, valid bitmap.View) *Column {
	n := 0
	if len(fields) > 0 {
		n = fields[0].Len()
	}
	fs := make([]dtype.Field, len(fields))
	for i, f := range fields {
		fs[i] = dtype.Field{Name: f.Name(), Type: f.DType(), Nullable: true}
	}
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{
		name:   name,
		dt:     dtype.Struct(fs...),
		len:    n,
		valid:  valid,
		fields: fields,
	}
}

// Fields returns a Struct column's field columns, or nil.
//
// The result aliases the column's own slice; callers must not reorder it.
func (c *Column) Fields() []*Column { return c.fields }

// Field returns the field column with this name, and whether there is one.
func (c *Column) Field(name string) (*Column, bool) {
	for _, f := range c.fields {
		if f.name == name {
			return f, true
		}
	}
	return nil, false
}

// NewNull builds an all-null column of the given type and length. It carries no
// payload buffer, which makes a typed null literal free.
func NewNull(name string, dt dtype.DataType, n int) *Column {
	return &Column{name: name, dt: dt, len: n, valid: bitmap.Zeros(n)}
}

// --- typed access ------------------------------------------------------------

// Values returns the column's fixed-width payload reinterpreted as []T.
//
// Zero-copy: the slice aliases the column's buffer. It must not be mutated —
// columns are shared.
//
// T must match the column's PHYSICAL type: a Datetime column is read as []int64,
// a Date column as []int32. Reading through the physical type is what lets one
// integer kernel serve every type that stores integers.
func Values[T Fixed](c *Column) ([]T, error) {
	if c.fixed == nil {
		return nil, uerr.New(uerr.KindType, "",
			"column %q (%s) has no fixed-width payload", c.name, c.dt)
	}

	// Check the exact physical type, NOT just the width.
	//
	// Comparing bit widths is not enough and the difference is not academic:
	// Int64 and Float64 are both 64 bits, so a width-only check lets
	// Values[float64] succeed on an Int64 column and hand back the integer bit
	// patterns reinterpreted as floats. The values look like 5e-324 — plausible
	// enough to be mistaken for a real computation, which is the worst kind of
	// wrong. Reading a Datetime column as []int64 still works, because Physical()
	// maps it to Int64 first; that is the intended punning and this check permits
	// exactly it and nothing else.
	want, ok := physicalID[T]()
	if !ok {
		return nil, uerr.Internalf("data: no physical type for the requested Go type")
	}
	if got := c.dt.Physical().ID(); got != want {
		return nil, uerr.New(uerr.KindType, "",
			"column %q is %s (physical %s) and cannot be read as %s",
			c.name, c.dt, got, want)
	}
	return unsafeData[T](c.fixed.Bytes())[:c.len], nil
}

// physicalID maps a Go storage type to the TypeID it represents.
func physicalID[T Fixed]() (dtype.TypeID, bool) {
	var z T
	switch any(z).(type) {
	case int8:
		return dtype.TypeInt8, true
	case int16:
		return dtype.TypeInt16, true
	case int32:
		return dtype.TypeInt32, true
	case int64:
		return dtype.TypeInt64, true
	case uint8:
		return dtype.TypeUint8, true
	case uint16:
		return dtype.TypeUint16, true
	case uint32:
		return dtype.TypeUint32, true
	case uint64:
		return dtype.TypeUint64, true
	case float32:
		return dtype.TypeFloat32, true
	case float64:
		return dtype.TypeFloat64, true
	case i128.Int128:
		return dtype.TypeInt128, true
	default:
		return 0, false
	}
}

// Reinterpret exposes a buffer as a typed slice. Used by kernels that allocate an
// output buffer and write into it directly.
func Reinterpret[T Fixed](b []byte) []T { return unsafeData[T](b) }

// MustValues is Values for call sites that have already checked the dtype.
func MustValues[T Fixed](c *Column) []T {
	v, err := Values[T](c)
	if err != nil {
		panic(err)
	}
	return v
}

// Bools returns the payload bitmap of a Boolean column.
func (c *Column) Bools() bitmap.View { return c.bits }

// Strings returns an accessor for a String or Binary column.
func (c *Column) Strings() StringAccessor {
	if c.offs == nil {
		return StringAccessor{}
	}
	return StringAccessor{
		offsets: unsafeData[int32](c.offs.Bytes())[: c.len+1 : c.len+1],
		chars:   c.chars.Bytes(),
	}
}

// StringAccessor reads a variable-width column.
//
// It is a struct rather than an interface so that the layout choice is resolved
// once per batch instead of once per value. When Arrow's StringView layout is
// added it becomes a tagged union here, and no kernel call site changes.
type StringAccessor struct {
	offsets []int32
	chars   []byte
}

// Get returns row i without copying: the result aliases the character buffer.
func (a StringAccessor) Get(i int) string {
	lo, hi := a.offsets[i], a.offsets[i+1]
	return unsafeString(a.chars[lo:hi])
}

// Len returns the number of values.
func (a StringAccessor) Len() int {
	if len(a.offsets) == 0 {
		return 0
	}
	return len(a.offsets) - 1
}

// --- raw payload access ------------------------------------------------------
//
// These exist for ONE caller: the spill format, which has to write a column out
// and read it back byte-exactly, including values a typed accessor would
// normalise away. Everything else should use Values, Bools or Strings, which
// check the type instead of trusting the caller.

// RawFixed returns the fixed-width payload as bytes, or nil if there is none.
// The slice aliases the column's buffer and must not be mutated.
func (c *Column) RawFixed() []byte {
	if c.fixed == nil {
		return nil
	}
	return c.fixed.Bytes()
}

// RawOffsets returns a String or Binary column's offset buffer as bytes.
// Read it as []int32 with Reinterpret; it holds len+1 entries.
func (c *Column) RawOffsets() []byte {
	if c.offs == nil {
		return nil
	}
	return c.offs.Bytes()
}

// RawChars returns a String or Binary column's character buffer.
//
// Note it is the WHOLE buffer: a sliced column keeps the original characters and
// re-windows the offsets, so offsets[0] is not necessarily zero and the live
// range is offsets[0]:offsets[len].
func (c *Column) RawChars() []byte {
	if c.chars == nil {
		return nil
	}
	return c.chars.Bytes()
}

// --- slicing -----------------------------------------------------------------

// Slice returns rows [offset, offset+length) as a zero-copy view.
//
// For fixed-width columns this needs a real offset on the values buffer, which the
// current representation does not carry, so fixed-width slicing copies. Bool and
// String slicing is genuinely O(1). This asymmetry is deliberate: Slice is not on
// any hot path in v0.1 (Filter uses Take, which must copy anyway), and adding an
// offset field to Column would put a `+ c.offset` in every kernel's inner loop to
// buy nothing today.
func (c *Column) Slice(offset, length int) *Column {
	if offset < 0 || length < 0 || offset+length > c.len {
		panic("data: Column.Slice out of range")
	}
	d := &Column{
		name:  c.name,
		dt:    c.dt,
		len:   length,
		valid: c.valid.Slice(offset, length),
	}
	switch {
	case c.bits.Len() > 0:
		d.bits = c.bits.Slice(offset, length)
	case c.offs != nil:
		// Offsets are relative to the payload, so a slice can keep the same payload
		// and re-window the offsets. That holds whether the payload is a character
		// buffer or a child column: the offsets are not rebased, so the child stays
		// whole and the slice stays O(1).
		o := unsafeData[int32](c.offs.Bytes())
		nb := arrowx.NewBuffer((length + 1) * 4)
		no := unsafeData[int32](nb.Bytes())
		copy(no, o[offset:offset+length+1])
		d.offs = nb
		d.chars = c.chars
		d.child = c.child
	case c.fields != nil:
		// A struct has no payload buffer of its own, so without this arm it would fall
		// through every case and produce a column of the right length holding nothing.
		// Each field is as long as the struct, so slicing them at the same window is
		// the whole operation.
		d.fields = make([]*Column, len(c.fields))
		for i, f := range c.fields {
			d.fields[i] = f.Slice(offset, length)
		}

	case c.fixed != nil:
		width := c.dt.Physical().BitWidth() / 8
		nb := arrowx.NewBuffer(length * width)
		copy(nb.Bytes(), c.fixed.Bytes()[offset*width:(offset+length)*width])
		d.fixed = nb
	}
	return d
}

func sizeOf[T Fixed]() uintptr {
	var z T
	return unsafeSizeof(z)
}
