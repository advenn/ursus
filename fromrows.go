package ursus

import (
	"math"
	"reflect"
	"time"
	"unsafe"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// FromRows (step 317, V1 of v0.6.7-scope.md): a frame from Go structs, Rows' inverse,
// as Polars builds one from a list of dataclasses.

// FromRows builds a frame from Go structs, a column a field, in the fields' order:
//
//	type sale struct {
//		ID    int64   `ursus:"id"`
//		Price float64 `ursus:"unit_price"`
//		Note  *string // nil is null
//	}
//	lf := ursus.FromRows([]sale{{1, 9.5, nil}})
//
// A field is named by its `ursus:"name"` tag, as Rows reads it, or by its own name; an
// unexported field, and one tagged `ursus:"-"`, is left out. A field takes the column
// type Values gives its Go type: the integers, floats, bool, string, []byte a Binary,
// time.Time a Datetime(ns, UTC) and time.Duration a Duration(ns). A pointer field is
// nullable, nil a null; a slice field, other than []byte, a List of its elements, nil
// a null list; and a struct field a Struct of its own fields, named as these are.
// Any other field type is refused, naming the field.
//
// Each field is read once a row by its offset, so a frame of a million rows costs
// about what copying their fields does.
func FromRows[T any](rows []T) *LazyFrame {
	var zero T
	rt := reflect.TypeOf(zero)
	if rt == nil || rt.Kind() != reflect.Struct {
		return &LazyFrame{err: uerr.New(uerr.KindType, "from_rows", "FromRows takes structs, got %T", zero)}
	}
	at := loc{rows: len(rows), stride: rt.Size()}
	if len(rows) > 0 {
		at.base = unsafe.Pointer(&rows[0])
	}
	cols, err := structColumns(rt, at)
	if err != nil {
		return &LazyFrame{err: err}
	}
	return Frame(cols...)
}

var (
	timeType     = reflect.TypeFor[time.Time]()
	durationType = reflect.TypeFor[time.Duration]()
)

// loc locates rows values: the i-th at base + i*stride + off, or, for a list's
// elements and a pointer's targets, at ptrs[i] + off. With deref, each holds a
// pointer to the value, nil a null.
type loc struct {
	rows   int
	base   unsafe.Pointer
	stride uintptr
	ptrs   []unsafe.Pointer
	off    uintptr
	deref  bool
}

func (l loc) at(i int) unsafe.Pointer {
	if l.ptrs != nil {
		return unsafe.Add(l.ptrs[i], l.off)
	}
	return unsafe.Add(l.base, uintptr(i)*l.stride+l.off)
}

// value is where the i-th value is, nil for a null one.
func (l loc) value(i int) unsafe.Pointer {
	p := l.at(i)
	if l.deref {
		p = *(*unsafe.Pointer)(p)
	}
	return p
}

// field is the values of a field at off within each of l's.
func (l loc) field(off uintptr) loc {
	l.off += off
	return l
}

// structColumns is a column for each of a struct type's fields, of the structs l
// locates.
func structColumns(rt reflect.Type, l loc) ([]*Column, error) {
	var cols []*Column
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name, skip := columnName(f)
		if skip {
			continue
		}
		c, err := encodeField(name, f.Type, l.field(f.Offset))
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindType, "from_rows", "field %s.%s", rt.Name(), f.Name)
		}
		cols = append(cols, c)
	}
	return cols, nil
}

// encodeField is the column of the values of Go type t that l locates.
func encodeField(name string, t reflect.Type, l loc) (*Column, error) {
	if t.Kind() == reflect.Pointer && isScalar(t.Elem()) {
		// A pointer to a value: read through it in the same walk, nil a null.
		l.deref = true
		return encodeScalar(name, t.Elem(), l)
	}
	if isScalar(t) {
		return encodeScalar(name, t, l)
	}
	switch t.Kind() {
	case reflect.Pointer:
		// A pointer to a slice or a struct: its targets, a null's the zero value's.
		valid := bitmap.NewBuilder(l.rows)
		zero := reflect.New(t.Elem()).UnsafePointer()
		ptrs := make([]unsafe.Pointer, l.rows)
		for i := range ptrs {
			p := l.value(i)
			if l.deref {
				// A pointer to a pointer: one more step.
				if p != nil {
					p = *(*unsafe.Pointer)(p)
				}
			} else {
				p = *(*unsafe.Pointer)(p)
			}
			valid.Append(p != nil)
			if p == nil && t.Elem().Kind() != reflect.Array {
				// An array's null row holds no element, so its pointer stays nil.
				p = zero
			}
			ptrs[i] = p
		}
		c, err := encodeField(name, t.Elem(), loc{rows: l.rows, ptrs: ptrs})
		if err != nil {
			return nil, err
		}
		v := valid.Finish()
		if !c.Validity().IsAllSet() {
			v = bitmap.And(v, c.Validity())
		}
		return c.WithValidity(v), nil

	case reflect.Slice:
		// A List: each row's elements, end to end, located by their addresses.
		offs := make([]int32, l.rows+1)
		valid := bitmap.NewBuilder(l.rows)
		var elems []unsafe.Pointer
		size := t.Elem().Size()
		for i := range l.rows {
			p := l.value(i)
			if p == nil {
				valid.Append(false)
				offs[i+1] = int32(len(elems))
				continue
			}
			s := reflect.NewAt(t, p).Elem()
			valid.Append(!s.IsNil())
			if k := s.Len(); k > 0 {
				first := s.Index(0).Addr().UnsafePointer()
				for j := range k {
					elems = append(elems, unsafe.Add(first, uintptr(j)*size))
				}
			}
			offs[i+1] = int32(len(elems))
		}
		child, err := encodeField("item", t.Elem(), loc{rows: len(elems), ptrs: elems})
		if err != nil {
			return nil, err
		}
		return data.NewList(name, offs, child, valid.Finish()), nil

	case reflect.Array:
		// An Array of the Go array's size (step 341): each row's elements in place, a
		// null row's, behind a nil pointer, none.
		n, size := t.Len(), t.Elem().Size()
		offs := make([]int32, l.rows+1)
		valid := bitmap.NewBuilder(l.rows)
		elems := make([]unsafe.Pointer, 0, l.rows*n)
		for i := range l.rows {
			p := l.value(i)
			valid.Append(p != nil)
			for j := 0; p != nil && j < n; j++ {
				elems = append(elems, unsafe.Add(p, uintptr(j)*size))
			}
			offs[i+1] = int32(len(elems))
		}
		child, err := encodeField("item", t.Elem(), loc{rows: len(elems), ptrs: elems})
		if err != nil {
			return nil, err
		}
		return data.NewList(name, offs, child, valid.Finish()).WithDType(dtype.Array(child.DType(), n)), nil

	case reflect.Struct:
		fields, err := structColumns(t, l)
		if err != nil {
			return nil, err
		}
		return data.NewStruct(name, fields, bitmap.AllSet(l.rows)), nil
	}
	return nil, uerr.New(uerr.KindType, "from_rows", "a %s has no column type", t).
		Hint("a field is a number, a bool, a string, []byte, time.Time, time.Duration, a pointer to one, a slice, " +
			"an array or a struct of them")
}

