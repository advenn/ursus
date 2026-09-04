package parquet

import (
	"encoding/binary"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/uerr"
)

// colReader reads one column chunk in row-sized slices.
//
// # Definition levels, and the trap inside them
//
// ReadBatch returns TWO counts: `total`, the number of rows (levels) it consumed,
// and `valuesRead`, the number of values it actually wrote. They differ whenever
// the column has nulls, because the values come back PACKED — with no gaps where
// the nulls are.
//
//	rows      0     1     2     3
//	defLvls   1     0     1     1
//	values    a     b     c            <- valuesRead = 3, total = 4
//	                                      b belongs at row 1? NO: at row 2.
//
// Writing values[i] to row i therefore shifts every value after the first null,
// silently, by an amount that depends on the data. It is the single most
// error-prone part of a Parquet reader, and it is invisible on any fixture whose
// columns happen to have no nulls.
//
// expand below walks the levels backwards from the packed values, which is the
// only place this mapping is implemented.
type colReader interface {
	// read consumes up to n rows and appends them. It returns the rows consumed,
	// which is 0 at the end of the chunk.
	read(n int) (int, error)
	finish(name string) *data.Column
	close() error
}

// batchReader is the ReadBatch shape shared by every typed column chunk reader.
type batchReader[P any] interface {
	ReadBatch(batchSize int64, values []P, defLvls, repLvls []int16) (int64, int, error)
	Close() error
}

// fixedCol reads a physical Parquet type P and stores it as an ursus type T.
type fixedCol[P any, T data.Fixed] struct {
	cr     batchReader[P]
	maxDef int16
	conv   func(P) T

	vals []P     // scratch: packed values from ReadBatch
	defs []int16 // scratch: definition levels

	out   []T
	valid *bitmap.Builder
	dt    dtype.DataType
}

func (c *fixedCol[P, T]) read(n int) (int, error) {
	if cap(c.vals) < n {
		c.vals = make([]P, n)
		if c.maxDef > 0 {
			c.defs = make([]int16, n)
		}
	}
	vals := c.vals[:n]
	var defs []int16
	if c.maxDef > 0 {
		defs = c.defs[:n]
	}

	total, valuesRead, err := c.cr.ReadBatch(int64(n), vals, defs, nil)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a column chunk")
	}
	rows := int(total)
	if rows == 0 {
		return 0, nil
	}

	if c.maxDef == 0 {
		// A REQUIRED column has no nulls and no levels; the values are already dense.
		for i := range valuesRead {
			c.out = append(c.out, c.conv(vals[i]))
		}
		c.valid.AppendMany(true, valuesRead)
		return valuesRead, nil
	}

	var zero T
	vi := 0
	for i := 0; i < rows; {
		// Pack validity 64 bits at a time. AppendBits is width-safe, which matters
		// because the tail word is partial for any row count not a multiple of 64.
		var word uint64
		w := min(64, rows-i)
		for b := range w {
			if defs[i+b] == c.maxDef {
				word |= 1 << uint(b)
				c.out = append(c.out, c.conv(vals[vi]))
				vi++
			} else {
				c.out = append(c.out, zero)
			}
		}
		c.valid.AppendBits(word, w)
		i += w
	}
	if vi != valuesRead {
		// The levels and the value count disagree, which means the file's levels are
		// inconsistent with its data. Catching it here names the cause; letting it
		// through would shift every later value.
		return 0, uerr.New(uerr.KindValue, "scan_parquet",
			"definition levels describe %d values but %d were read", vi, valuesRead)
	}
	return rows, nil
}

func (c *fixedCol[P, T]) finish(name string) *data.Column {
	col := data.NewFixed(name, c.dt, c.out, c.valid.Finish())
	// A fresh buffer per batch: NewFixed wraps without copying, so reusing the
	// slice would rewrite a batch the consumer still holds.
	c.out = nil
	c.valid = bitmap.NewBuilder(0)
	return col
}

func (c *fixedCol[P, T]) close() error { return c.cr.Close() }

// boolCol is separate because Bool is stored as a bitmap rather than a values
// buffer — the one ursus type outside data.Fixed.
type boolCol struct {
	cr     *file.BooleanColumnChunkReader
	maxDef int16

	vals []bool
	defs []int16

	out   *bitmap.Builder
	valid *bitmap.Builder
	n     int
}

func (c *boolCol) read(n int) (int, error) {
	if cap(c.vals) < n {
		c.vals = make([]bool, n)
		if c.maxDef > 0 {
			c.defs = make([]int16, n)
		}
	}
	vals := c.vals[:n]
	var defs []int16
	if c.maxDef > 0 {
		defs = c.defs[:n]
	}

	total, valuesRead, err := c.cr.ReadBatch(int64(n), vals, defs, nil)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a boolean chunk")
	}
	rows := int(total)
	if rows == 0 {
		return 0, nil
	}

	if c.maxDef == 0 {
		for i := range valuesRead {
			c.out.Append(vals[i])
		}
		c.valid.AppendMany(true, valuesRead)
		c.n += valuesRead
		return valuesRead, nil
	}

	vi := 0
	for i := range rows {
		if defs[i] == c.maxDef {
			c.out.Append(vals[vi])
			c.valid.Append(true)
			vi++
		} else {
			c.out.Append(false)
			c.valid.Append(false)
		}
	}
	c.n += rows
	return rows, nil
}

