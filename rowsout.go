package ursus

import (
	"context"
	"iter"
	"reflect"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// Rows out (step 318, V2 of v0.6.7-scope.md): Polars' rows_by_key, to_dicts and
// iter_rows.

// RowsByKey is the frame's rows, each a T as Rows decodes it, grouped by the value of
// the column key, a K, as Polars' rows_by_key(key, named=True) groups them: a key's
// rows in the frame's order. K is read from the column as a field of its type would
// be, so an Int64 key reads into an int or an int64. A null key's rows are under K's
// zero value; T's fields name the columns it reads, the key's among them or not.
//
//	byRegion, err := df.RowsByKey[string, sale]("region")
func (df *DataFrame) RowsByKey[K comparable, T any](key string) (map[K][]T, error) {
	rows, err := decodeRows[T](df.batch)
	if err != nil {
		return nil, err
	}
	i := df.batch.Schema().IndexOf(key)
	if i < 0 {
		return nil, uerr.UnknownColumn("rows_by_key", key, df.Columns())
	}
	set, err := makeSetter(reflect.TypeFor[K](), df.batch.Column(i))
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindType, "rows_by_key", "key %q", key)
	}
	out := map[K][]T{}
	var k K
	kv := reflect.ValueOf(&k).Elem()
	for r := range rows {
		set(kv, r) // a null as K's zero, as every setter reads one
		out[k] = append(out[k], rows[r])
	}
	return out, nil
}

// IterRows streams the query's rows, each a T as Rows decodes it, a batch at a time,
// as Polars' iter_rows(named=True) walks a frame's: the query runs as CollectBatches
// runs it, holding a batch, so a result larger than memory can be read row by row.
// An error ends the walk, yielded with T's zero value.
//
//	for row, err := range lf.IterRows[sale](ctx) { ... }
func (lf *LazyFrame) IterRows[T any](ctx context.Context, opts ...CollectOption) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		for df, err := range lf.CollectBatches(ctx, opts...) {
			if err != nil {
				yield(zero, err)
				return
			}
			rows, err := decodeRows[T](df.batch)
			if err != nil {
				yield(zero, err)
				return
			}
			for _, r := range rows {
				if !yield(r, nil) {
					return
				}
			}
		}
	}
}

// ToMaps is the frame's rows, each a map from its columns' names to their values, as
// Polars' to_dicts: an integer or a float its Go type, an Int64 an int64; text and an
// Enum a string; a Binary a []byte; a Boolean a bool; a Date or a Datetime a
// time.Time in its zone; a Duration a time.Duration; a Time the time.Duration since
// midnight; a Decimal its text; a List a []any; a Struct a map[string]any; a null nil.
func (df *DataFrame) ToMaps() ([]map[string]any, error) {
	n := df.Height()
	names := df.Columns()
	readers := make([]func(int) any, len(names))
	for j := range names {
		r, err := cellReader(df.batch.Column(j))
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindType, "to_maps", "column %q", names[j])
		}
		readers[j] = r
	}
	out := make([]map[string]any, n)
	for i := range out {
		m := make(map[string]any, len(names))
		for j, name := range names {
			m[name] = readers[j](i)
		}
		out[i] = m
	}
	return out, nil
}

// cellReader reads a column's values as ToMaps gives them, the type switch paid once.
func cellReader(c *data.Column) (func(int) any, error) {
	read, err := valueReader(c)
	if err != nil {
		return nil, err
	}
	return func(i int) any {
		if !c.IsValid(i) {
			return nil
		}
		return read(i)
	}, nil
}

func valueReader(c *data.Column) (func(int) any, error) {
	dt := c.DType()
	switch dt.ID() {
	case dtype.TypeNull:
		return func(int) any { return nil }, nil
	case dtype.TypeBool:
		return typedReader[bool](c)
	case dtype.TypeString, dtype.TypeEnum, dtype.TypeCategorical:
		return typedReader[string](c)
	case dtype.TypeBinary:
		acc := c.Strings()
		return func(i int) any { return []byte(acc.Get(i)) }, nil
	case dtype.TypeInt8:
		return typedReader[int8](c)
	case dtype.TypeInt16:
		return typedReader[int16](c)
	case dtype.TypeInt32:
		return typedReader[int32](c)
	case dtype.TypeInt64:
		return typedReader[int64](c)
	case dtype.TypeInt128:
		return typedReader[i128.Int128](c)
	case dtype.TypeUint8:
		return typedReader[uint8](c)
	case dtype.TypeUint16:
		return typedReader[uint16](c)
	case dtype.TypeUint32:
		return typedReader[uint32](c)
	case dtype.TypeUint64:
		return typedReader[uint64](c)
	case dtype.TypeFloat32:
		return typedReader[float32](c)
	case dtype.TypeFloat64:
		return typedReader[float64](c)
	case dtype.TypeDate, dtype.TypeDatetime:
		var t time.Time
		set, err := timeSetter(c)
		if err != nil {
			return nil, err
		}
		v := reflect.ValueOf(&t).Elem()
		return func(i int) any { set(v, i); return t }, nil
	case dtype.TypeDuration, dtype.TypeTime:
		ticks, err := data.Values[int64](c)
		if err != nil {
			return nil, err
		}
		npt, _ := dt.NanosPerTick()
		return func(i int) any { return time.Duration(ticks[i] * npt) }, nil
	case dtype.TypeDecimal:
		text, err := kernel.Cast(c.Name(), dtype.String, true, c)
		if err != nil {
			return nil, err
		}
		return typedReader[string](text)
	case dtype.TypeList, dtype.TypeArray:
		lists := c.Lists()
		elem, err := cellReader(lists.Child())
		if err != nil {
			return nil, err
		}
		return func(i int) any {
			lo, hi, _ := lists.Get(i)
			out := make([]any, 0, hi-lo)
			for j := lo; j < hi; j++ {
				out = append(out, elem(int(j)))
			}
			return out
		}, nil
	case dtype.TypeStruct:
		fields := c.Fields()
		readers := make([]func(int) any, len(fields))
		for k, f := range fields {
			r, err := cellReader(f)
			if err != nil {
				return nil, err
			}
			readers[k] = r
		}
		return func(i int) any {
			m := make(map[string]any, len(fields))
			for k, f := range fields {
				m[f.Name()] = readers[k](i)
			}
			return m
		}, nil
	}
	return nil, uerr.New(uerr.KindType, "", "no Go value for a %s", dt)
}

func typedReader[T any](c *data.Column) (func(int) any, error) {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return nil, err
	}
	return func(i int) any { v, _ := s.Get(i); return v }, nil
}
