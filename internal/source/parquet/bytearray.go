package parquet

import (
	"encoding/binary"

	"github.com/apache/arrow-go/v18/parquet"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// byteArrayCol reads BYTE_ARRAY as String or Binary.
type byteArrayCol struct {
	cr     byteArrayReader
	pages  pageStrings // set instead of cr when the chunk is read from its pages
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
	valid lazyValid

	parentDef int16
	parent    *bitmap.Builder
}

// byteArrayReader is what byteArrayCol reads from: a BYTE_ARRAY chunk, or a
// FIXED_LEN_BYTE_ARRAY one through flbaAsBytes.
type byteArrayReader interface {
	ReadBatch(n int64, vals []parquet.ByteArray, defs, reps []int16) (int64, int, error)
	Err() error
	Close() error
}

func (c *byteArrayCol) trackParent(level int16) {
	c.parentDef, c.parent = level, bitmap.NewBuilder(0)
}

func (c *byteArrayCol) parentValidity() bitmap.View {
	v := c.parent.Finish()
	c.parent = bitmap.NewBuilder(0)
	return v
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

// growExact makes room for n more elements and no more: the column keeps the
// buffer, so append's doubling, across the page reads of one batch, was memory the
// frame held for nothing.
func growExact[E any](s []E, n int) []E {
	if cap(s)-len(s) >= n {
		return s
	}
	out := make([]E, len(s), len(s)+n)
	copy(out, s)
	return out
}

// inPageReader reads values that alias the current page instead of copies of them.
// arrow-go's ReadBatch copies every value of a byte-array chunk into a fresh buffer,
// and byteArrayCol copies them again into its own: one of the two is enough, and
// the allocation it makes for every batch is work for the collector.
type inPageReader interface {
	ReadBatchInPage(n int64, vals []parquet.ByteArray, defs, reps []int16) (int64, int, error)
}

func (c *byteArrayCol) read(n int) (int, error) {
	if c.pages.pageChunk != nil {
		return c.pages.appendStrings(n, c)
	}
	if cap(c.vals) < n {
		c.vals = make([]parquet.ByteArray, n)
		if c.maxDef > 0 {
			c.defs = make([]int16, n)
		}
	}
	ip, inPage := c.cr.(inPageReader)
	if !inPage {
		var defs []int16
		if c.maxDef > 0 {
			defs = c.defs[:n]
		}
		total, valuesRead, err := c.cr.ReadBatch(int64(n), c.vals[:n], defs, nil)
		if err := batchErr(c.cr, total, err); err != nil {
			return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a byte-array chunk")
		}
		return c.take(int(total), valuesRead, defs), nil
	}
	// A page at a time. The values alias the page, which the next call may replace,
	// so each call's values are copied into chars before it.
	rows := 0
	for rows < n {
		var defs []int16
		if c.maxDef > 0 {
			defs = c.defs[rows:n]
		}
		total, valuesRead, err := ip.ReadBatchInPage(int64(n-rows), c.vals[:n-rows], defs, nil)
		if err := batchErr(c.cr, total, err); err != nil {
			return 0, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "reading a byte-array chunk")
		}
		if total == 0 {
			break
		}
		rows += c.take(int(total), valuesRead, defs)
	}
	return rows, nil
}

// take appends one read's values, the first valuesRead of c.vals, for rows rows
// whose definition levels are defs (nil for a REQUIRED column), and returns rows.
func (c *byteArrayCol) take(rows, valuesRead int, defs []int16) int {
	if c.maxDef == 0 {
		rows = valuesRead
	}
	if rows == 0 {
		return 0
	}
	vals := c.vals[:valuesRead]

	// The mandatory leading zero, seeded once per column rather than branched on
	// per value. finish then hands offs straight over with no fixup.
	if c.offs == nil {
		c.offs = make([]int32, 1, rows+1)
	}
	// Both buffers grown once for the whole read, not by append's doubling as each
	// value arrives: the characters are counted first.
	chars := 0
	for _, v := range vals {
		chars += len(v)
	}
	c.chars = growExact(c.chars, chars)
	c.offs = growExact(c.offs, rows)

	if c.maxDef == 0 || valuesRead == rows {
		for _, v := range vals {
			c.appendVal(v)
		}
		c.valid.appendValid(rows)
	} else {
		vi := 0
		for i := range rows {
			if defs[i] == c.maxDef {
				c.appendVal(vals[vi])
				vi++
			} else {
				c.appendNull()
			}
		}
		c.valid.appendDefs(defs[:rows], c.maxDef)
	}
	if c.parent != nil && c.maxDef > 0 {
		for _, d := range defs[:rows] {
			c.parent.Append(d >= c.parentDef)
		}
	}
	return rows
}

func (c *byteArrayCol) finish(name string) *data.Column {
	// The column takes both slices, which are sized for this batch alone, and the
	// next batch makes its own. NewStringParts copied them, writing every string's
	// characters a second time (step 163).
	col := data.NewStringOwned(name, c.offs, c.chars, c.valid.finish())
	c.offs, c.chars = nil, nil
	return col.WithDType(c.dt)
}

func (c *byteArrayCol) close() error {
	if c.pages.pageChunk != nil {
		return c.pages.close()
	}
	return c.cr.Close()
}

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
