package ursus

import (
	"context"

	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// The row shortcuts of a frame (step 293, R5 of v0.6.7-scope.md): Polars' shift,
// gather_every, clear, first, last, is_empty and equals.

// schemaNow is the frame's schema, resolved as the plan is built, for a shortcut
// that writes one expression a column.
func (lf *LazyFrame) schemaNow() (*dtype.Schema, error) {
	resolved, err := plan.Resolve(context.Background(), lf.node)
	if err != nil {
		return nil, err
	}
	return resolved.Schema()
}

// Shift moves every column's values n rows later, a negative n earlier, nulls where
// the rows were vacated: Polars' DataFrame.shift(n). It is Expr.Shift of each column,
// a window function, so it streams.
func (lf *LazyFrame) Shift(n int) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	return lf.Select(All().Shift(n))
}

// ShiftFill is Shift with fill in the vacated rows: Polars' shift(n, fill_value). The
// fill meets each column as Expr.ShiftFill's does, a Go value of a type that fits a
// column's taking the column's type, so 0 into an Int32 column is an Int32 and 1.5 into
// an Int64 one makes it a Float64. A text column takes the fill's text, "0", as
// Polars' does. A column the fill cannot meet, a number column with a text fill, is
// refused naming it, where Polars makes the column text.
func (lf *LazyFrame) ShiftFill[T Operand](n int, fill T) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	node := lift(fill)
	f := wrap(node)
	lit, isLit := node.(*expr.Lit)
	cols := make([]Expr, 0, s.Len())
	for _, fd := range s.All() {
		c := Col(fd.Name)
		switch {
		case fd.Type == dtype.String:
			cols = append(cols, c.ShiftFill(n, f.Cast(String)).Alias(fd.Name))
			continue
		case isLit && lit.DT == dtype.String:
			return &LazyFrame{err: uerr.New(uerr.KindType, "shift",
				"a text fill does not meet column %q of %s", fd.Name, fd.Type).
				Hint("Polars makes the column text; shift its text columns alone, or cast the column first")}
		}
		cols = append(cols, c.ShiftFill(n, fill).Alias(fd.Name))
	}
	return lf.Select(cols...)
}

// GatherEvery is every nth row from offset: Polars' gather_every(n, offset). n must be
// positive. It is a filter by the row's index, so it streams.
func (lf *LazyFrame) GatherEvery(n, offset int) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if n <= 0 || offset < 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "gather_every",
			"gather_every takes a positive n and an offset of at least 0, got %d and %d", n, offset)}
	}
	const idx = "\x00gather_every"
	i := Col(idx).Cast(Int64)
	return lf.WithRowIndex(idx, 0).
		Filter(i.Ge(int64(offset)).And(i.Sub(int64(offset)).Mod(int64(n)).Eq(int64(0)))).
		Drop(idx)
}

// Clear is the frame's schema with n rows of nulls, none by default: Polars'
// clear(n).
func (lf *LazyFrame) Clear(n int) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if n < 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "clear", "clear takes %d rows", n)}
	}
	if n == 0 {
		return lf.Head(0)
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	cols := make([]Expr, 0, s.Len())
	for _, fd := range s.All() {
		cols = append(cols, Repeat(Null(fd.Type), int64(n)).Alias(fd.Name))
	}
	return lf.Select(cols...)
}

// First and Last are the frame's first and last row, none for no rows: Polars'
// LazyFrame.first() and last().
func (lf *LazyFrame) First() *LazyFrame { return lf.Head(1) }
func (lf *LazyFrame) Last() *LazyFrame  { return lf.Tail(1) }

// Eager forms.

func (df *DataFrame) Shift(ctx context.Context, n int) (*DataFrame, error) {
	return df.Lazy().Shift(n).Collect(ctx)
}

func (df *DataFrame) ShiftFill[T Operand](ctx context.Context, n int, fill T) (*DataFrame, error) {
	return df.Lazy().ShiftFill(n, fill).Collect(ctx)
}

func (df *DataFrame) GatherEvery(ctx context.Context, n, offset int) (*DataFrame, error) {
	return df.Lazy().GatherEvery(n, offset).Collect(ctx)
}

func (df *DataFrame) Clear(ctx context.Context, n int) (*DataFrame, error) {
	return df.Lazy().Clear(n).Collect(ctx)
}

// IsEmpty reports whether the frame has no rows: Polars' is_empty. A frame of no
// columns has none.
func (df *DataFrame) IsEmpty() bool { return df.Height() == 0 }

// Equals reports whether other holds the same columns, by name and in order, and the
// same values row for row: Polars' DataFrame.equals(other, null_equal). Values compare
// as Polars compares them: NaN equals NaN, -0 equals 0, and a List or a Struct
// element by element. Two numbers of different types are compared at their common
// type, as Polars compares an Int64 with an Int32 or a Float64; other differing types
// are unequal. A null equals a null when nullEqual, and with it false a frame holding
// any null equals no frame, as Polars' does.
func (df *DataFrame) Equals(ctx context.Context, other *DataFrame, nullEqual bool) (bool, error) {
	a, b := df.Schema(), other.Schema()
	if a.Len() != b.Len() || df.Height() != other.Height() {
		return false, nil
	}
	left, right := df, other
	var castL, castR []Expr
	for i := range a.Len() {
		fa, fb := a.Field(i), b.Field(i)
		if fa.Name != fb.Name {
			return false, nil
		}
		ta, tb := fa.Type, fb.Type
		if ta != tb {
			common, ok := dtype.Promote(ta, tb)
			if !ok || !ta.IsNumeric() || !tb.IsNumeric() {
				return false, nil
			}
			ta, tb = common, common
		}
		castL = append(castL, Col(fa.Name).Cast(ta))
		castR = append(castR, Col(fb.Name).Cast(tb))
	}
	if len(castL) > 0 {
		var err error
		if left, err = df.Select(ctx, castL...); err != nil {
			return false, err
		}
		if right, err = other.Select(ctx, castR...); err != nil {
			return false, err
		}
	}
	ra, err := left.Record()
	if err != nil {
		return false, err
	}
	defer ra.Release()
	rb, err := right.Record()
	if err != nil {
		return false, err
	}
	defer rb.Release()
	if !ra.Schema().Equal(rb.Schema()) {
		// A column of text past 2 GiB is a LargeString where the same text in less is a
		// String (step 368): the two compared as large, alike.
		if ra, err = arrowout.RecordLarge(left.batch); err != nil {
			return false, err
		}
		defer ra.Release()
		if rb, err = arrowout.RecordLarge(right.batch); err != nil {
			return false, err
		}
		defer rb.Release()
	}
	for i := range int(ra.NumCols()) {
		x, y := ra.Column(i), rb.Column(i)
		if !nullEqual && (x.NullN() > 0 || y.NullN() > 0) {
			return false, nil
		}
		if !array.ApproxEqual(x, y, array.WithNaNsEqual(true), array.WithAbsTolerance(0)) {
			return false, nil
		}
	}
	return true, nil
}
