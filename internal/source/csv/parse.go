package csv

import (
	"errors"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// str views b as a string without copying.
//
// Safe here and nowhere careless: every caller passes the result straight to a
// strconv parser, which reads the string and does not retain it. The alternative,
// string(b), heap-allocates on every value in the file because strconv's error
// paths let the parameter escape — a copy per value across a 10 GB file.
//
// The result must never outlive the scanner's buffer. Values that need to survive
// (strings) are copied explicitly in stringBuilder.
func str(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// --- builders ------------------------------------------------------------------

// colBuilder accumulates one column's values for one batch.
type colBuilder interface {
	// appendField parses and appends. It is given the raw bytes, which alias the
	// scanner's buffer and must not be retained.
	appendField(b []byte) error
	appendNull()
	// finish produces the column and resets the builder for the next batch.
	finish(name string) *data.Column
	// size readies a builder for rows values, and a String one for about chars
	// bytes of them, so that a block whose rows are counted grows nothing.
	size(rows, chars int)
	// take is finish for a builder used once: the column takes the builder's
	// slices rather than copying them, and the builder is not used again.
	take(name string) *data.Column
	len() int
}

// wasteful reports whether a slice of n values in an allocation of c is worth a
// copy before a column takes it. A column holds all of what it takes, for as long
// as the query holds the column, so more than an eighth unused is copied away.
func wasteful(n, c int) bool { return c-n > n/8 }

// validity is a batch's validity built in runs: valid values are counted, and put
// in the bitmap only when a null comes, so a batch with no null builds none, and
// its column says so without a bitmap of ones, which every kernel after it took
// the slow path for (step 373).
type validity struct {
	bits *bitmap.Builder // nil until the batch's first null
	run  int             // valid values since the last null
	rows int             // the rows a sized batch holds, the bitmap's capacity
}

func (v *validity) null() {
	if v.bits == nil {
		v.bits = bitmap.NewBuilder(v.rows)
	}
	v.bits.AppendMany(true, v.run)
	v.bits.Append(false)
	v.run = 0
}

// finish is the batch's validity, and readies v for the next batch.
func (v *validity) finish() bitmap.View {
	b := v.bits
	if b == nil {
		v.run = 0
		return bitmap.View{}
	}
	b.AppendMany(true, v.run)
	v.bits, v.run = nil, 0
	return b.Finish()
}

type fixedBuilder[T data.Fixed] struct {
	dt    dtype.DataType
	vals  []T
	valid validity
	parse func([]byte) (T, error)
}

func (b *fixedBuilder[T]) appendField(f []byte) error {
	v, err := b.parse(f)
	if err != nil {
		return err
	}
	b.vals = append(b.vals, v)
	b.valid.run++
	return nil
}

func (b *fixedBuilder[T]) appendNull() {
	var zero T
	b.vals = append(b.vals, zero)
	b.valid.null()
}

func (b *fixedBuilder[T]) len() int { return len(b.vals) }

func (b *fixedBuilder[T]) finish(name string) *data.Column {
	c := data.NewFixed(name, b.dt, b.vals, b.valid.finish())
	// The values slice is kept for the next batch, as stringBuilder keeps its
	// buffers: NewFixed copies into a fresh Arrow buffer, so the column just made
	// does not alias it. This said NewFixed wrapped the slice, and dropped it every
	// batch to be safe, but NewFixed has copied since the first commit: each batch's
	// values were allocated twice, once growing here by append and once in NewFixed
	// (step 136). TestReadingAllocatesAboutWhatItProduces checks an earlier batch is
	// intact after the later ones.
	b.vals = b.vals[:0]
	return c
}

func (b *fixedBuilder[T]) size(rows, _ int) {
	b.vals = make([]T, 0, rows)
	b.valid.rows = rows
}

func (b *fixedBuilder[T]) take(name string) *data.Column {
	if wasteful(len(b.vals), cap(b.vals)) {
		return data.NewFixed(name, b.dt, b.vals, b.valid.finish())
	}
	return data.NewFixedOwned(name, b.dt, b.vals, b.valid.finish())
}

// boolBuilder is separate because Bool is stored as a bitmap, not a values
// buffer — it is the one type outside data.Fixed.
type boolBuilder struct {
	vals  *bitmap.Builder
	valid validity
	n     int
}

func (b *boolBuilder) appendField(f []byte) error {
	v, err := parseBool(f)
	if err != nil {
		return err
	}
	b.vals.Append(v)
	b.valid.run++
	b.n++
	return nil
}

func (b *boolBuilder) appendNull() {
	b.vals.Append(false)
	b.valid.null()
	b.n++
}

func (b *boolBuilder) len() int { return b.n }

func (b *boolBuilder) finish(name string) *data.Column {
	c := data.NewBool(name, b.vals.Finish(), b.valid.finish())
	b.vals = bitmap.NewBuilder(0)
	b.n = 0
	return c
}

func (b *boolBuilder) size(rows, _ int) {
	b.vals, b.valid.rows = bitmap.NewBuilder(rows), rows
}

// take is finish: a bitmap's Finish already hands its bytes over.
func (b *boolBuilder) take(name string) *data.Column { return b.finish(name) }

// stringBuilder accumulates Arrow's string layout directly: one offsets slice and
// one character buffer.
//
// It used to append a Go string per value — one heap allocation per row — and
// then hand the []string to data.NewString, which copied every byte a second time
// into these same two buffers. A million-row scan of a file with three string
// columns spent 3.15M allocations there, 99% of everything the CSV reader
// allocated. It is the defect the Parquet reader carried until byteArrayCol was
// rewritten this way, and the fix is the same one.
//
// offs carries the usual n+1 entries: it holds a single zero before the first
// append and gains one entry per value, null or not.
type stringBuilder struct {
	offs  []int32
	chars []byte
	valid validity
	cat   bool // the column is a Categorical, stored as its text (step 351)
	lossy bool // text that is not UTF-8 is read with U+FFFD, not refused (step 357)

	// known, when set and true, says the fields appended are UTF-8: a scanner's or a
	// stage's verdict on a span of whole records, checked at once. A field is
	// checked by itself where it is unset or false. Checked by itself, every field
	// was a call, and reading a million rows of two text columns 11% slower.
	known *bool
}

// errNotUTF8 is text that is not UTF-8 in a String column, Polars' "invalid utf-8
// sequence".
var errNotUTF8 = errors.New("it is not UTF-8")

// trustUTF8 points builders' text columns at the verdict known.
func trustUTF8(builders []colBuilder, known *bool) {
	for _, b := range builders {
		if sb, ok := b.(*stringBuilder); ok {
			sb.known = known
		}
	}
}

func (b *stringBuilder) appendField(f []byte) error {
	// Text is UTF-8, as Polars requires: a String's every kernel reads it so.
	if (b.known == nil || !*b.known) && !utf8.Valid(f) {
		if !b.lossy {
			return errNotUTF8
		}
		b.chars = appendLossy(b.chars, f)
		b.offs = append(b.offs, int32(len(b.chars)))
		b.valid.run++
		return nil
	}
	// The copy is not optional and never was: f aliases the scanner's buffer,
	// which is valid only until the next call to Next.
	b.chars = append(b.chars, f...)
	b.offs = append(b.offs, int32(len(b.chars)))
	b.valid.run++
	return nil
}

// appendLossy appends f with each maximal invalid subsequence a U+FFFD, as Rust's
// from_utf8_lossy and Polars' utf8-lossy replace it: a lead byte and the
// continuation bytes its sequence allows before it fails count as one, so
// "\xe2\x82A" is one replacement and an A, where a byte at a time would be two.
func appendLossy(dst, f []byte) []byte {
	for len(f) > 0 {
		r, n := utf8.DecodeRune(f)
		if r != utf8.RuneError || n > 1 {
			dst = append(dst, f[:n]...)
			f = f[n:]
			continue
		}
		dst = utf8.AppendRune(dst, utf8.RuneError)
		f = f[maximalSubpart(f):]
	}
	return dst
}

// maximalSubpart is the length of the invalid sequence f begins with: its lead byte,
// and the continuation bytes that byte's sequence takes before it fails, the second
// in the range the lead allows.
func maximalSubpart(f []byte) int {
	lead := f[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF)
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		need = 1
	case lead == 0xE0:
		need, lo = 2, 0xA0
	case lead == 0xED:
		need, hi = 2, 0x9F
	case lead >= 0xE1 && lead <= 0xEF:
		need = 2
	case lead == 0xF0:
		need, lo = 3, 0x90
	case lead == 0xF4:
		need, hi = 3, 0x8F
	case lead >= 0xF1 && lead <= 0xF3:
		need = 3
	default:
		return 1
	}
	n := 1
	for ; n <= need && n < len(f); n++ {
		c := f[n]
		if n > 1 {
			lo, hi = 0x80, 0xBF
		}
		if c < lo || c > hi {
			break
		}
	}
	return n
}

// commaPoint is b with its decimal comma a point, for a float's or a Decimal's
// parse, or ok false for text with a point in it, which under a decimal comma is no
// number, as Polars reads it.
func commaPoint(dst, b []byte) ([]byte, bool) {
	for _, c := range b {
		switch c {
		case '.':
			return dst, false
		case ',':
			c = '.'
		}
		dst = append(dst, c)
	}
	return dst, true
}

// appendNull records an absent value as an empty slice — chars does not advance,
// so a null costs nothing beyond its offset entry.
//
// A null that DID advance chars would not corrupt itself, since nothing reads a
// null's bytes; it would corrupt the value after it, whose window starts at the
// offset this call recorded. That makes the failure appear one row away from its
// cause, which is why the test for it puts a null between two multi-byte values.
func (b *stringBuilder) appendNull() {
	b.offs = append(b.offs, int32(len(b.chars)))
	b.valid.null()
}

func (b *stringBuilder) len() int { return len(b.offs) - 1 }

func (b *stringBuilder) finish(name string) *data.Column {
	c := data.NewStringParts(name, b.offs, b.chars, b.valid.finish())
	if b.cat {
		c = c.WithDType(dtype.Categorical)
	}
	// Both buffers are reused with their capacity intact, which is safe because
	// NewStringParts COPIES into fresh arrow buffers, so the column we just produced
	// does not alias what the next batch overwrites. NewFixed copies too, and
	// fixedBuilder keeps its slice for the same reason (step 136).
	//
	// Truncating to [:1] keeps the leading zero, so offs is never re-seeded.
	b.offs = b.offs[:1]
	b.chars = b.chars[:0]
	return c
}

func (b *stringBuilder) size(rows, chars int) {
	b.offs = make([]int32, 1, rows+1)
	b.chars = make([]byte, 0, chars)
	b.valid.rows = rows
}

// take hands both buffers over. The characters are sized by an estimate, so the
// slice is copied first when the estimate was well over, or append grew it well
// past them.
func (b *stringBuilder) take(name string) *data.Column {
	offs, chars := b.offs, b.chars
	if wasteful(len(offs), cap(offs)) {
		offs = append(make([]int32, 0, len(offs)), offs...)
	}
	if wasteful(len(chars), cap(chars)) {
		chars = append(make([]byte, 0, len(chars)), chars...)
	}
	c := data.NewStringOwned(name, offs, chars, b.valid.finish())
	if b.cat {
		c = c.WithDType(dtype.Categorical)
	}
	return c
}

// newBuilder returns a builder for dt, or an error naming the type if CSV cannot
// produce it. Refusing here rather than at the first bad value means a schema with
// an unreadable type fails when the file is opened, not ten minutes in.
func newBuilder(dt dtype.DataType, o Options) (colBuilder, error) {
	switch dt.ID() {
	case dtype.TypeBool:
		return &boolBuilder{vals: bitmap.NewBuilder(0)}, nil
	case dtype.TypeInt8:
		return intBuilder[int8](dt, 8), nil
	case dtype.TypeInt16:
		return intBuilder[int16](dt, 16), nil
	case dtype.TypeInt32:
		return intBuilder[int32](dt, 32), nil
	case dtype.TypeInt64:
		return &int64Builder{fixedBuilder[int64]{dt: dt}}, nil
	case dtype.TypeUint8:
		return uintBuilder[uint8](dt, 8), nil
	case dtype.TypeUint16:
		return uintBuilder[uint16](dt, 16), nil
	case dtype.TypeUint32:
		return uintBuilder[uint32](dt, 32), nil
	case dtype.TypeUint64:
		return uintBuilder[uint64](dt, 64), nil
	case dtype.TypeInt128:
		return &fixedBuilder[i128.Int128]{dt: dt,
			parse: func(b []byte) (i128.Int128, error) {
				v, ok := i128.Parse(str(b))
				if !ok {
					return v, strconv.ErrSyntax
				}
				return v, nil
			}}, nil
	case dtype.TypeDecimal:
		// What the writer writes for a Decimal column, read back exactly — or, for
		// text with more digits than the scale, rounded half away from zero, as
		// Polars and DuckDB read it.
		prec, scale := int(dt.Precision()), int(dt.Scale())
		var point []byte
		return &fixedBuilder[i128.Int128]{dt: dt,
			parse: func(b []byte) (i128.Int128, error) {
				if o.DecimalComma {
					var ok bool
					if point, ok = commaPoint(point[:0], b); !ok {
						return i128.Int128{}, strconv.ErrSyntax
					}
					b = point
				}
				return i128.ParseDecimal(str(b), prec, scale)
			}}, nil
	case dtype.TypeEnum:
		// The text, encoded as its category's index. A value outside the categories
		// is a value error naming it, as a strict String -> Enum cast refuses it.
		pos := make(map[string]uint32, len(dt.Categories()))
		for i, s := range dt.Categories() {
			pos[s] = uint32(i)
		}
		return &fixedBuilder[uint32]{dt: dt,
			parse: func(b []byte) (uint32, error) {
				if j, ok := pos[str(b)]; ok {
					return j, nil
				}
				return 0, strconv.ErrSyntax
			}}, nil
	case dtype.TypeFloat32:
		var point []byte
		return &fixedBuilder[float32]{dt: dt,
			parse: func(b []byte) (float32, error) {
				if o.DecimalComma {
					var ok bool
					if point, ok = commaPoint(point[:0], b); !ok {
						return 0, strconv.ErrSyntax
					}
					b = point
				}
				v, err := dtype.ParseFloat(str(b), 32)
				return float32(v), err
			}}, nil
	case dtype.TypeFloat64:
		if o.DecimalComma {
			var point []byte
			return &fixedBuilder[float64]{dt: dt,
				parse: func(b []byte) (float64, error) {
					var ok bool
					if point, ok = commaPoint(point[:0], b); !ok {
						return 0, strconv.ErrSyntax
					}
					return dtype.ParseFloat(str(point), 64)
				}}, nil
		}
		return &float64Builder{fixedBuilder[float64]{dt: dt}}, nil
	case dtype.TypeString, dtype.TypeCategorical:
		// offs is seeded with its leading zero here rather than branched on per
		// append; finish preserves it, so the hot path never tests for it.
		return &stringBuilder{offs: make([]int32, 1),
			cat: dt.ID() == dtype.TypeCategorical, lossy: o.Lossy}, nil

	case dtype.TypeDate, dtype.TypeTime, dtype.TypeDatetime, dtype.TypeDuration:
		// Parsing goes through dtype, the function the frame renderer's formatter
		// inverts and the CSV writer emits for. Four copies of a date parser is exactly
		// the defect shape step 2's audit found. A date, an instant and a time of day are
		// read by Polars' CSV patterns, dd/mm/yyyy among them, scanned by hand, and else
		// as ParseTemporal reads them (step 355); a Duration as ParseTemporal does.
		parse := dtype.ParseTemporalText
		if dt.ID() == dtype.TypeDuration {
			parse = dtype.ParseTemporal
		}
		if dt.Physical().ID() == dtype.TypeInt32 {
			return &fixedBuilder[int32]{dt: dt,
				parse: func(b []byte) (int32, error) {
					v, ok := parse(dt, str(b))
					if !ok {
						return 0, strconv.ErrSyntax
					}
					return int32(v), nil
				}}, nil
		}
		return &fixedBuilder[int64]{dt: dt,
			parse: func(b []byte) (int64, error) {
				v, ok := parse(dt, str(b))
				if !ok {
					return 0, strconv.ErrSyntax
				}
				return v, nil
			}}, nil
	default:
		return nil, uerr.New(uerr.KindUnsupported, "scan_csv",
			"cannot read %s from a CSV file", dt).
			Hint("CSV supports Bool, the integer and float types, Int128, Decimal, " +
				"the temporal types, String, Enum and Categorical").
			Hint("read it as String and cast, if a conversion exists")
	}
}

// int64Builder and float64Builder are fixedBuilder for the types inference gives a
// file's numbers, their parse called, not a func value's (step 373).
type int64Builder struct{ fixedBuilder[int64] }

func (b *int64Builder) appendField(f []byte) error {
	v, err := parseInt(f, 64)
	if err != nil {
		return err
	}
	b.vals = append(b.vals, v)
	b.valid.run++
	return nil
}

type float64Builder struct{ fixedBuilder[float64] }

func (b *float64Builder) appendField(f []byte) error {
	v, err := dtype.ParseFloat(str(f), 64)
	if err != nil {
		return err
	}
	b.vals = append(b.vals, v)
	b.valid.run++
	return nil
}

func intBuilder[T interface {
	data.Fixed
	~int8 | ~int16 | ~int32 | ~int64
}](dt dtype.DataType, bits int) colBuilder {
	return &fixedBuilder[T]{dt: dt,
		parse: func(b []byte) (T, error) {
			v, err := parseInt(b, bits)
			return T(v), err
		}}
}

func uintBuilder[T interface {
	data.Fixed
	~uint8 | ~uint16 | ~uint32 | ~uint64
}](dt dtype.DataType, bits int) colBuilder {
	return &fixedBuilder[T]{dt: dt,
		parse: func(b []byte) (T, error) {
			v, err := parseUint(b, bits)
			return T(v), err
		}}
}

// parseInt is strconv.ParseInt(str(b), 10, bits), in one pass for what a file
// mostly holds, a sign and at most 18 digits that fit bits, and strconv's for
// anything else, its errors among it (step 373).
func parseInt(b []byte, bits int) (int64, error) {
	d := b
	neg := false
	if len(d) > 0 && (d[0] == '-' || d[0] == '+') {
		neg, d = d[0] == '-', d[1:]
	}
	if len(d) == 0 || len(d) > 18 {
		return strconv.ParseInt(str(b), 10, bits)
	}
	var v int64
	for _, c := range d {
		c -= '0'
		if c > 9 {
			return strconv.ParseInt(str(b), 10, bits)
		}
		v = v*10 + int64(c)
	}
	if neg {
		v = -v
	}
	if bits < 64 && (v < -1<<(bits-1) || v >= 1<<(bits-1)) {
		return strconv.ParseInt(str(b), 10, bits)
	}
	return v, nil
}

// parseUint is strconv.ParseUint(str(b), 10, bits) as parseInt is ParseInt's: at
// most 19 digits that fit bits in one pass, strconv's for the rest. ParseUint takes
// no sign, so a sign is the rest.
func parseUint(b []byte, bits int) (uint64, error) {
	if len(b) == 0 || len(b) > 19 {
		return strconv.ParseUint(str(b), 10, bits)
	}
	var v uint64
	for _, c := range b {
		c -= '0'
		if c > 9 {
			return strconv.ParseUint(str(b), 10, bits)
		}
		v = v*10 + uint64(c)
	}
	if bits < 64 && v >= 1<<bits {
		return strconv.ParseUint(str(b), 10, bits)
	}
	return v, nil
}

// parseBool accepts exactly true/false in any capitalisation.
//
// Deliberately NOT strconv.ParseBool, which also accepts 1, 0, t and f. A column
// of 0s and 1s is far more often a count than a flag, and inference would have to
// choose; accepting them here would make an explicit Bool schema silently disagree
// with what inference does with the same file.
func parseBool(b []byte) (bool, error) {
	switch len(b) {
	case 4:
		if eqFold(b, "true") {
			return true, nil
		}
	case 5:
		if eqFold(b, "false") {
			return false, nil
		}
	}
	return false, strconv.ErrSyntax
}

// eqFold compares against an ASCII lowercase literal.
func eqFold(b []byte, lower string) bool {
	if len(b) != len(lower) {
		return false
	}
	for i := range b {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}
