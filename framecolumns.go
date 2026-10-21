package ursus

import (
	"context"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// A column put in its place, and a row's hash (step 302, C5 of v0.6.7-scope.md):
// Polars' DataFrame.insert_column and hash_rows.

// InsertColumn puts a column at index i, an expression computed over the frame or Go
// values, as Polars' DataFrame.insert_column takes an Expr or a Series. The columns
// from i on move one place right: i of the frame's width appends, and a negative i
// counts from the width, -1 the place before the last column, as Polars counts it.
//
//	lf.InsertColumn(0, ursus.Col("qty").Mul(ursus.Col("price")).Alias("revenue"))
//	lf.InsertColumn(-1, ursus.Values("note", notes))
//
// An index past either end is refused, as Polars refuses it, and so is a name the
// frame already has. An expression is one column, and an aggregate or a literal is
// broadcast to every row, as in WithColumns. Go values are as many as the frame's
// rows, or the query fails, as HStack's does; their frame is held, as HStack holds
// it.
func (lf *LazyFrame) InsertColumn[C Expr | *Column](i int, c C) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	width := s.Len()
	at := i
	if at < 0 {
		at += width
	}
	if at < 0 || at > width {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "insert_column",
			"column index %d is out of range: the frame has %d columns", i, width).
			Hint("an index runs from -%d to %d, the width appending", width, width)}
	}
	var with *LazyFrame
	var name string
	switch v := any(c).(type) {
	case Expr:
		out, err := lf.Select(v).schemaNow()
		if err != nil {
			return &LazyFrame{err: err}
		}
		if out.Len() != 1 {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "insert_column",
				"InsertColumn inserts one column, and the expression gives %d", out.Len()).
				Hint("insert each column on its own, or add them with WithColumns")}
		}
		name, with = out.Field(0).Name, lf.WithColumns(v)
	case *Column:
		if v == nil {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "insert_column", "the column is nil")}
		}
		name, with = v.Name(), lf.HStack(Frame(v))
	}
	if s.IndexOf(name) >= 0 {
		return &LazyFrame{err: uerr.New(uerr.KindSchema, "insert_column",
			"the frame has a column %q already", name).
			Hint("alias the new column, or replace the old one with WithColumns")}
	}
	names := s.Names()
	order := make([]Expr, 0, width+1)
	for k := 0; k <= width; k++ {
		if k == at {
			order = append(order, Col(name))
		}
		if k < width {
			order = append(order, Col(names[k]))
		}
	}
	return with.Select(order...)
}

// InsertColumn puts a column in a DataFrame: LazyFrame's.
func (df *DataFrame) InsertColumn[C Expr | *Column](ctx context.Context, i int, c C) (*DataFrame, error) {
	return df.Lazy().InsertColumn(i, c).Collect(ctx)
}

// HashRows is each row's 64-bit hash over the given columns, every column when none
// is given, seeded, a UInt64: Polars' DataFrame.hash_rows, here an expression, as
// Polars' pl.struct(...).hash() is one.
//
// Rows a group-by takes as one hash alike: -0 with 0, NaN with NaN, and a row of
// nulls has a hash of its own. The columns' order counts. The algorithm is Hash's,
// over the row's group-by key, so the numbers are not Polars' (v0.6.6-scope.md §6.3);
// one seed takes four in Polars. A List or a Struct column is refused, being no
// group-by key here, where Polars hashes one. The answer is named after its first
// column; alias it.
func HashRows(seed uint64, exprs ...Expr) Expr {
	if len(exprs) == 0 {
		exprs = []Expr{All()}
	}
	return variadic("hash_rows", exprs, func(ns []expr.Node) (expr.Node, error) {
		return &expr.Call{Fn: expr.FnHashRows, Args: append(ns, litNode(int64(seed)))}, nil
	})
}

// HashRows is each row's hash over every column, seeded, as Polars'
// DataFrame.hash_rows answers it: a Series of UInt64, named "" as Polars names it.
func (df *DataFrame) HashRows(ctx context.Context, seed uint64) (*Series[uint64], error) {
	out, err := df.Select(ctx, HashRows(seed).Alias(""))
	if err != nil {
		return nil, err
	}
	return out.Column[uint64]("")
}
