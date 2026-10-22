package ursus

import (
	"context"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// A Series' operations (step 319, V3 of v0.6.7-scope.md): Polars' Series.

// Series is a typed view over a Column, with the operations a user reaches for on one
// column, as Polars' Series has them: Sort, Unique, Reverse, FillNull, DropNulls, Cast
// and the aggregates. It holds no storage of its own: an operation runs the column
// through the engine as a one-column frame, and gives a Series, or a value, of the
// answer. Head, Tail, Slice and Rename share the column.
type Series[T any] struct {
	*data.Series[T]
}

// seriesOf is a Series over c, or an error if T does not read it.
func seriesOf[T any](c *Column) (*Series[T], error) {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return nil, err
	}
	return &Series[T]{s}, nil
}

// run is one expression of the series' column, as a one-column frame's, and its
// answer's one column.
func (s *Series[T]) run(ctx context.Context, op string, fn func(Expr) Expr) (*Column, error) {
	c := s.Column()
	out, err := Frame(c).Select(fn(Col(c.Name()))).Collect(ctx)
	if err != nil {
		return nil, uerr.Annotate(err, "series", op)
	}
	return out.ColumnAt(0), nil
}

// same is run, its answer read as a T.
func (s *Series[T]) same(ctx context.Context, op string, fn func(Expr) Expr) (*Series[T], error) {
	c, err := s.run(ctx, op, fn)
	if err != nil {
		return nil, err
	}
	return seriesOf[T](c)
}

// one is run, an aggregate's one value read as a U, false where it is null.
func one[U, T any](ctx context.Context, s *Series[T], op string, fn func(Expr) Expr) (U, bool, error) {
	var zero U
	c, err := s.run(ctx, op, fn)
	if err != nil {
		return zero, false, err
	}
	v, err := data.TypedColumn[U](c)
	if err != nil {
		return zero, false, uerr.Annotate(err, "series", op)
	}
	if v.Len() != 1 {
		return zero, false, uerr.Internalf("series: %s answered %d rows", op, v.Len())
	}
	x, ok := v.Get(0)
	return x, ok, nil
}

// Sort is the values in ascending order, nulls first, as Polars' Series.sort; SortDesc
// descending.
func (s *Series[T]) Sort(ctx context.Context) (*Series[T], error) {
	return s.same(ctx, "sort", Expr.Sort)
}

func (s *Series[T]) SortDesc(ctx context.Context) (*Series[T], error) {
	return s.same(ctx, "sort", Expr.SortDesc)
}

// Unique is the distinct values, in the order each first appears; a null is one of
// them, once.
func (s *Series[T]) Unique(ctx context.Context) (*Series[T], error) {
	return s.same(ctx, "unique", Expr.Unique)
}

// Reverse is the values in the opposite order.
func (s *Series[T]) Reverse(ctx context.Context) (*Series[T], error) {
	return s.same(ctx, "reverse", Expr.Reverse)
}

// DropNulls is the values that are not null.
func (s *Series[T]) DropNulls(ctx context.Context) (*Series[T], error) {
	return s.same(ctx, "drop_nulls", Expr.DropNulls)
}

// FillNull is the values with each null filled by a strategy, as Expr.FillNull fills
// them: FillForward, FillBackward, FillMin and the others.
func (s *Series[T]) FillNull(ctx context.Context, strategy FillStrategy) (*Series[T], error) {
	return s.same(ctx, "fill_null", func(e Expr) Expr { return e.FillNull(strategy) })
}

// Head is the first n values, Tail the last n, and Slice the length values from
// offset, a negative offset counting from the end, each sharing the column.
func (s *Series[T]) Head(n int) *Series[T] { return s.Slice(0, max(n, 0)) }

func (s *Series[T]) Tail(n int) *Series[T] { return s.Slice(-max(n, 0), max(n, 0)) }

func (s *Series[T]) Slice(offset, length int) *Series[T] {
	n := s.Len()
	if offset < 0 {
		// Polars' window: a start before the first value cuts the length.
		offset += n
		if offset < 0 {
			length += offset
			offset = 0
		}
	}
	offset = min(offset, n)
	length = max(min(length, n-offset), 0)
	out, _ := seriesOf[T](s.Column().Slice(offset, length))
	return out
}

// Rename is the series under another name.
func (s *Series[T]) Rename(name string) *Series[T] {
	out, _ := seriesOf[T](s.Column().Rename(name))
	return out
}

// Cast is the values as the type dt, as Expr.Cast casts them, read as a U:
//
//	f, err := s.Cast[float64](ctx, ursus.Float64)
func (s *Series[T]) Cast[U any](ctx context.Context, dt DataType) (*Series[U], error) {
	c, err := s.run(ctx, "cast", func(e Expr) Expr { return e.Cast(dt) })
	if err != nil {
		return nil, err
	}
	return seriesOf[U](c)
}

// Min and Max are the least and the greatest value, false when there is none.
func (s *Series[T]) Min(ctx context.Context) (T, bool, error) { return one[T](ctx, s, "min", Expr.Min) }

func (s *Series[T]) Max(ctx context.Context) (T, bool, error) { return one[T](ctx, s, "max", Expr.Max) }

// Sum is the values' sum, in the series' own type, as Polars' Series.sum gives it: an
// integer sum past the type is an error, where Polars' wraps.
func (s *Series[T]) Sum(ctx context.Context) (T, error) {
	v, _, err := one[T](ctx, s, "sum", func(e Expr) Expr { return e.Sum().Cast(s.DType()) })
	return v, err
}

// Mean and Median are the values' mean and median, and Std and Var their standard
// deviation and variance with ddof degrees of freedom, Polars' default 1, as
// Expr.Std takes them; false when there are too few values.
func (s *Series[T]) Mean(ctx context.Context) (float64, bool, error) {
	return one[float64](ctx, s, "mean", func(e Expr) Expr { return e.Mean().Cast(Float64) })
}

func (s *Series[T]) Median(ctx context.Context) (float64, bool, error) {
	return one[float64](ctx, s, "median", func(e Expr) Expr { return e.Median().Cast(Float64) })
}

func (s *Series[T]) Std(ctx context.Context, ddof int) (float64, bool, error) {
	return one[float64](ctx, s, "std", func(e Expr) Expr { return e.Std(ddof).Cast(Float64) })
}

func (s *Series[T]) Var(ctx context.Context, ddof int) (float64, bool, error) {
	return one[float64](ctx, s, "var", func(e Expr) Expr { return e.Var(ddof).Cast(Float64) })
}

// Count is how many values are not null, and NUnique how many are distinct, a null
// not counted, as ursus's NUnique counts them.
func (s *Series[T]) Count(ctx context.Context) (int, error) {
	v, _, err := one[int64](ctx, s, "count", func(e Expr) Expr { return e.Count().Cast(Int64) })
	return int(v), err
}

func (s *Series[T]) NUnique(ctx context.Context) (int, error) {
	v, _, err := one[int64](ctx, s, "n_unique", func(e Expr) Expr { return e.NUnique().Cast(Int64) })
	return int(v), err
}
