package ursus

import (
	"reflect"
	"strings"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// decodeRows converts a batch into a slice of Go structs.
//
// # Reflection is paid once, not per row
//
// The struct-to-column mapping is resolved ONCE into a positional plan — field
// index, column index, and a typed setter — and the per-row loop then just calls
// setters. A naive implementation that reflects per field per row is roughly two
// orders of magnitude slower, and it is the reason people believe reflection-based
// decoding is inherently slow.
//
// Fields are matched by the `ursus:"name"` tag, falling back to the field name and
// then to a case-insensitive match. Unexported fields and `ursus:"-"` are skipped;
// a field with no matching column is an error, because silently leaving it zero is
// how a typo becomes a wrong number in a report.
func decodeRows[T any](b *data.Batch) ([]T, error) {
	var zero T
	rt := reflect.TypeOf(zero)
	if rt == nil || rt.Kind() != reflect.Struct {
		return nil, uerr.New(uerr.KindType, "rows",
			"CollectInto requires a struct type, got %T", zero)
	}

	setters, err := buildSetters(rt, b)
	if err != nil {
		return nil, err
	}

	out := make([]T, b.Rows())
	for i := range out {
		rv := reflect.ValueOf(&out[i]).Elem()
		for _, s := range setters {
			s.set(rv.Field(s.field), i)
		}
	}
	return out, nil
}

type setter struct {
	field int
	set   func(dst reflect.Value, row int)
}

func buildSetters(rt reflect.Type, b *data.Batch) ([]setter, error) {
	schema := b.Schema()
	out := make([]setter, 0, rt.NumField())

	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name, skip := columnName(f)
		if skip {
			continue
		}

		ci := schema.IndexOf(name)
		if ci < 0 {
			// Case-insensitive fallback: Go fields are CamelCase and columns are
			// usually snake_case, so requiring an exact match would mean a tag on
			// every field.
			for j, sf := range schema.All() {
				if strings.EqualFold(sf.Name, name) {
					ci = j
					break
				}
			}
		}
		if ci < 0 {
			return nil, uerr.UnknownColumn("rows", name, schema.Names()).
				Hint("field %s.%s has no matching column; tag it with `ursus:\"-\"` to skip it",
					rt.Name(), f.Name)
		}

		set, err := makeSetter(f.Type, b.Column(ci))
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindType, "rows",
				"field %s.%s", rt.Name(), f.Name)
		}
		out = append(out, setter{field: i, set: set})
	}
	return out, nil
}

func columnName(f reflect.StructField) (name string, skip bool) {
	tag, ok := f.Tag.Lookup("github.com/advenn/ursus")
	if !ok {
		return f.Name, false
	}
	head, _, _ := strings.Cut(tag, ",")
	switch head {
	case "-":
		return "", true
	case "":
		return f.Name, false
	default:
		return head, false
	}
}

// makeSetter resolves the column accessor once and returns a closure over it.
func makeSetter(ft reflect.Type, c *data.Column) (func(reflect.Value, int), error) {
	// A pointer field is how a caller asks to see nulls: nil means null.
	if ft.Kind() == reflect.Pointer {
		inner, err := makeSetter(ft.Elem(), c)
		if err != nil {
			return nil, err
		}
		elem := ft.Elem()
		return func(dst reflect.Value, row int) {
			if !c.IsValid(row) {
				dst.SetZero()
				return
			}
			p := reflect.New(elem)
			inner(p.Elem(), row)
			dst.Set(p)
		}, nil
	}

	switch ft.Kind() {
	case reflect.String:
		s, err := data.TypedColumn[string](c)
		if err != nil {
			return nil, err
		}
		return func(dst reflect.Value, row int) {
			v, _ := s.Get(row)
			dst.SetString(v)
		}, nil

	case reflect.Bool:
		s, err := data.TypedColumn[bool](c)
		if err != nil {
			return nil, err
		}
		return func(dst reflect.Value, row int) {
			v, _ := s.Get(row)
			dst.SetBool(v)
		}, nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// time.Duration is an int64 with a name; treat it as one.
		return intSetter(c)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return uintSetter(c)

	case reflect.Float32, reflect.Float64:
		return floatSetter(c)

	case reflect.Struct:
		if ft == reflect.TypeOf(time.Time{}) {
			return timeSetter(c)
		}
	}

	return nil, uerr.New(uerr.KindType, "", "no decoder for Go type %s from column %s",
		ft, c.DType())
}

