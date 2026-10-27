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
	"encoding/binary"
	"math"
	"slices"

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
//	String, Binary     → offs + chars (int32 offsets plus a character buffer),
//	                     or offs64 + chars past MaxStringBytes (step 368)
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
	offs64 []int64        // len+1 entries, in offs' place past MaxStringBytes
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
	return c.fixed == nil && c.offs == nil && c.offs64 == nil && c.bits.Len() == 0 &&
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

// WithValidity returns a copy with a different validity bitmap and the same payload.
//
// The values are untouched, so a row that becomes null keeps whatever was stored in
// its slot — which is exactly the contract every constructor here already has, since
// a null row's payload is never read.
//
// Unnest is the caller: a field of an absent struct is absent, and that has to be
// applied to the field on its way out rather than assumed of whoever built the
// struct.
func (c *Column) WithValidity(valid bitmap.View) *Column {
	d := *c
	d.valid = valid
	return &d
}

// --- construction ------------------------------------------------------------

// NewFixed builds a fixed-width column over vals.
//
// The values slice must alias a buffer obtained from arrowx so that alignment and
// lifetime are right; NewFixedBuffer is the lower-level entry point.
//
// # T must be dt's physical type, and that used to go unchecked
//
// Values checks on the way OUT — its doc spells out why a width-only check is not
// enough: "Int64 and Float64 are both 64 bits, so a width-only check lets
// Values[float64] succeed on an Int64 column". Nothing checked on the way IN, and
// that asymmetry cost a whole defect: a cumulative sum handed this a []float64 under
// a Duration dtype, and because the two are the same width, neither the dtype nor the
// row count could disagree. One hour read back as about 152 years.
//
// A panic rather than an error for the reason NewBool gives for the same shape of
// violation: this signature has no error to return, and a caller cannot handle it —
// values of the wrong type are not a condition, they are a bug in whoever built them.
// The pun this does NOT forbid is the legitimate one: Physical() maps Date to Int32,
// the temporal types to Int64, Enum to Uint32 and Decimal to Int128, so every column
// stored as a different type than it declares still passes.
func NewFixed[T Fixed](name string, dt dtype.DataType, vals []T, valid bitmap.View) *Column {
	// physicalID is the same helper Values consults on the way out; this is the
	// matching check on the way in. A Go type it does not know — a DEFINED type over
	// a primitive, which the Fixed constraint admits with its tildes — is let
	// through rather than refused, because its width is all that is knowable and
	// convertNamedSlice already converts those before they reach here.
	if want, ok := physicalID[T](); ok && want != dt.Physical().ID() {
		panic(uerr.Internalf(
			"data: column %q declares %s, stored as %s, but its values are %s",
			name, dt, dt.Physical().ID(), want))
	}
	buf := arrowx.NewBuffer(len(vals) * int(sizeOf[T]()))
	copy(unsafeData[T](buf.Bytes()), vals)
	return NewFixedBuffer(name, dt, buf, len(vals), valid)
}

