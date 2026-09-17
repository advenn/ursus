package arrowin

import (
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

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
func validate(arr arrow.Array) error {
	// A released arrow-go array keeps its Go value and loses its data; DataType would
	// dereference nil.
	if d, ok := arr.Data().(*array.Data); !ok || d == nil {
		return uerr.New(uerr.KindValue, op, "an Arrow array has no data").
			Hint("it was probably released: Retain an array you keep past its owner's Release")
	}
	if err := array.Validate(arr); err != nil {
		return uerr.Wrap(err, uerr.KindValue, op, "the Arrow data is malformed")
	}
	return nil
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
	return b.finish(name)
}