func intSetter(c *data.Column) (func(reflect.Value, int), error) {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return numSetter[int8](c, func(d reflect.Value, v int8) { d.SetInt(int64(v)) })
	case dtype.TypeInt16:
		return numSetter[int16](c, func(d reflect.Value, v int16) { d.SetInt(int64(v)) })
	case dtype.TypeInt32:
		return numSetter[int32](c, func(d reflect.Value, v int32) { d.SetInt(int64(v)) })
	case dtype.TypeInt64:
		return numSetter[int64](c, func(d reflect.Value, v int64) { d.SetInt(v) })
	default:
		return nil, uerr.New(uerr.KindType, "", "column is %s, not an integer", c.DType())
	}
}

func uintSetter(c *data.Column) (func(reflect.Value, int), error) {
	switch c.DType().Physical().ID() {
	case dtype.TypeUint8:
		return numSetter[uint8](c, func(d reflect.Value, v uint8) { d.SetUint(uint64(v)) })
	case dtype.TypeUint16:
		return numSetter[uint16](c, func(d reflect.Value, v uint16) { d.SetUint(uint64(v)) })
	case dtype.TypeUint32:
		return numSetter[uint32](c, func(d reflect.Value, v uint32) { d.SetUint(uint64(v)) })
	case dtype.TypeUint64:
		return numSetter[uint64](c, func(d reflect.Value, v uint64) { d.SetUint(v) })
	default:
		return nil, uerr.New(uerr.KindType, "", "column is %s, not an unsigned integer", c.DType())
	}
}

func floatSetter(c *data.Column) (func(reflect.Value, int), error) {
	switch c.DType().Physical().ID() {
	case dtype.TypeFloat32:
		return numSetter[float32](c, func(d reflect.Value, v float32) { d.SetFloat(float64(v)) })
	case dtype.TypeFloat64:
		return numSetter[float64](c, func(d reflect.Value, v float64) { d.SetFloat(v) })
	default:
		return nil, uerr.New(uerr.KindType, "", "column is %s, not a float", c.DType())
	}
}

func numSetter[T data.Fixed](c *data.Column, assign func(reflect.Value, T)) (func(reflect.Value, int), error) {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return nil, err
	}
	return func(dst reflect.Value, row int) {
		v, _ := s.Get(row)
		assign(dst, v)
	}, nil
}

func timeSetter(c *data.Column) (func(reflect.Value, int), error) {
	dt := c.DType()
	if dt.ID() != dtype.TypeDatetime && dt.ID() != dtype.TypeDate {
		return nil, uerr.New(uerr.KindType, "", "column is %s, not temporal", dt)
	}

	// The scale factor is resolved once here rather than branched on per row.
	var toNanos int64 = 1
	switch dt.TimeUnit() {
	case dtype.Second:
		toNanos = int64(time.Second)
	case dtype.Milli:
		toNanos = int64(time.Millisecond)
	case dtype.Micro:
		toNanos = int64(time.Microsecond)
	}
	if dt.ID() == dtype.TypeDate {
		toNanos = int64(24 * time.Hour)
	}

	loc := time.UTC
	if tz := dt.TimeZone(); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}

	if dt.ID() == dtype.TypeDate {
		s, err := data.TypedColumn[int32](c)
		if err != nil {
			return nil, err
		}
		return func(dst reflect.Value, row int) {
			v, _ := s.Get(row)
			dst.Set(reflect.ValueOf(time.Unix(0, int64(v)*toNanos).In(loc)))
		}, nil
	}

	s, err := data.TypedColumn[int64](c)
	if err != nil {
		return nil, err
	}
	return func(dst reflect.Value, row int) {
		v, _ := s.Get(row)
		dst.Set(reflect.ValueOf(time.Unix(0, v*toNanos).In(loc)))
	}, nil
}
