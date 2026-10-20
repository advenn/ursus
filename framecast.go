package ursus

import (
	"context"
	"sort"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// A frame's casts (step 298, C1 of v0.6.7-scope.md): Polars' DataFrame.cast and
// LazyFrame.cast, which take a mapping of names or types to types, or one type for
// every column. Go has no mapping of either key, so they are three methods: Cast by
// name, CastTypes by type, CastAll every column. Each is a WithColumns of Expr.Cast,
// or of Expr.CastLossy under CastStrict(false), written in the frame's column order.
//
// A cast is Expr.Cast's, so it is exact or refused where Polars' is not:
//
//   - a float with a fraction is refused as an integer, and is null when not strict,
//     where Polars truncates it toward zero;
//   - an instant cast to text is ISO 8601's, "2026-01-01T00:00:00.000000", where
//     Polars writes a space for the T.
//
// feature-gaps.md §9 records both.

// CastOption configures a frame's Cast, CastTypes and CastAll.
type CastOption func(*castCfg)

type castCfg struct{ lossy bool }

// CastStrict says what a value that does not convert becomes: an error naming it, by
// default and with true, as Expr.Cast answers; a null with false, as Expr.CastLossy
// does. Polars' strict.
func CastStrict(strict bool) CastOption { return func(c *castCfg) { c.lossy = !strict } }

// Cast casts each named column to its type, and leaves the others as they are, in
// their places: Polars' cast({"a": pl.Int32}). A name the frame does not have is
// refused, as Polars refuses it, and a column already of its type is left out of the
// plan.
//
//	lf.Cast(map[string]ursus.DataType{"qty": ursus.Int32, "price": ursus.Float32})
func (lf *LazyFrame) Cast(types map[string]DataType, opts ...CastOption) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	var unknown []string
	for name := range types {
		if s.IndexOf(name) < 0 {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return &LazyFrame{err: uerr.UnknownColumn("cast", unknown[0], s.Names())}
	}
	return lf.castEach(s, opts, func(f dtype.Field) (DataType, bool) {
		to, ok := types[f.Name]
		return to, ok
	})
}

// CastTypes casts every column of each type to another: Polars'
// cast({pl.Int64: pl.Float32}). A type the frame has no column of changes nothing.
//
// A key matches a type exactly: a Datetime key its unit and its zone, a List key its
// element. Polars' pl.Datetime, with no unit, matches every Datetime; here each one
// is its own key.
//
//	lf.CastTypes(map[ursus.DataType]ursus.DataType{ursus.Int64: ursus.Int32})
func (lf *LazyFrame) CastTypes(types map[DataType]DataType, opts ...CastOption) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	return lf.castEach(s, opts, func(f dtype.Field) (DataType, bool) {
		to, ok := types[f.Type]
		return to, ok
	})
}

// CastAll casts every column to one type: Polars' cast(pl.String).
func (lf *LazyFrame) CastAll(to DataType, opts ...CastOption) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	return lf.castEach(s, opts, func(dtype.Field) (DataType, bool) { return to, true })
}

// castEach casts each column target names a type for, in the frame's order, so the
// plan is the same whichever order a map gives its keys in.
func (lf *LazyFrame) castEach(s *dtype.Schema, opts []CastOption, target func(dtype.Field) (DataType, bool)) *LazyFrame {
	var cfg castCfg
	for _, o := range opts {
		o(&cfg)
	}
	var casts []Expr
	for _, f := range s.All() {
		to, ok := target(f)
		if !ok || to == f.Type {
			continue
		}
		c := Col(f.Name)
		if cfg.lossy {
			casts = append(casts, c.CastLossy(to))
		} else {
			casts = append(casts, c.Cast(to))
		}
	}
	if len(casts) == 0 {
		return lf
	}
	return lf.WithColumns(casts...)
}

// Cast, CastTypes and CastAll cast a DataFrame's columns: LazyFrame's.
func (df *DataFrame) Cast(ctx context.Context, types map[string]DataType, opts ...CastOption) (*DataFrame, error) {
	return df.Lazy().Cast(types, opts...).Collect(ctx)
}

func (df *DataFrame) CastTypes(ctx context.Context, types map[DataType]DataType,
	opts ...CastOption) (*DataFrame, error) {
	return df.Lazy().CastTypes(types, opts...).Collect(ctx)
}

func (df *DataFrame) CastAll(ctx context.Context, to DataType, opts ...CastOption) (*DataFrame, error) {
	return df.Lazy().CastAll(to, opts...).Collect(ctx)
}
