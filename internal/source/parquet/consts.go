package parquet

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// constReader is a column every row of a file shares, a partition value or the
// file's path (step 347): it reads as many rows as it is asked for, and they are its
// one value repeated.
type constReader struct {
	v *data.Column // one row
	n int          // rows read since the last finish
}

func (c *constReader) read(n int) (int, error) {
	c.n += n
	return n, nil
}

func (c *constReader) finish(name string) *data.Column {
	if !c.v.IsValid(0) {
		// A null, a partition's or a column's a file lacks (step 348), which may
		// have no payload to repeat: an operand's nulls instead.
		col, err := kernel.NullColumn(name, c.v.DType(), c.n)
		if err != nil {
			panic(err)
		}
		c.n = 0
		return col
	}
	col, err := kernel.Take(c.v, make([]int32, c.n))
	if err != nil {
		panic(err) // a gather of row 0 of a one-row column cannot fail
	}
	c.n = 0
	return col.Rename(name)
}

func (c *constReader) close() error { return nil }

// castReader is a column a file holds in a narrower type than the schema's: read as the
// file holds it, and widened (step 348).
type castReader struct {
	inner colReader
	to    dtype.DataType
}

func (c *castReader) read(n int) (int, error) { return c.inner.read(n) }

func (c *castReader) finish(name string) *data.Column {
	col, err := kernel.Cast(name, c.to, true, c.inner.finish(name))
	if err != nil {
		panic(err) // a widening cast holds every value
	}
	return col
}

func (c *castReader) close() error { return c.inner.close() }
