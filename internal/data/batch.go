package data

import (
	"iter"

	"ursus/dtype"
	"ursus/internal/uerr"
)

// Batch is a horizontal slice of a frame: N equal-length columns with a schema.
//
// It is the unit that flows between physical operators. Batch size is a tuning
// knob rather than a semantic one — the same query must produce the same answer
// at any batch size, and the test suite checks that by varying it.
type Batch struct {
	schema *dtype.Schema
	cols   []*Column
	rows   int
}

// NewBatch builds a batch, checking that the columns match the schema in count,
// order, name and type.
//
// The check is not paranoia: a mismatch here means the evaluator produced
// something other than what the plan's schema resolution promised, which is
// exactly the class of bug that would otherwise surface as corrupted output far
// downstream. Failing at the boundary makes it a one-line diagnosis.
func NewBatch(schema *dtype.Schema, cols []*Column) (*Batch, error) {
	if schema.Len() != len(cols) {
		return nil, uerr.Internalf("data: batch has %d columns but schema has %d",
			len(cols), schema.Len())
	}
	rows := -1
	for i, c := range cols {
		f := schema.Field(i)
		if c.Name() != f.Name {
			return nil, uerr.Internalf("data: batch column %d is named %q, schema says %q",
				i, c.Name(), f.Name)
		}
		if c.DType() != f.Type {
			return nil, uerr.Internalf("data: batch column %q is %s, schema says %s",
				c.Name(), c.DType(), f.Type)
		}
		if rows == -1 {
			rows = c.Len()
		} else if c.Len() != rows {
			return nil, uerr.Internalf(
				"data: batch column %q has %d rows, but column %q has %d",
				c.Name(), c.Len(), cols[0].Name(), rows)
		}
	}
	if rows == -1 {
		rows = 0
	}
	return &Batch{schema: schema, cols: cols, rows: rows}, nil
}

// NewBatchRows builds a zero-column batch that still has a row count. A frame can
// legitimately have rows but no columns after `Select()` with nothing.
func NewBatchRows(schema *dtype.Schema, cols []*Column, rows int) *Batch {
	return &Batch{schema: schema, cols: cols, rows: rows}
}

// Schema returns the batch's schema.
func (b *Batch) Schema() *dtype.Schema { return b.schema }

// Rows returns the number of rows.
func (b *Batch) Rows() int { return b.rows }

// NumCols returns the number of columns.
func (b *Batch) NumCols() int { return len(b.cols) }

// Column returns the i'th column.
func (b *Batch) Column(i int) *Column { return b.cols[i] }

// Columns returns all columns. The slice is shared and must not be mutated.
func (b *Batch) Columns() []*Column { return b.cols }

// ByName returns the named column.
func (b *Batch) ByName(name string) (*Column, bool) {
	i := b.schema.IndexOf(name)
	if i < 0 {
		return nil, false
	}
	return b.cols[i], true
}

// All iterates the columns in order.
func (b *Batch) All() iter.Seq2[int, *Column] {
	return func(yield func(int, *Column) bool) {
		for i, c := range b.cols {
			if !yield(i, c) {
				return
			}
		}
	}
}

// Slice returns rows [offset, offset+length) of every column.
func (b *Batch) Slice(offset, length int) *Batch {
	cols := make([]*Column, len(b.cols))
	for i, c := range b.cols {
		cols[i] = c.Slice(offset, length)
	}
	return &Batch{schema: b.schema, cols: cols, rows: length}
}
