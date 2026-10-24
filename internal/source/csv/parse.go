package csv

import (
	"strconv"
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

type fixedBuilder[T data.Fixed] struct {
	dt    dtype.DataType
	vals  []T
	valid *bitmap.Builder
	parse func([]byte) (T, error)
}

func (b *fixedBuilder[T]) appendField(f []byte) error {
	v, err := b.parse(f)
	if err != nil {
		return err
	}
	b.vals = append(b.vals, v)
	b.valid.Append(true)
	return nil
}

func (b *fixedBuilder[T]) appendNull() {
	var zero T
	b.vals = append(b.vals, zero)
	b.valid.Append(false)
}

func (b *fixedBuilder[T]) len() int { return len(b.vals) }

func (b *fixedBuilder[T]) finish(name string) *data.Column {
	c := data.NewFixed(name, b.dt, b.vals, b.valid.Finish())
	// The values slice is kept for the next batch, as stringBuilder keeps its
	// buffers: NewFixed copies into a fresh Arrow buffer, so the column just made
	// does not alias it. This said NewFixed wrapped the slice, and dropped it every
	// batch to be safe, but NewFixed has copied since the first commit: each batch's
	// values were allocated twice, once growing here by append and once in NewFixed
	// (step 136). TestReadingAllocatesAboutWhatItProduces checks an earlier batch is
	// intact after the later ones.
	b.vals = b.vals[:0]
	b.valid = bitmap.NewBuilder(0)
	return c
}

func (b *fixedBuilder[T]) size(rows, _ int) {
	b.vals = make([]T, 0, rows)
	b.valid = bitmap.NewBuilder(rows)
}

func (b *fixedBuilder[T]) take(name string) *data.Column {
	if wasteful(len(b.vals), cap(b.vals)) {
		return data.NewFixed(name, b.dt, b.vals, b.valid.Finish())
	}
	return data.NewFixedOwned(name, b.dt, b.vals, b.valid.Finish())
}

// boolBuilder is separate because Bool is stored as a bitmap, not a values
// buffer — it is the one type outside data.Fixed.
type boolBuilder struct {
	vals  *bitmap.Builder
	valid *bitmap.Builder
	n     int
}

func (b *boolBuilder) appendField(f []byte) error {
	v, err := parseBool(f)
	if err != nil {
		return err
	}
	b.vals.Append(v)
	b.valid.Append(true)
	b.n++
	return nil
}

func (b *boolBuilder) appendNull() {
	b.vals.Append(false)
	b.valid.Append(false)
	b.n++
}

func (b *boolBuilder) len() int { return b.n }

func (b *boolBuilder) finish(name string) *data.Column {
	c := data.NewBool(name, b.vals.Finish(), b.valid.Finish())
	b.vals = bitmap.NewBuilder(0)
	b.valid = bitmap.NewBuilder(0)
	b.n = 0
	return c
}

func (b *boolBuilder) size(rows, _ int) {
	b.vals, b.valid = bitmap.NewBuilder(rows), bitmap.NewBuilder(rows)
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
	valid *bitmap.Builder
	cat   bool // the column is a Categorical, stored as its text (step 351)
}

func (b *stringBuilder) appendField(f []byte) error {
	// The copy is not optional and never was: f aliases the scanner's buffer,
	// which is valid only until the next call to Next.
	b.chars = append(b.chars, f...)
	b.offs = append(b.offs, int32(len(b.chars)))
	b.valid.Append(true)
	return nil
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
	b.valid.Append(false)
}

func (b *stringBuilder) len() int { return len(b.offs) - 1 }

func (b *stringBuilder) finish(name string) *data.Column {
	c := data.NewStringParts(name, b.offs, b.chars, b.valid.Finish())
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
	b.valid = bitmap.NewBuilder(0)
	return c
}

func (b *stringBuilder) size(rows, chars int) {
	b.offs = make([]int32, 1, rows+1)
	b.chars = make([]byte, 0, chars)
	b.valid = bitmap.NewBuilder(rows)
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
	c := data.NewStringOwned(name, offs, chars, b.valid.Finish())
	if b.cat {
		c = c.WithDType(dtype.Categorical)
	}
	return c
}

// newBuilder returns a builder for dt, or an error naming the type if CSV cannot
// produce it. Refusing here rather than at the first bad value means a schema with
// an unreadable type fails when the file is opened, not ten minutes in.
func newBuilder(dt dtype.DataType) (colBuilder, error) {
	switch dt.ID() {
	case dtype.TypeBool:
		return &boolBuilder{vals: bitmap.NewBuilder(0), valid: bitmap.NewBuilder(0)}, nil
	case dtype.TypeInt8:
		return intBuilder[int8](dt, 8), nil
	case dtype.TypeInt16:
		return intBuilder[int16](dt, 16), nil
	case dtype.TypeInt32:
		return intBuilder[int32](dt, 32), nil
	case dtype.TypeInt64:
		return intBuilder[int64](dt, 64), nil
	case dtype.TypeUint8:
		return uintBuilder[uint8](dt, 8), nil
	case dtype.TypeUint16:
		return uintBuilder[uint16](dt, 16), nil
	case dtype.TypeUint32:
		return uintBuilder[uint32](dt, 32), nil
	case dtype.TypeUint64:
		return uintBuilder[uint64](dt, 64), nil
	case dtype.TypeInt128:
		return &fixedBuilder[i128.Int128]{dt: dt, valid: bitmap.NewBuilder(0),
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
		return &fixedBuilder[i128.Int128]{dt: dt, valid: bitmap.NewBuilder(0),
			parse: func(b []byte) (i128.Int128, error) {
				return i128.ParseDecimal(str(b), prec, scale)
			}}, nil
	case dtype.TypeEnum:
		// The text, encoded as its category's index. A value outside the categories
		// is a value error naming it, as a strict String -> Enum cast refuses it.
		pos := make(map[string]uint32, len(dt.Categories()))
		for i, s := range dt.Categories() {
			pos[s] = uint32(i)
		}
		return &fixedBuilder[uint32]{dt: dt, valid: bitmap.NewBuilder(0),
			parse: func(b []byte) (uint32, error) {
				if j, ok := pos[str(b)]; ok {
					return j, nil
				}
				return 0, strconv.ErrSyntax
			}}, nil
	case dtype.TypeFloat32:
		return &fixedBuilder[float32]{dt: dt, valid: bitmap.NewBuilder(0),
			parse: func(b []byte) (float32, error) {
				v, err := dtype.ParseFloat(str(b), 32)
				return float32(v), err
			}}, nil
	case dtype.TypeFloat64:
		return &fixedBuilder[float64]{dt: dt, valid: bitmap.NewBuilder(0),
			parse: func(b []byte) (float64, error) { return dtype.ParseFloat(str(b), 64) }}, nil
	case dtype.TypeString, dtype.TypeCategorical:
		// offs is seeded with its leading zero here rather than branched on per
		// append; finish preserves it, so the hot path never tests for it.
		return &stringBuilder{offs: make([]int32, 1), valid: bitmap.NewBuilder(0),
			cat: dt.ID() == dtype.TypeCategorical}, nil

	case dtype.TypeDate, dtype.TypeTime, dtype.TypeDatetime, dtype.TypeDuration:
		// Parsing goes through dtype.ParseTemporal, the same function the frame
		// renderer's formatter inverts and the CSV writer emits for. Four copies of
		// a date parser is exactly the defect shape step 2's audit found.
		if dt.Physical().ID() == dtype.TypeInt32 {
			return &fixedBuilder[int32]{dt: dt, valid: bitmap.NewBuilder(0),
				parse: func(b []byte) (int32, error) {
					v, ok := dtype.ParseTemporal(dt, str(b))
					if !ok {
						return 0, strconv.ErrSyntax
					}
					return int32(v), nil
				}}, nil
		}
		return &fixedBuilder[int64]{dt: dt, valid: bitmap.NewBuilder(0),
			parse: func(b []byte) (int64, error) {
				v, ok := dtype.ParseTemporal(dt, str(b))
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

func intBuilder[T interface {
	data.Fixed
	~int8 | ~int16 | ~int32 | ~int64
}](dt dtype.DataType, bits int) colBuilder {
	return &fixedBuilder[T]{dt: dt, valid: bitmap.NewBuilder(0),
		parse: func(b []byte) (T, error) {
			v, err := strconv.ParseInt(str(b), 10, bits)
			return T(v), err
		}}
}

func uintBuilder[T interface {
	data.Fixed
	~uint8 | ~uint16 | ~uint32 | ~uint64
}](dt dtype.DataType, bits int) colBuilder {
	return &fixedBuilder[T]{dt: dt, valid: bitmap.NewBuilder(0),
		parse: func(b []byte) (T, error) {
			v, err := strconv.ParseUint(str(b), 10, bits)
			return T(v), err
		}}
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
