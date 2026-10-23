package ursus

import (
	"context"
	"reflect"
	"unsafe"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// Polars' frame leftovers (step 328): map_rows, map_columns, show, and a lazy frame's
// cache.

// MapRows is fn of each row, as Polars' map_rows: the frame's rows each a T, and fn's
// answers a frame. A T that is a struct is read as Rows reads one, a field a column
// by its name or tag; a T that is []any is the row's values in the columns' order, as
// Row gives them. An R that is a struct is a column a field, as FromRows builds them;
// any other R is one column named "map", as Polars names a function's one answer: a
// pointer nil a null, a slice a List. fn runs on one goroutine, in the rows' order,
// and an error from it ends the walk, naming the row.
//
//	type in struct{ Qty int64; Price float64 }
//	revenue, err := df.MapRows(ctx, func(r in) (float64, error) { return float64(r.Qty) * r.Price, nil })
//
// An expression is faster: MapRows decodes every row and calls fn once a row, as
// Polars' map_rows calls Python.
func (df *DataFrame) MapRows[T, R any](ctx context.Context, fn func(T) (R, error)) (*DataFrame, error) {
	rows, err := rowsAs[T](df)
	if err != nil {
		return nil, err
	}
	out := make([]R, len(rows))
	for i, r := range rows {
		if i&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if out[i], err = fn(r); err != nil {
			return nil, uerr.Wrap(err, uerr.KindValue, "map_rows", "the function failed at row %d", i)
		}
	}
	rt := reflect.TypeFor[R]()
	if rt.Kind() == reflect.Struct && rt != timeType {
		return FromRows(out).Collect(ctx)
	}
	if rt.Kind() == reflect.Interface {
		return nil, uerr.New(uerr.KindType, "map_rows", "MapRows cannot type a column of %s", rt).
			Hint("return a concrete type, a struct for several columns")
	}
	// One column, "map": R read as the one field of a struct laid out as R is.
	one := reflect.StructOf([]reflect.StructField{{Name: "Map", Type: rt, Tag: `ursus:"map"`}})
	at := loc{rows: len(out), stride: rt.Size()}
	if len(out) > 0 {
		at.base = unsafe.Pointer(&out[0])
	}
	cols, err := structColumns(one, at)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindType, "map_rows", "the answers, %s", rt)
	}
	return frameOfColumns(cols, len(out))
}

var anyRow = reflect.TypeFor[[]any]()

// rowsAs is the frame's rows as Ts: structs as Rows decodes them, or []any, a row's
// values as Row reads them, every row's in one allocation.
func rowsAs[T any](df *DataFrame) ([]T, error) {
	if rt := reflect.TypeFor[T](); rt != anyRow {
		if rt.Kind() != reflect.Struct || rt == timeType {
			return nil, uerr.New(uerr.KindType, "map_rows", "MapRows reads a row as a struct or a []any, not %s", rt).
				Hint("a struct of a field a column, named by its name or its ursus tag")
		}
		rows, err := decodeRows[T](df.batch)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindType, "map_rows", "the rows as %s", rt)
		}
		return rows, nil
	}
	w := df.Width()
	readers := make([]func(int) any, w)
	for j, c := range df.batch.Columns() {
		var err error
		if readers[j], err = cellReader(c); err != nil {
			return nil, err
		}
	}
	out := make([]T, df.Height())
	flat := make([]any, df.Height()*w)
	for i := range out {
		row := flat[i*w : (i+1)*w : (i+1)*w]
		for j, read := range readers {
			row[j] = read(i)
		}
		out[i] = any(row).(T)
	}
	return out, nil
}

// MapColumns is the frame with each column named replaced by fn of it, as Polars'
// map_columns, for what only Go can say of a whole column: the answer keeps the
// column's name and place, and is as long as the frame, or one value long, which is
// repeated. A selector's names are its Expand of the frame's schema.
func (df *DataFrame) MapColumns(ctx context.Context, names []string, fn func(*Column) (*Column, error)) (*DataFrame, error) {
	cols := append([]*data.Column(nil), df.batch.Columns()...)
	for _, name := range names {
		j := df.batch.Schema().IndexOf(name)
		if j < 0 {
			return nil, uerr.UnknownColumn("map_columns", name, df.Columns())
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := fn(cols[j])
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindValue, "map_columns", "the function failed on %q", name)
		}
		if out == nil {
			return nil, uerr.New(uerr.KindValue, "map_columns", "the function gave no column for %q", name)
		}
		switch out.Len() {
		case df.Height():
		case 1:
			if out, err = kernel.Take(out, make([]int32, df.Height())); err != nil {
				return nil, err
			}
		default:
			return nil, uerr.New(uerr.KindSchema, "map_columns",
				"the function's answer for %q has %d rows, and the frame %d", name, out.Len(), df.Height()).
				Hint("answer a column as long as the frame, or one value")
		}
		cols[j] = out.Rename(name)
	}
	return frameOfColumns(cols, df.Height())
}

// Show is the frame's first limit rows as a table, every one of them drawn, where
// String draws ten, as Polars' show prints them; a negative limit leaves the last
// -limit rows out, and Show(df.Height()) draws them all. It is the text to print,
// as Glimpse's is.
func (df *DataFrame) Show(limit int) string {
	k := limit
	if k < 0 {
		k = max(0, df.Height()+limit)
	}
	k = min(k, df.Height())
	return df.table(k, k)
}

// Show is the query's first limit rows as a table, as DataFrame's Show draws them,
// Polars' LazyFrame.show: only those rows are computed, unless limit is negative.
func (lf *LazyFrame) Show(ctx context.Context, limit int) (string, error) {
	q := lf
	if limit >= 0 {
		q = lf.Head(limit)
	}
	df, err := q.Collect(ctx)
	if err != nil {
		return "", err
	}
	return df.Show(limit), nil
}

// Cache marks the query to run once where it is used at several places, as Polars'
// cache: a query reused, joined with itself or in two arms of a Concat, computes its
// answer once and every use reads it. ursus does so unasked for a reused query that
// joins, aggregates or sorts; Cache asks it for any other, a scan read twice or a Go
// function's answer. A query used once is not cached, as Polars drops its cache. A
// cached query is a fence: a filter or a projection above one use stops at it.
func (lf *LazyFrame) Cache() *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if c, ok := lf.node.(*plan.Cache); ok && c.ID == 0 {
		return lf
	}
	return lf.derive(&plan.Cache{Input: lf.node})
}
