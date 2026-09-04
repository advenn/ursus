package csv

import (
	"strconv"
	"unsafe"

	"github.com/advenn/ursus/dtype"
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
	len() int
}

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
	// A fresh values slice per batch: NewFixed wraps the slice without copying, so
	// reusing it would rewrite a batch the consumer is still holding.
	b.vals = nil
	b.valid = bitmap.NewBuilder(0)
	return c
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

type stringBuilder struct {
	dt    dtype.DataType
	vals  []string
	valid *bitmap.Builder
}

func (b *stringBuilder) appendField(f []byte) error {
	b.vals = append(b.vals, string(f)) // must copy: f aliases the scanner buffer
	b.valid.Append(true)
	return nil
}

func (b *stringBuilder) appendNull() {
	b.vals = append(b.vals, "")
	b.valid.Append(false)
}

func (b *stringBuilder) len() int { return len(b.vals) }

func (b *stringBuilder) finish(name string) *data.Column {
	c := data.NewString(name, b.vals, b.valid.Finish())
	b.vals = nil
	b.valid = bitmap.NewBuilder(0)
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
	case dtype.TypeFloat32:
		return &fixedBuilder[float32]{dt: dt, valid: bitmap.NewBuilder(0),
			parse: func(b []byte) (float32, error) {
				v, err := strconv.ParseFloat(str(b), 32)
				return float32(v), err
			}}, nil
	case dtype.TypeFloat64:
		return &fixedBuilder[float64]{dt: dt, valid: bitmap.NewBuilder(0),
			parse: func(b []byte) (float64, error) { return strconv.ParseFloat(str(b), 64) }}, nil
	case dtype.TypeString:
		return &stringBuilder{dt: dt, valid: bitmap.NewBuilder(0)}, nil

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
			Hint("CSV supports Bool, the integer and float types, the temporal " +
				"types and String").
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