func (c *boolCol) finish(name string) *data.Column {
	col := data.NewBool(name, c.out.Finish(), c.valid.Finish())
	c.out = bitmap.NewBuilder(0)
	c.valid = bitmap.NewBuilder(0)
	c.n = 0
	return col
}

func (c *boolCol) close() error { return c.cr.Close() }

// byteArrayCol reads BYTE_ARRAY as String or Binary.
type byteArrayCol struct {
	cr     *file.ByteArrayColumnChunkReader
	maxDef int16
	dt     dtype.DataType

	vals []parquet.ByteArray
	defs []int16

	// Arrow's layout, accumulated directly. This used to be an []string with one
	// Go string appended per value — one heap allocation per row, and then a
	// second full copy of every byte when data.NewString turned the slice into
	// these same two buffers. A scan of a million rows with two string columns
	// spent 2.14M allocations on it, which a profile put at 35% of every object
	// the engine allocated.
	//
	// offs carries the usual n+1 entries, so it starts with a single zero and
	// gains one entry per value, null or not.
	offs  []int32
	chars []byte
	valid *bitmap.Builder
}

// appendVal records one present value. The copy is not optional: the ByteArray
// points into a decode buffer arrow-go reuses across batches.
func (c *byteArrayCol) appendVal(v parquet.ByteArray) {
	c.chars = append(c.chars, v...)
	c.offs = append(c.offs, int32(len(c.chars)))
}

// appendNull records one absent value as an empty slice — the offset does not
// advance, so it costs nothing beyond its offset entry.
func (c *byteArrayCol) appendNull() {
	c.offs = append(c.offs, int32(len(c.chars)))
}

func (c *byteArrayCol) read(n int) (int, error) {
	if cap(c.vals) < n {
		c.vals = make([]parquet.ByteArray, n)
		if c.maxDef > 0 {
			c.defs = make([]int16, n)
		}
	}
	vals := c.vals[:n]
	var defs []int16
	if c.maxDef > 0 {
		defs = c.defs[:n]
	}

	total, valuesRead, err := c.cr.ReadBatch(int64(n), vals, defs, nil)
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a byte-array chunk")
	}
	rows := int(total)
	if rows == 0 {
		return 0, nil
	}

	// The mandatory leading zero, seeded once per column rather than branched on
	// per value. finish then hands offs straight over with no fixup.
	if c.offs == nil {
		c.offs = make([]int32, 1, rows+1)
	}

	if c.maxDef == 0 {
		for i := range valuesRead {
			c.appendVal(vals[i])
		}
		c.valid.AppendMany(true, valuesRead)
		return valuesRead, nil
	}

	vi := 0
	for i := range rows {
		if defs[i] == c.maxDef {
			c.appendVal(vals[vi])
			c.valid.Append(true)
			vi++
		} else {
			c.appendNull()
			c.valid.Append(false)
		}
	}
	return rows, nil
}

func (c *byteArrayCol) finish(name string) *data.Column {
	col := data.NewStringParts(name, c.offs, c.chars, c.valid.Finish())
	c.offs, c.chars = nil, nil
	c.valid = bitmap.NewBuilder(0)
	return col.WithDType(c.dt)
}

func (c *byteArrayCol) close() error { return c.cr.Close() }

// --- decimal conversions --------------------------------------------------------

// decimalFromBytes decodes a big-endian two's-complement integer of up to 16
// bytes, which is how Parquet stores a DECIMAL in a (FIXED_LEN_)BYTE_ARRAY.
//
// Sign extension is the part worth stating: a 4-byte -1 is ff ff ff ff, and
// zero-extending it to 128 bits would produce 4294967295 rather than -1.
func decimalFromBytes(b []byte) (i128.Int128, error) {
	if len(b) == 0 {
		return i128.Zero, nil
	}
	if len(b) > 16 {
		return i128.Zero, uerr.New(uerr.KindValue, "scan_parquet",
			"a DECIMAL value is %d bytes, which does not fit in 128 bits", len(b))
	}

	var buf [16]byte
	if b[0]&0x80 != 0 {
		for i := range buf {
			buf[i] = 0xff // sign-extend a negative value
		}
	}
	copy(buf[16-len(b):], b)

	return i128.Int128{
		Hi: int64(binary.BigEndian.Uint64(buf[0:8])),
		Lo: binary.BigEndian.Uint64(buf[8:16]),
	}, nil
}
