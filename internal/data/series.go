package data

import (
	"iter"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/uerr"
)

// Series is a typed view over a Column.
//
// # Column owns, Series views
//
// This resolves the "two representations, two code paths" problem the design
// review flagged. A Series holds no storage of its own: it is a Column plus a
// resolved accessor, constructed on demand and thrown away freely. The engine
// only ever handles Columns, so kernels, operators and the evaluator have exactly
// one type to know about. Series exists purely at the boundary where Go values
// meet columnar data.
//
// # Why generic methods matter here
//
// Before Go 1.27, Map and Fold had to be package-level functions taking the
// receiver as an argument:
//
//	data.Map(data.Map(s, parse), normalise)     // reads inside-out
//
// As generic methods they chain in the direction the data flows:
//
//	s.Map(parse).Map(normalise)
//
// That difference — nothing more — is a large part of why this library targets
// Go 1.27.
type Series[T any] struct {
	col *Column
	get func(i int) T
}

// TypedColumn returns a typed view over c, or an error if T does not match the
// column's physical representation.
func TypedColumn[T any](c *Column) (*Series[T], error) {
	get, err := accessor[T](c)
	if err != nil {
		return nil, err
	}
	return &Series[T]{col: c, get: get}, nil
}

// accessor resolves the per-row reader ONCE, so the type switch is paid per
// column rather than per value.
func accessor[T any](c *Column) (func(int) T, error) {
	var zero T
	switch any(zero).(type) {
	case bool:
		if c.dt.ID() != dtype.TypeBool {
			return nil, mismatch[T](c)
		}
		bits := c.Bools()
		return func(i int) T { return any(bits.Get(i)).(T) }, nil

	case string:
		if !c.dt.IsString() && c.dt.ID() != dtype.TypeBinary {
			return nil, mismatch[T](c)
		}
		acc := c.Strings()
		return func(i int) T { return any(acc.Get(i)).(T) }, nil

	case int8:
		return fixedAccessor[T, int8](c)
	case int16:
		return fixedAccessor[T, int16](c)
	case int32:
		return fixedAccessor[T, int32](c)
	case int64:
		return fixedAccessor[T, int64](c)
	case uint8:
		return fixedAccessor[T, uint8](c)
	case uint16:
		return fixedAccessor[T, uint16](c)
	case uint32:
		return fixedAccessor[T, uint32](c)
	case uint64:
		return fixedAccessor[T, uint64](c)
	case float32:
		return fixedAccessor[T, float32](c)
	case float64:
		return fixedAccessor[T, float64](c)
	case i128.Int128:
		return fixedAccessor[T, i128.Int128](c)

	default:
		return nil, uerr.New(uerr.KindType, "",
			"no typed view for %T over a %s column", zero, c.dt)
	}
}

func fixedAccessor[T any, P Fixed](c *Column) (func(int) T, error) {
	vals, err := Values[P](c)
	if err != nil {
		return nil, err
	}
	return func(i int) T { return any(vals[i]).(T) }, nil
}

func mismatch[T any](c *Column) error {
	var zero T
	return uerr.New(uerr.KindType, "",
		"column %q is %s and cannot be viewed as %T", c.name, c.dt, zero)
}

// Column returns the underlying column.
func (s *Series[T]) Column() *Column { return s.col }

// Name returns the column name.
func (s *Series[T]) Name() string { return s.col.name }

// DType returns the logical type.
func (s *Series[T]) DType() dtype.DataType { return s.col.dt }

// Len returns the number of values.
func (s *Series[T]) Len() int { return s.col.len }

// NullCount returns the number of nulls.
func (s *Series[T]) NullCount() int { return s.col.NullCount() }

// Get returns the value at i and whether it is non-null.
//
// The two-value form is deliberate. Boxing nulls into a *T or an Option[T] would
// cost an allocation per row and would make the values buffer non-contiguous,
// which is exactly what a columnar library must not do. Validity stays a parallel
// bitmap and the caller is told about it explicitly.
func (s *Series[T]) Get(i int) (T, bool) {
	if !s.col.valid.Get(i) {
		var zero T
		return zero, false
	}
	return s.get(i), true
}

// All iterates (value, valid) pairs in order.
func (s *Series[T]) All() iter.Seq2[T, bool] {
	return func(yield func(T, bool) bool) {
		for i := range s.col.len {
			v, ok := s.Get(i)
			if !yield(v, ok) {
				return
			}
		}
	}
}

// Values collects the non-null values into a slice, in order.
func (s *Series[T]) Values() []T {
	out := make([]T, 0, s.col.len)
	for v, ok := range s.All() {
		if ok {
			out = append(out, v)
		}
	}
	return out
}

// Map applies fn to every value, producing a plain Go slice.
//
// Nulls yield the zero value of U; use MapErr or All if the distinction matters.
// This is a generic METHOD, which is what lets it chain.
func (s *Series[T]) Map[U any](fn func(T) U) []U {
	out := make([]U, s.col.len)
	for i := range s.col.len {
		if v, ok := s.Get(i); ok {
			out[i] = fn(v)
		}
	}
	return out
}

// MapErr is Map with a fallible function, stopping at the first error and naming
// the row it failed on.
func (s *Series[T]) MapErr[U any](fn func(T) (U, error)) ([]U, error) {
	out := make([]U, s.col.len)
	for i := range s.col.len {
		v, ok := s.Get(i)
		if !ok {
			continue
		}
		u, err := fn(v)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindValue, "",
				"row %d of column %q", i, s.col.name)
		}
		out[i] = u
	}
	return out, nil
}

// Fold reduces the non-null values left to right.
func (s *Series[T]) Fold[A any](init A, fn func(A, T) A) A {
	acc := init
	for v, ok := range s.All() {
		if ok {
			acc = fn(acc, v)
		}
	}
	return acc
}
