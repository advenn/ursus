package ursus

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// A frame's NaN and its gaps (step 301, C4 of v0.6.7-scope.md): Polars' drop_nans and
// interpolate. A strategy's limit, fill_null(strategy=, limit=), is FillStrategy's
// Limit, and the frame's FillNull takes it.

// DropNans drops the rows holding a NaN in any of the named columns, or in any float
// column when none is named: Polars' drop_nans. A null is not a NaN, and its row is
// kept.
//
// A named column of integers holds no NaN, and drops nothing, as Polars'; a named
// column of another type, text, a Boolean, a date or a Decimal, is refused, as
// Polars refuses is_nan of one. It is a Filter, so it streams.
func (lf *LazyFrame) DropNans(subset ...string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	var floats []string
	if len(subset) == 0 {
		for _, f := range s.All() {
			if f.Type.IsFloat() {
				floats = append(floats, f.Name)
			}
		}
	}
	for _, name := range subset {
		i := s.IndexOf(name)
		if i < 0 {
			return &LazyFrame{err: uerr.UnknownColumn("drop_nans", name, s.Names())}
		}
		switch t := s.Field(i).Type; {
		case t.IsFloat():
			floats = append(floats, name)
		case t.IsInteger():
			// No integer is a NaN.
		default:
			return &LazyFrame{err: uerr.New(uerr.KindType, "drop_nans",
				"column %q is %s, which has no NaN", name, t).
				Hint("only a float is ever NaN; a null is dropped by DropNulls")}
		}
	}
	if len(floats) == 0 {
		return lf
	}
	keep := make([]Expr, len(floats))
	for i, name := range floats {
		c := Col(name)
		// A null's IsNan is null, which a Filter would drop: a null is kept.
		keep[i] = c.IsNull().Or(c.IsNan().Not())
	}
	return lf.Filter(keep...)
}

// Interpolate fills each run of nulls in a column between two values with the
// points on the straight line between them, by row, every column it can: Polars'
// interpolate. A run before a column's first value or after its last stays null, and
// NaN is a value, so a line to one is NaN.
//
// Numbers and instants are interpolated, as Polars interpolates them: an integer,
// an Int128 too, answers a Float64, a float keeps its width, and a date, a datetime,
// a duration or a time keeps its type, a point between two days truncated to the
// earlier. A Decimal answers a Float64, as Expr.Interpolate's does, where Polars'
// keeps the Decimal. Text, Booleans, lists and structs are left as they are.
//
// Each column is Expr.Interpolate, an ordered window over the whole frame, so it
// holds its input, and past a memory limit spills it.
func (lf *LazyFrame) Interpolate() *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	var lines []Expr
	for _, f := range s.All() {
		c := Col(f.Name)
		switch {
		case f.Type.ID() == dtype.TypeInt128:
			// Expr.Interpolate reads 64-bit ticks and numbers; Polars' answer is a
			// Float64, which the cast gives first.
			lines = append(lines, c.Cast(Float64).Interpolate().Alias(f.Name))
		case f.Type.IsNumeric() || f.Type.IsTemporal():
			lines = append(lines, c.Interpolate())
		}
	}
	if len(lines) == 0 {
		return lf
	}
	return lf.WithColumns(lines...)
}

// DropNans and Interpolate are a DataFrame's: LazyFrame's.
func (df *DataFrame) DropNans(ctx context.Context, subset ...string) (*DataFrame, error) {
	return df.Lazy().DropNans(subset...).Collect(ctx)
}

func (df *DataFrame) Interpolate(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Interpolate().Collect(ctx)
}