// NewFixedOwned is NewFixed taking vals rather than copying it (step 163): the
// column wraps vals' allocation, all of it, and the caller hands vals over and never
// writes it again. A reader that decodes a batch's values into a fresh slice uses it,
// so the values are written once.
func NewFixedOwned[T Fixed](name string, dt dtype.DataType, vals []T, valid bitmap.View) *Column {
	if want, ok := physicalID[T](); ok && want != dt.Physical().ID() {
		panic(uerr.Internalf(
			"data: column %q declares %s, stored as %s, but its values are %s",
			name, dt, dt.Physical().ID(), want))
	}
	return NewFixedBuffer(name, dt, ownedBuffer(vals), len(vals), valid)
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
//
// # The length comes from the payload, and a disagreeing validity is a panic
//
// It used to be taken from the payload SILENTLY. `NewBool(name, bitmap.Not(c.Bools()),
// c.Validity())` over a Null column handed this a zero-length payload and an n-bit
// validity, and got back a Bool column of length 0 carrying n validity bits — a
// structurally impossible value, returned with no error, from a kernel that had
// satisfied its type contract. It surfaced three layers away as "expression produced
// 0 rows for a 3-row batch; this is a bug in ursus".
//
// Two integer compares against the cost of building the bitmap, so there is no case
// for hiding this behind CheckNonNullable's debug flag. A panic rather than a
// clamp for the reason NewBatchRows gives for the same shape of violation: this
// signature has no error to return, and the caller cannot handle it — two buffers
// of different lengths are not a condition, they are a bug in whoever built them.
func NewBool(name string, bits bitmap.View, valid bitmap.View) *Column {
	n := bits.Len()
	if valid.Len() != 0 && valid.Len() != n {
		panic(uerr.Internalf(
			"data: bool column %q has a %d-bit payload and a %d-bit validity bitmap",
			name, n, valid.Len()))
	}
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dtype.Bool, len: n, valid: valid, bits: bits}
}

// MaxStringBytes is the most character data a String column of 32-bit offsets holds.
// Past it they wrapped silently, reading other rows' bytes or slicing at a negative
// offset (audit.md S24). Since step 368 a column built whole, by NewString or by the
// combiners every Collect, join and sort ends in (ConcatStrings, TakeStrings), takes
// 64-bit offsets past it instead, and a column built from a reader's 32-bit offsets
// refuses. A variable only so a test can lower it.
var MaxStringBytes int64 = math.MaxInt32

// tooManyChars is raised by a constructor of 32-bit offsets asked for more than they
// hold. The constructors return no error, and every boundary recovers a panic, so
// this is how a batch of more than 2 GiB of text fails instead of wrapping.
func tooManyChars(name string, n int64) {
	uerr.Raise(uerr.New(uerr.KindResource, "string",
		"column %q needs %d bytes of characters in one batch, and a batch's String column holds %d", name, n,
		MaxStringBytes).
		Hint("its offsets are 32-bit; read it in smaller batches"))
}

// offsetWidth is an offset's type, 32 or 64 bits.
type offsetWidth interface{ ~int32 | ~int64 }

// offsets are a String or Binary column's offsets, one of the two widths, or neither
// where it has no payload.
func (c *Column) offsets() ([]int32, []int64) {
	switch {
	case c.offs != nil:
		return unsafeData[int32](c.offs.Bytes())[:c.len+1], nil
	case c.offs64 != nil:
		return nil, c.offs64
	}
	return nil, nil
}

// charSpan is the range of c's characters its rows cover, and false where it has
// no payload.
func (c *Column) charSpan() (lo, hi int64, ok bool) {
	o32, o64 := c.offsets()
	switch {
	case o32 != nil:
		return int64(o32[0]), int64(o32[c.len]), true
	case o64 != nil:
		return o64[0], o64[c.len], true
	}
	return 0, 0, false
}

// stringBuffers allocates a column's offsets of n rows and chars characters: 32-bit
// up to MaxStringBytes, 64-bit past it.
func stringBuffers(n int, chars int64) (offs *memory.Buffer, offs64 []int64, cb *memory.Buffer) {
	if chars > MaxStringBytes {
		offs64 = make([]int64, n+1)
	} else {
		offs = arrowx.NewBuffer((n + 1) * 4)
	}
	return offs, offs64, arrowx.NewBuffer(int(chars))
}

// MaxListElements is the most elements one List column holds. Its offsets are
// 32-bit, as a String's are, and past this they wrapped silently: two one-row lists
// of 2^30 elements concatenated to a second row spanning [2^30, -2^31), with no
// error (step 99). A variable only so a test can lower it.
var MaxListElements int64 = math.MaxInt32