// isScalar reports whether a value of t is one column value: a number, a bool, a
// string, a []byte, a time.Time or a time.Duration.
func isScalar(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint,
		reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.Uint8
	case reflect.Struct:
		return t == timeType || t.ConvertibleTo(timeType)
	}
	return false
}

func encodeScalar(name string, t reflect.Type, l loc) (*Column, error) {
	switch {
	case t == timeType || t.Kind() == reflect.Struct:
		return values[time.Time](name, l), nil
	case t == durationType:
		return values[time.Duration](name, l), nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return values[bool](name, l), nil
	case reflect.Int:
		return values[int](name, l), nil
	case reflect.Int8:
		return values[int8](name, l), nil
	case reflect.Int16:
		return values[int16](name, l), nil
	case reflect.Int32:
		return values[int32](name, l), nil
	case reflect.Int64:
		return values[int64](name, l), nil
	case reflect.Uint:
		return values[uint](name, l), nil
	case reflect.Uint8:
		return values[uint8](name, l), nil
	case reflect.Uint16:
		return values[uint16](name, l), nil
	case reflect.Uint32:
		return values[uint32](name, l), nil
	case reflect.Uint64:
		return values[uint64](name, l), nil
	case reflect.Float32:
		return values[float32](name, l), nil
	case reflect.Float64:
		return values[float64](name, l), nil
	case reflect.String:
		return textColumn[string](name, dtype.String, l)
	}
	return textColumn[[]byte](name, dtype.Binary, l) // a []byte, isScalar's last
}

// values is the column of the values of type V that l locates.
func values[V Literal](name string, l loc) *Column {
	vals, valid := gather[V](l)
	return valuesWith(name, vals, valid)
}

// gather reads the values of type V that l locates, and which are not null: a walk
// by the stride, by the pointers, or through each.
func gather[V any](l loc) ([]V, bitmap.View) {
	out := make([]V, l.rows)
	switch {
	case l.deref:
		valid := bitmap.NewBuilder(l.rows)
		for i := range out {
			p := l.value(i)
			valid.Append(p != nil)
			if p != nil {
				out[i] = *(*V)(p)
			}
		}
		return out, valid.Finish()
	case l.ptrs != nil:
		for i, p := range l.ptrs {
			out[i] = *(*V)(unsafe.Add(p, l.off))
		}
	case l.rows > 0:
		// A step between rows, never past the last: checkptr, under the race
		// detector, refuses a pointer one past its allocation, read or not.
		p := unsafe.Add(l.base, l.off)
		for i := range out {
			if i > 0 {
				p = unsafe.Add(p, l.stride)
			}
			out[i] = *(*V)(p)
		}
	}
	return out, bitmap.AllSet(l.rows)
}

// textColumn is a String or Binary column of the strings or byte slices l locates,
// written straight into its offsets and bytes, with no slice of them between.
func textColumn[V string | []byte](name string, dt dtype.DataType, l loc) (*Column, error) {
	offs := make([]int32, l.rows+1)
	var valid *bitmap.Builder
	if l.deref {
		valid = bitmap.NewBuilder(l.rows)
	}
	total := 0
	for i := range l.rows {
		p := l.value(i)
		if valid != nil {
			valid.Append(p != nil)
		}
		if p != nil {
			total += len(*(*V)(p))
			if total > math.MaxInt32 {
				return nil, uerr.New(uerr.KindResource, "from_rows",
					"column %q holds more than 2 GiB of text, which a String column cannot", name)
			}
		}
		offs[i+1] = int32(total)
	}
	chars := make([]byte, 0, total)
	for i := range l.rows {
		if p := l.value(i); p != nil {
			chars = append(chars, *(*V)(p)...)
		}
	}
	v := bitmap.AllSet(l.rows)
	if valid != nil {
		v = valid.Finish()
	}
	c := data.NewStringParts(name, offs, chars, v)
	if dt != dtype.String {
		c = c.WithDType(dt)
	}
	return c, nil
}
