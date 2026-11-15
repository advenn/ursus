package arrowin

import (
	"slices"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Checker verifies records against the schema a scan was planned with, before any
// of their values are read. It is not safe for concurrent use.
//
// # Types are checked, not trusted
//
// A builder is chosen from the SCHEMA and then reads an array. If the array's type
// disagreed — a record whose columns were reordered, an Int32 where an Int64 was
// planned — a fixed-width builder would read 4n bytes as 8n, or one column's values
// under another's name. So every record's schema is converted and compared with the
// planned one, every array is compared with its field, and array.Validate checks
// that each child agrees with its parent's declared type and that buffers are long
// enough for the rows they claim.
//
// arrow.Schema.Equal is not the comparison: it compares Nullable and metadata,
// which ursus deliberately ignores, and never looks at the arrays at all. It is used
// only to skip re-converting a schema already known to be good.
type Checker struct {
	want *dtype.Schema
	seen *arrow.Schema
}

// NewChecker returns a Checker for records that must convert to want.
func NewChecker(want *dtype.Schema) *Checker { return &Checker{want: want} }

// Check verifies one record.
func (c *Checker) Check(rec arrow.RecordBatch) error {
	if rec == nil {
		return uerr.New(uerr.KindValue, op, "a record is nil")
	}
	s := rec.Schema()
	if s == nil {
		return uerr.New(uerr.KindValue, op, "a record has no schema")
	}
	if s != c.seen && (c.seen == nil || !s.Equal(c.seen)) {
		got, err := Schema(s)
		if err != nil {
			return err
		}
		if !got.Equal(c.want) {
			return uerr.New(uerr.KindSchema, op,
				"a record's schema %s differs from the scan's %s", got, c.want).
				Hint("every record read by one scan must have the same columns and types")
		}
		c.seen = s
	}

	// A released arrow-go record keeps its schema and drops its columns.
	if int(rec.NumCols()) != c.want.Len() {
		return uerr.New(uerr.KindValue, op,
			"a record has %d columns but its schema declares %d", rec.NumCols(), c.want.Len()).
			Hint("a record that was released has no columns: Retain any record you keep " +
				"past your own Release, or past its reader's next Next")
	}
	rows := rec.NumRows()
	if rows < 0 {
		return uerr.New(uerr.KindValue, op, "a record declares %d rows", rows)
	}
	for i := range c.want.Len() {
		name := s.Field(i).Name
		arr := rec.Column(i)
		if arr == nil {
			return uerr.New(uerr.KindValue, op, "column %q of a record is nil", name)
		}
		if err := validate(arr); err != nil {
			return within(err, "column %q", name)
		}
		// The record's rows, not the array's: arrow-go lets a column be LONGER than
		// its record, and the rows past the record are not part of it.
		if int64(arr.Len()) < rows {
			return uerr.New(uerr.KindValue, op,
				"column %q has %d rows and its record declares %d", name, arr.Len(), rows)
		}
		if !arrow.TypeEqual(s.Field(i).Type, arr.DataType()) {
			return uerr.New(uerr.KindSchema, op,
				"column %q is declared %s and holds %s", name, s.Field(i).Type, arr.DataType())
		}
	}
	return nil
}

// validate refuses an array that was released or is internally inconsistent.
//
// # A Null array of no buffers
//
// The IPC format writes a Null column as a field node and no buffer, and arrow-go's
// IPC reader builds it as a Null array of no buffers (loadNull), where its builders
// and array.Validate give a Null array the one buffer the Null layout declares, which
// must be nil. Validate refused the reader's, "expected at least 1 buffers for null,
// got 0", and with it every IPC file and stream holding a Null column at any depth,
// whoever wrote it (step 522). The two shapes hold the same data, so Validate is handed
// a copy in which each such array holds the nil buffer (nullShaped). Every other
// array, beside one or above it, is validated as it is: the copy keeps its type,
// length, null count, offset, buffers, children and dictionary.
func validate(arr arrow.Array) error {
	// A released arrow-go array keeps its Go value and loses its data; DataType would
	// dereference nil.
	d, ok := arr.Data().(*array.Data)
	if !ok || d == nil {
		return uerr.New(uerr.KindValue, op, "an Arrow array has no data").
			Hint("it was probably released: Retain an array you keep past its owner's Release")
	}
	shaped, err := nullShaped(arr, d)
	if err != nil {
		return err
	}
	if shaped != arr {
		defer shaped.Release()
	}
	if err := array.Validate(shaped); err != nil {
		return uerr.Wrap(err, uerr.KindValue, op, "the Arrow data is malformed")
	}
	return nil
}

// nullShaped is arr, whose data is d, or, where a Null array of no buffer is in it,
// arr itself among them, a copy of arr in which each holds the nil buffer, which its
// caller releases. arrow-go built arr from the same buffers, so building the copy
// does not panic where building arr did not; a panic is still the data's error.
func nullShaped(arr arrow.Array, d *array.Data) (out arrow.Array, err error) {
	s := shapeNulls(d)
	if s == d {
		return arr, nil
	}
	defer s.Release()
	defer func() {
		if v := recover(); v != nil {
			out, err = nil, uerr.New(uerr.KindValue, op, "the Arrow data is malformed: %v", v)
		}
	}()
	return array.MakeFromData(s), nil
}

// shapeNulls is d, or a copy of d in which every Null array of no buffer holds the nil
// buffer, d itself among them, with a reference its caller releases. Only the arrays
// on the path from d to such an array are copied, each as it is but for the child or
// the dictionary copied beneath it; d is returned, and nothing allocated, where there
// is none.
func shapeNulls(d *array.Data) *array.Data {
	var shaped []arrow.ArrayData // d's children, those copied in place
	for i, c := range d.Children() {
		cd, ok := c.(*array.Data)
		if !ok || cd == nil {
			continue
		}
		if s := shapeNulls(cd); s != cd {
			if shaped == nil {
				shaped = slices.Clone(d.Children())
			}
			shaped[i] = s
		}
	}
	dict, _ := d.Dictionary().(*array.Data)
	var shapedDict *array.Data
	if dict != nil {
		if s := shapeNulls(dict); s != dict {
			shapedDict = s
		}
	}
	empty := alwaysNull(d.DataType()) && len(d.Buffers()) == 0
	if shaped == nil && shapedDict == nil && !empty {
		return d
	}
	buffers := slices.Clone(d.Buffers())
	if empty {
		buffers = []*memory.Buffer{nil}
	}
	children := shaped
	if children == nil {
		children = slices.Clone(d.Children())
	}
	out := array.NewData(d.DataType(), d.Len(), buffers, children, d.NullN(), d.Offset())
	for i, c := range shaped {
		if c != d.Children()[i] {
			c.Release() // out holds it
		}
	}
	switch {
	case shapedDict != nil:
		out.SetDictionary(shapedDict)
		shapedDict.Release()
	case dict != nil:
		out.SetDictionary(dict)
	}
	return out
}

// alwaysNull is whether dt has the Null layout, one buffer that is always nil: the
// Null type, or an extension type stored as it.
func alwaysNull(dt arrow.DataType) bool {
	if ext, ok := dt.(arrow.ExtensionType); ok {
		dt = ext.StorageType()
	}
	return dt.ID() == arrow.NULL
}

// Batch copies rows [lo, hi) of a record into an ursus batch whose columns are
// out's, read from the record's columns cols. The record must have passed a Checker
// whose schema out was selected from.
func Batch(rec arrow.RecordBatch, out *dtype.Schema, cols []int, lo, hi int) (*data.Batch, error) {
	if lo < 0 || hi < lo || int64(hi) > rec.NumRows() || len(cols) != out.Len() {
		return nil, uerr.Internalf("arrow: rows [%d, %d) of a %d-row record into %d of %d columns",
			lo, hi, rec.NumRows(), len(cols), out.Len())
	}
	if len(cols) == 0 {
		// Rows and no columns: selecting nothing still has a height.
		return data.NewBatchRows(out, nil, hi-lo), nil
	}
	columns := make([]*data.Column, len(cols))
	for j, ci := range cols {
		f := out.Field(j)
		c, err := column(f.Name, rec.Column(ci), f.Type, lo, hi)
		if err != nil {
			return nil, err
		}
		columns[j] = c
	}
	return data.NewBatch(out, columns)
}

// Column copies rows [lo, hi) of arr into a column of type dt, validating arr first.
func Column(name string, arr arrow.Array, dt dtype.DataType, lo, hi int) (*data.Column, error) {
	if arr == nil {
		return nil, uerr.New(uerr.KindValue, op, "column %q: the Arrow array is nil", name)
	}
	if err := validate(arr); err != nil {
		return nil, within(err, "column %q", name)
	}
	return column(name, arr, dt, lo, hi)
}

func column(name string, arr arrow.Array, dt dtype.DataType, lo, hi int) (*data.Column, error) {
	b, err := newBuilder(dt)
	if err != nil {
		return nil, err
	}
	b.reserve(hi - lo)
	if err := appendArrow(b, arr, lo, hi); err != nil {
		return nil, within(err, "column %q", name)
	}
	c, err := b.finish(name)
	if err != nil {
		return nil, err
	}
	if err := checkTimes(c, lo); err != nil {
		return nil, err
	}
	return c, nil
}

// checkTimes refuses a Time the column holds outside its day, c the copy of an
// array's rows from lo (step 525). A Time is a time of day, [0, 24h) in its unit
// (dtype), as Arrow's columnar format says of its Time32 and Time64, and arrowout
// refuses any other; nothing in arrow-go range-checks one. Read in, 26:00 renders as
// 02:00:00, sorts after 23:00:00 and equals no 02:00:00, so it is refused as the
// corrupt value it is, an element of a List or a field of a Struct as well, by the
// rule the Parquet reader's checkTimes refuses one by (data.FindOutsideDay). A null
// slot holds whatever it holds, and is not looked at.
func checkTimes(c *data.Column, lo int) error {
	o, ok := data.FindOutsideDay(c)
	if !ok {
		return nil
	}
	return uerr.New(uerr.KindValue, op,
		"column %q holds the %s %d in row %d of its record, outside [0, %d)",
		c.Name(), o.At.DType(), o.Tick, lo+o.Row, o.PerDay).
		Hint("the data is corrupt: an Arrow time counts from midnight and is less than a day")
}