// tooManyElements is tooManyChars for a List column.
func tooManyElements(name string, n int64) {
	uerr.Raise(uerr.New(uerr.KindResource, "list",
		"column %q needs %d list elements, and a List column holds %d", name, n,
		MaxListElements).
		Hint("its offsets are 32-bit; read the result in batches with CollectBatches, or " +
			"write it with SinkParquet, which never build one column of all of it"))
}

// NewString builds a String column from Go strings.
func NewString(name string, vals []string, valid bitmap.View) *Column {
	var total int64
	for _, s := range vals {
		total += int64(len(s))
	}
	offs, offs64, chars := stringBuffers(len(vals), total)
	if offs != nil {
		fillStrings(unsafeData[int32](offs.Bytes()), chars.Bytes(), vals)
	} else {
		fillStrings(offs64, chars.Bytes(), vals)
	}
	if valid.Len() == 0 && len(vals) > 0 {
		valid = bitmap.AllSet(len(vals))
	}
	return &Column{
		name: name, dt: dtype.String, len: len(vals),
		valid: valid, offs: offs, offs64: offs64, chars: chars,
	}
}

func fillStrings[O offsetWidth](o []O, d []byte, vals []string) {
	pos := O(0)
	for i, s := range vals {
		o[i] = pos
		copy(d[pos:], s)
		pos += O(len(s))
	}
	o[len(vals)] = pos
}

// ConcatStrings builds one String or Binary column of type dt from parts, end to
// end, and validity valid: each part's live characters copied once, its offsets
// shifted by the characters before it.
//
// Every Collect ends in one of these per String column, its rows copied on the
// query's threads since step 389 (ConcatStringsWith). It used to build a Go string per
// row and hand the slice to NewString, which copied every byte a
// second time: 0.37 s of h2o j3's 0.58 s at 2M rows (step 101's profile).
//
// A part is a derived column as often as not — Column.Slice keeps the whole
// character buffer and re-windows the offsets — so what it contributes is the range
// its own offsets cover, not its buffer.
//
// Past MaxStringBytes its offsets are 64-bit (step 368), whatever its parts' are.
func ConcatStrings(name string, dt dtype.DataType, parts []*Column, valid bitmap.View) *Column {
	return ConcatStringsWith(name, dt, parts, valid, Serial)
}

// Spread runs fn over pieces of [0, n) that cover it, one after another or at once.
type Spread func(n int, fn func(lo, hi int))

// Serial is the Spread of one piece, run by the caller.
func Serial(n int, fn func(lo, hi int)) { fn(0, n) }

// ConcatStringsWith is ConcatStrings with its rows copied by spread, a piece of them
// at a time: where each part's rows and characters land is known before any is copied
// (step 389).
func ConcatStringsWith(name string, dt dtype.DataType, parts []*Column, valid bitmap.View, spread Spread) *Column {
	n, chars := 0, int64(0)
	for _, p := range parts {
		n += p.len
		if lo, hi, ok := p.charSpan(); ok && p.len > 0 {
			chars += hi - lo
		}
	}
	// Counted before anything is allocated, as NewString counts.
	ob, ob64, cb := stringBuffers(n, chars)
	if ob != nil {
		concatInto(unsafeData[int32](ob.Bytes()), cb.Bytes(), parts, spread)
	} else {
		concatInto(ob64, cb.Bytes(), parts, spread)
	}
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dt, len: n, valid: valid, offs: ob, offs64: ob64, chars: cb}
}

