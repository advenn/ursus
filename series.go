package ursus

import (
	"reflect"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// Column is a named, typed, nullable run of values.
type Column = data.Column

// Series is a typed view over a Column.
type Series[T any] = data.Series[T]

// TypedColumn returns a typed view over a column.
func TypedColumn[T any](c *Column) (*Series[T], error) { return data.TypedColumn[T](c) }

// Values builds a column from Go values, with no nulls.
//
// The element type determines the column type: []int64 gives Int64, []string gives
// String, []time.Time gives Datetime(ns, UTC).
func Values[T Literal](name string, vals []T) *Column {
	return valuesWith(name, vals, bitmap.AllSet(len(vals)))
}

// ValuesNullable builds a column with an explicit validity mask, where valid[i]
// false means row i is null.
//
// Nulls are a parallel bitmap rather than a *T or an Option[T] because boxing
// would cost an allocation per row and make the values buffer non-contiguous —
// exactly what a columnar layout must not do.
func ValuesNullable[T Literal](name string, vals []T, valid []bool) *Column {
	b := bitmap.NewBuilder(len(vals))
	for i := range vals {
		b.Append(i < len(valid) && valid[i])
	}
	return valuesWith(name, vals, b.Finish())
}

func valuesWith[T Literal](name string, vals []T, valid bitmap.View) *Column {
	switch v := any(vals).(type) {
	case []bool:
		bits := bitmap.NewBuilder(len(v))
		for _, x := range v {
			bits.Append(x)
		}
		return data.NewBool(name, bits.Finish(), valid)
	case []string:
		return data.NewString(name, v, valid)
	case [][]byte:
		strs := make([]string, len(v))
		for i, b := range v {
			strs[i] = string(b)
		}
		return data.NewString(name, strs, valid).WithDType(dtype.Binary)
	case []time.Duration:
		// Matched before []int64 would be, so a Duration keeps its identity. Without
		// this it fell through to convertNamedSlice and landed as a bare Int64,
		// losing the type — the same reasoning that puts time.Duration ahead of
		// int64 in litNode.
		ns := make([]int64, len(v))
		for i, d := range v {
			ns[i] = int64(d)
		}
		return data.NewFixed(name, dtype.Duration(dtype.Nano), ns, valid)

	case []time.Time:
		ns := make([]int64, len(v))
		for i, t := range v {
			ns[i] = t.UnixNano()
		}
		return data.NewFixed(name, dtype.Datetime(dtype.Nano, "UTC"), ns, valid)

	case []int:
		i64 := make([]int64, len(v))
		for i, x := range v {
			i64[i] = int64(x)
		}
		return data.NewFixed(name, dtype.Int64, i64, valid)
	case []uint:
		u64 := make([]uint64, len(v))
		for i, x := range v {
			u64[i] = uint64(x)
		}
		return data.NewFixed(name, dtype.Uint64, u64, valid)

	case []int8:
		return data.NewFixed(name, dtype.Int8, v, valid)
	case []int16:
		return data.NewFixed(name, dtype.Int16, v, valid)
	case []int32:
		return data.NewFixed(name, dtype.Int32, v, valid)
	case []int64:
		return data.NewFixed(name, dtype.Int64, v, valid)
	case []uint8:
		return data.NewFixed(name, dtype.Uint8, v, valid)
	case []uint16:
		return data.NewFixed(name, dtype.Uint16, v, valid)
	case []uint32:
		return data.NewFixed(name, dtype.Uint32, v, valid)
	case []uint64:
		return data.NewFixed(name, dtype.Uint64, v, valid)
	case []float32:
		return data.NewFixed(name, dtype.Float32, v, valid)
	case []float64:
		return data.NewFixed(name, dtype.Float64, v, valid)

	default:
		// Unreachable for the Literal constraint's own types. A DEFINED type over
		// a permitted underlying type (type UserID int64) lands here, because a
		// []UserID does not match `case []int64`.
		return convertNamedSlice(name, vals, valid)
	}
}

// Frame builds an in-memory LazyFrame from columns.
//
// This is the entry point for data that is already in Go — the counterpart to
// ScanParquet and friends, which arrive in step 2.
func Frame(cols ...*Column) *LazyFrame {
	src, err := memsrc.FromColumns(cols...)
	if err != nil {
		return &LazyFrame{err: err}
	}
	return Scan(src)
}

// FrameOf is Frame for callers who want the error separately.
func FrameOf(cols ...*Column) (*LazyFrame, error) {
	src, err := memsrc.FromColumns(cols...)
	if err != nil {
		return nil, err
	}
	return Scan(src), nil
}

// convertNamedSlice handles a slice of a DEFINED type over a permitted underlying
// type — []UserID where `type UserID int64`.
//
// The Literal constraint uses `~`, so such slices are accepted at the call site,
// but `case []int64` will not match []UserID. Rather than forbid them (which would
// make the tildes a lie) they are converted here. The reflection cost is one Kind
// lookup plus one pass over the values, at frame-construction time.
func convertNamedSlice[T Literal](name string, vals []T, valid bitmap.View) *Column {
	if len(vals) == 0 {
		return data.NewNull(name, dtype.Null, 0)
	}

	rv := reflect.ValueOf(vals)
	switch rv.Type().Elem().Kind() {
	case reflect.Bool:
		bits := bitmap.NewBuilder(len(vals))
		for i := range vals {
			bits.Append(rv.Index(i).Bool())
		}
		return data.NewBool(name, bits.Finish(), valid)

	case reflect.String:
		out := make([]string, len(vals))
		for i := range vals {
			out[i] = rv.Index(i).String()
		}
		return data.NewString(name, out, valid)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out := make([]int64, len(vals))
		for i := range vals {
			out[i] = rv.Index(i).Int()
		}
		return data.NewFixed(name, dtype.Int64, out, valid)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		out := make([]uint64, len(vals))
		for i := range vals {
			out[i] = rv.Index(i).Uint()
		}
		return data.NewFixed(name, dtype.Uint64, out, valid)

	case reflect.Float32, reflect.Float64:
		out := make([]float64, len(vals))
		for i := range vals {
			out[i] = rv.Index(i).Float()
		}
		return data.NewFixed(name, dtype.Float64, out, valid)

	default:
		return data.NewNull(name, dtype.Null, len(vals))
	}
}
