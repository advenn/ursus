package parquet

import (
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
	col, err := kernel.Take(c.v, make([]int32, c.n))
	if err != nil {
		panic(err) // a gather of row 0 of a one-row column cannot fail
	}
	c.n = 0
	return col.Rename(name)
}

func (c *constReader) close() error { return nil }