// concatInto writes parts' offsets, rebased, into offs, and their characters into out,
// spread's pieces of rows each by itself.
func concatInto[O offsetWidth](offs []O, out []byte, parts []*Column, spread Spread) {
	// Each part's first row and first character in the result.
	rows, at := make([]int, len(parts)+1), make([]O, len(parts)+1)
	for k, p := range parts {
		rows[k+1], at[k+1] = rows[k]+p.len, at[k]
		if lo, hi, ok := p.charSpan(); ok && p.len > 0 {
			at[k+1] += O(hi - lo)
		}
	}
	spread(rows[len(parts)], func(lo, hi int) {
		for k := PartAt(rows, lo); k < len(parts) && rows[k] < hi; k++ {
			p := parts[k]
			a, b := max(lo, rows[k])-rows[k], min(hi, rows[k+1])-rows[k]
			if a >= b {
				continue
			}
			switch o32, o64 := p.offsets(); {
			case o32 != nil:
				appendPart(offs, out, rows[k]+a, at[k]+O(o32[a]-o32[0]), o32[a:b+1], p.chars.Bytes())
			case o64 != nil:
				appendPart(offs, out, rows[k]+a, at[k]+O(o64[a]-o64[0]), o64[a:b+1], p.chars.Bytes())
			default:
				// A column with no payload — every row null — holds no characters.
				for r := rows[k] + a; r < rows[k]+b; r++ {
					offs[r] = at[k]
				}
			}
		}
	})
	offs[rows[len(parts)]] = at[len(parts)]
}

// PartAt is the part row lo falls in, of parts whose first rows are rows, the last
// entry their total: the first whose end is past lo.
func PartAt(rows []int, lo int) int {
	k, _ := slices.BinarySearch(rows[1:], lo+1)
	return k
}

// appendPart appends one part's rows, its offsets o, at row and pos.
func appendPart[O, I offsetWidth](offs []O, out []byte, row int, pos O, o []I, chars []byte) (int, O) {
	n := len(o) - 1
	lo, hi := o[0], o[n]
	copy(out[pos:], chars[lo:hi])
	for i := range n {
		offs[row] = pos + O(o[i]-lo)
		row++
	}
	return row, pos + O(hi-lo)
}

// TakeStrings gathers a String or Binary column by row: row j of the result is row
// sel[j] of c, and a negative index is an empty row, which valid marks null. Each
// row's bytes are copied once, into one buffer counted before it is allocated.
//
// Past MaxStringBytes its offsets are 64-bit (step 368), whatever c's are.
func TakeStrings(c *Column, sel []int32, valid bitmap.View) *Column {
	n := len(sel)
	o32, o64 := c.offsets()
	chars := int64(0)
	switch {
	case o32 != nil:
		chars = countChars(o32, sel)
	case o64 != nil:
		chars = countChars(o64, sel)
	}
	ob, ob64, cb := stringBuffers(n, chars)
	var src []byte
	if c.chars != nil {
		src = c.chars.Bytes()
	}
	switch {
	case ob != nil && o64 == nil:
		takeInto(unsafeData[int32](ob.Bytes()), cb.Bytes(), sel, o32, src)
	case ob != nil:
		takeInto(unsafeData[int32](ob.Bytes()), cb.Bytes(), sel, o64, src)
	case o64 == nil:
		takeInto(ob64, cb.Bytes(), sel, o32, src)
	default:
		takeInto(ob64, cb.Bytes(), sel, o64, src)
	}
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: c.name, dt: c.dt, len: n, valid: valid, offs: ob, offs64: ob64, chars: cb}
}

// countChars is the characters of rows sel, a negative row none.
func countChars[I offsetWidth](o []I, sel []int32) int64 {
	chars := int64(0)
	for _, i := range sel {
		if i >= 0 {
			chars += int64(o[i+1] - o[i])
		}
	}
	return chars
}

// takeInto writes rows sel of o and src into offs and out, from the start of both;
// o nil is a column of no payload, every row empty.
//
// A run of at least four consecutive rows, as a join's probe side gathers its matches
// in order, is copied at once and its offsets shifted; a lone row of at most sixteen
// bytes is moved as two words where both buffers have room past it, which takes no
// branch on its length. Every row was a copy call of its own: a tenth of h2o j1's
// time over Parquet (step 379, J1 of v0.7-scope.md).
func takeInto[O, I offsetWidth](offs []O, out []byte, sel []int32, o []I, src []byte) {
	pos := O(0)
	if o == nil {
		clear(offs[:len(sel)+1])
		return
	}
	n := len(sel)
	for j := 0; j < n; {
		i := sel[j]
		offs[j] = pos
		if i < 0 {
			j++
			continue
		}
		if j+3 < n && sel[j+3] == i+3 && sel[j+1] == i+1 {
			k := j + 2
			for k < n && sel[k] == sel[k-1]+1 {
				k++
			}
			lo, hi := int(o[i]), int(o[int(sel[k-1])+1])
			for t, v := range o[i : int(i)+k-j] {
				offs[j+t] = pos + O(int(v)-lo)
			}
			copy(out[pos:], src[lo:hi])
			pos += O(hi - lo)
			j = k
			continue
		}
		lo, hi := int(o[i]), int(o[i+1])
		if hi-lo <= 16 && lo+16 <= len(src) && int(pos)+16 <= len(out) {
			binary.LittleEndian.PutUint64(out[pos:], binary.LittleEndian.Uint64(src[lo:]))
			binary.LittleEndian.PutUint64(out[pos+8:], binary.LittleEndian.Uint64(src[lo+8:]))
		} else {
			copy(out[pos:], src[lo:hi])
		}
		pos += O(hi - lo)
		j++
	}
	offs[n] = pos
}

// TakeStringsChunked is TakeStrings of a long selection, a chunk of rows at a time,
// the chunks run by run, which may run them at once: each chunk's bytes are counted
// first, which places every chunk's in the one answer, and then each chunk writes
// its own offsets and bytes.
func TakeStringsChunked(c *Column, sel []int32, valid bitmap.View, chunk int,
	run func(n int, fn func(k int) error) error) (*Column, error) {
	n := len(sel)
	chunks := (n + chunk - 1) / chunk
	o32, o64 := c.offsets()
	start := make([]int64, chunks+1)
	if o32 != nil || o64 != nil {
		err := run(chunks, func(k int) error {
			part := sel[k*chunk : min(n, (k+1)*chunk)]
			if o32 != nil {
				start[k+1] = countChars(o32, part)
			} else {
				start[k+1] = countChars(o64, part)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for k := range chunks {
		start[k+1] += start[k]
	}
	ob, ob64, cb := stringBuffers(n, start[chunks])
	var src []byte
	if c.chars != nil {
		src = c.chars.Bytes()
	}
	err := run(chunks, func(k int) error {
		lo, hi := k*chunk, min(n, (k+1)*chunk)
		switch {
		case ob != nil && o64 == nil:
			takeChunk(unsafeData[int32](ob.Bytes()), cb.Bytes(), sel, lo, hi, start[k], o32, src)
		case ob != nil:
			takeChunk(unsafeData[int32](ob.Bytes()), cb.Bytes(), sel, lo, hi, start[k], o64, src)
		case o64 == nil:
			takeChunk(ob64, cb.Bytes(), sel, lo, hi, start[k], o32, src)
		default:
			takeChunk(ob64, cb.Bytes(), sel, lo, hi, start[k], o64, src)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if ob != nil {
		unsafeData[int32](ob.Bytes())[n] = int32(start[chunks])
	} else {
		ob64[n] = start[chunks]
	}
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: c.name, dt: c.dt, len: n, valid: valid, offs: ob, offs64: ob64, chars: cb}, nil
}

// takeChunk writes rows sel[lo:hi] of o and src into offs and out, from row lo and
// character at.
func takeChunk[O, I offsetWidth](offs []O, out []byte, sel []int32, lo, hi int, at int64, o []I, src []byte) {
	pos := O(at)
	for j := lo; j < hi; j++ {
		offs[j] = pos
		if i := sel[j]; i >= 0 && o != nil {
			a, b := o[i], o[i+1]
			copy(out[pos:], src[a:b])
			pos += O(b - a)
		}
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
// offs must have n+1 monotonic entries, and a column CONSTRUCTED here starts at zero
// with offs[n] == len(chars). That is a property of this constructor, not of every
// String column: a derived one — any Column.Slice — keeps its offsets absolute, so
// offs[0] may be non-zero. See ListAccessor.Window for why that sentence matters. The
// int32 write goes through unsafeData rather than encoding/binary because the buffer
// is native-endian, which is what every reader of it assumes.
func NewStringParts(name string, offs []int32, chars []byte, valid bitmap.View) *Column {
	// A reader computes offs as int32 while it appends, so past the limit they have
	// already wrapped; the characters themselves are what can be counted.
	if int64(len(chars)) > MaxStringBytes {
		tooManyChars(name, int64(len(chars)))
	}
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

// NewStringOwned is NewStringParts taking offs and chars rather than copying them
// (step 163); the caller hands both over and never writes them again.
func NewStringOwned(name string, offs []int32, chars []byte, valid bitmap.View) *Column {
	if int64(len(chars)) > MaxStringBytes {
		tooManyChars(name, int64(len(chars)))
	}
	n := max(len(offs)-1, 0)
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dtype.String, len: n, valid: valid,
		offs: ownedBuffer(offs), chars: ownedBuffer(chars)}
}

// NewStringBuffers builds a String column over existing offset and character
// buffers, without copying.
func NewStringBuffers(name string, offs, chars *memory.Buffer, n int, valid bitmap.View) *Column {
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dtype.String, len: n, valid: valid, offs: offs, chars: chars}
}

// NewStringBuffers64 is NewStringBuffers for 64-bit offsets, len(offs)-1 rows, which a
// column past MaxStringBytes holds (step 368): a spill file's, read back. It takes
// offs rather than copying them.
func NewStringBuffers64(name string, offs []int64, chars *memory.Buffer, valid bitmap.View) *Column {
	n := max(len(offs)-1, 0)
	if valid.Len() == 0 && n > 0 {
		valid = bitmap.AllSet(n)
	}
	return &Column{name: name, dt: dtype.String, len: n, valid: valid, offs64: offs, chars: chars}
}

// NewList builds a List column from offsets and a child column holding every
// element of every row, concatenated.
//
// offs must have n+1 monotonic entries, and a column CONSTRUCTED here starts at zero
// with offs[n] == child.Len().
//
// That is a property of this constructor and NOT the invariant of a List column, and
// this doc used to say otherwise — "offs[0] == 0 and offs[n] == child.Len(), the
// ordinary Arrow invariant". Column.Slice produces Lists violating both clauses by
// design, and two kernels believed this sentence over that one: listReduce credited
// every element before a slice to its first row, and concatList concatenated whole
// children at shifted offsets. Neither is Arrow's rule either — Arrow reads offsets as
// absolute and a sliced array's first offset is routinely non-zero. The invariant is
// stated once, on ListAccessor.Window.
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
//
// # At most MaxListElements elements
//
// Past it the offsets have wrapped, so it is refused rather than built. The caller
// computed offs as int32 and they are already wrong; the child's length is what can
// be counted.
func NewList(name string, offs []int32, child *Column, valid bitmap.View) *Column {
	if int64(child.Len()) > MaxListElements {
		tooManyElements(name, int64(child.Len()))
	}
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
// Row i occupies child[offs[i]:offs[i+1]], with ABSOLUTE offsets. The child may hold
// elements belonging to no row of this column at all — a sliced List shares its
// parent's whole child — so it is not "every row's elements concatenated", and code
// that treats it that way is wrong on any derived column. Use Lists to walk it, and
// ListAccessor.Window for the part that belongs to this column.
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

// Window returns the half-open range of child elements that belong to this column:
// [offs[0], offs[n]).
//
// # This is the List invariant, stated once
//
// Offsets are ABSOLUTE indices into the child. A List built by NewList starts at zero
// and ends at child.Len(), but that is a property of the constructor, not of a List:
// Column.Slice keeps the offsets absolute and the child whole — "the offsets are not
// rebased, so the child stays whole and the slice stays O(1)" — so a derived column
// has offs[0] > 0 and offs[n] < child.Len(), and the child holds elements that belong
// to rows this column does not have.
//
// Any code that treats the whole child as this column's elements is therefore wrong
// on every sliced List, and two kernels were: listReduce credited every element before
// the window to row 0, and concatList concatenated whole children at shifted offsets.
// It went unnoticed because every List batch-size test read from Parquet, which builds
// a fresh column per batch and never produces a sliced one. Measured before the fix,
// through the public API: Tail(2) then list.sum returned 163 where 100 belonged, and
// re-lazying a six-row frame at batch size 1 put eight elements in an empty list.
//
// Code that reads rows one at a time through Get is unaffected. Code that works on the
// child in bulk must restrict itself to Window.
func (a ListAccessor) Window() (lo, hi int32) {
	if a.n == 0 || len(a.offs) == 0 {
		return 0, 0
	}
	o := unsafeData[int32](a.offs)
	return o[0], o[a.n]
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
func (c *Column) Strings() (a StringAccessor) {
	switch {
	case c.offs != nil:
		a.offsets = entries[int32](c.offs, c.len+1)
	case c.offs64 != nil:
		a.offsets64 = c.offs64
	default:
		return a
	}
	a.chars = c.chars.Bytes()
	return a
}

// StringAccessor reads a variable-width column.
//
// It is a struct rather than an interface so that the layout choice is resolved
// once per batch instead of once per value. When Arrow's StringView layout is
// added it becomes a tagged union here, and no kernel call site changes.
//
// A column past MaxStringBytes has 64-bit offsets (step 368), and Get and Bytes take
// one branch on which, which the processor predicts for a whole column.
type StringAccessor struct {
	offsets   []int32
	offsets64 []int64 // in offsets' place, past MaxStringBytes
	chars     []byte
}

// Get returns row i without copying: the result aliases the character buffer.
func (a StringAccessor) Get(i int) string {
	if a.offsets64 != nil {
		return unsafeString(a.chars[a.offsets64[i]:a.offsets64[i+1]])
	}
	lo, hi := a.offsets[i], a.offsets[i+1]
	return unsafeString(a.chars[lo:hi])
}

// Bytes returns row i as bytes without copying: the result aliases the character
// buffer, and must not be written.
func (a StringAccessor) Bytes(i int) []byte {
	if a.offsets64 != nil {
		lo, hi := a.offsets64[i], a.offsets64[i+1]
		return a.chars[lo:hi:hi]
	}
	lo, hi := a.offsets[i], a.offsets[i+1]
	return a.chars[lo:hi:hi]
}

// Len returns the number of values.
func (a StringAccessor) Len() int {
	if a.offsets64 != nil {
		return len(a.offsets64) - 1
	}
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
// Read it as []int32 with Reinterpret; it holds len+1 entries. A column past
// MaxStringBytes has none, and RawOffsets64 instead.
func (c *Column) RawOffsets() []byte {
	if c.offs == nil {
		return nil
	}
	return c.offs.Bytes()
}

// RawOffsets64 returns a String or Binary column's 64-bit offset buffer as bytes,
// which a column past MaxStringBytes has in its 32-bit buffer's place (step 368):
// []int64 with Reinterpret, len+1 entries. Nil where the offsets are 32-bit.
func (c *Column) RawOffsets64() []byte {
	return bytesOf(c.offs64)
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
// Every payload shape shares rather than copies: Bool and the validity bitmap
// re-window their views, String and List re-window the offsets and keep the
// character buffer or child column whole, a Struct slices its fields, and a
// fixed-width column takes a SUB-BUFFER of the values.
//
// # The fixed-width case used to copy, and step 37 is why it stopped
//
// It copied because Column carries no offset into its values buffer, and adding one
// would put a `+ c.offset` in every kernel's inner loop. It does not need one:
// memory.SliceBuffer produces a buffer whose base IS the sliced range, so
// Values[T] — which reads unsafeData(c.fixed.Bytes())[:c.len] from the start of the
// buffer — sees the right rows without knowing anything happened.
//
// Sharing is what makes splitting an in-memory frame into batches free. memsrc used
// to hand back one batch however large, so an in-memory frame had one unit of work
// and WithThreads did nothing; splitting it while Slice copied would have traded
// that for a full copy of the frame.
//
// Two things make this safe, and both were checked rather than assumed. Nothing
// writes into an existing column's payload — the only two data.Reinterpret call
// sites write into a freshly allocated buffer and read offsets for spilling. And a
// sub-buffer may be aligned to less than 64 bytes, which internal/arrowx's own doc
// already requires kernels to tolerate: "Kernels must treat 64-byte alignment as an
// optimization hint, never as a precondition." In practice the address is
// base + offset*width over a 64-byte-aligned base, so it is usually still aligned,
// and arrowx.IsAligned is what a kernel WOULD ask — no kernel asks today; its only
// caller in the module is arrowx's own package test.
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
		// whole and the slice stays O(1). The window is a view of the offsets, as a
		// fixed column's is of its values: it copied them, a join's probe a 10,000-row
		// query's tenth of what it allocated (step 400).
		d.offs = memory.SliceBuffer(c.offs, offset*4, (length+1)*4)
		d.chars = c.chars
		d.child = c.child
	case c.offs64 != nil:
		// A column of 64-bit offsets, sliced as a column of 32-bit ones is.
		d.offs64 = slices.Clone(c.offs64[offset : offset+length+1])
		d.chars = c.chars
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
		d.fixed = memory.SliceBuffer(c.fixed, offset*width, length*width)
	}
	return d
}

func sizeOf[T Fixed]() uintptr {
	var z T
	return unsafeSizeof(z)
}

// FixedSlice returns the fixed-width payload as a typed slice inside an any —
// []int64, []float64 and so on — or nil when the column has no fixed payload.
//
// It exists for GENERIC callers that cannot name the type. Values[T Fixed] is the
// zero-copy reader, but a caller constrained to something wider than Fixed — the
// UDF methods are constrained to Literal, which also admits string and bool —
// cannot instantiate it at all, so without this it would fall back to Series.Get
// and pay an interface boxing per row.
//
// The returned slice ALIASES the column's buffer, exactly as Values does. Callers
// must not write to it.
//
// Logical types are returned as their physical storage, which is the same punning
// Values documents: a Datetime column comes back as []int64.
func (c *Column) FixedSlice() any {
	if c.fixed == nil {
		return nil
	}
	b := c.fixed.Bytes()
	switch c.dt.Physical().ID() {
	case dtype.TypeInt8:
		return unsafeData[int8](b)[:c.len]
	case dtype.TypeInt16:
		return unsafeData[int16](b)[:c.len]
	case dtype.TypeInt32:
		return unsafeData[int32](b)[:c.len]
	case dtype.TypeInt64:
		return unsafeData[int64](b)[:c.len]
	case dtype.TypeUint8:
		return unsafeData[uint8](b)[:c.len]
	case dtype.TypeUint16:
		return unsafeData[uint16](b)[:c.len]
	case dtype.TypeUint32:
		return unsafeData[uint32](b)[:c.len]
	case dtype.TypeUint64:
		return unsafeData[uint64](b)[:c.len]
	case dtype.TypeFloat32:
		return unsafeData[float32](b)[:c.len]
	case dtype.TypeFloat64:
		return unsafeData[float64](b)[:c.len]
	default:
		// Int128 deliberately omitted: no Literal type maps to it, so a caller
		// that could use this could not name the result anyway.
		return nil
	}
}
