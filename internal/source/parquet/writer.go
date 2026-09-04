package parquet

import (
	"io"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// WriteOptions configures a Parquet writer.
type WriteOptions struct {
	// Compression applied to every column. Snappy is the default because it is
	// what every reader supports and it costs almost nothing to decompress.
	Compression compress.Compression

	// RowGroupRows is the target row-group size. It is the unit of BOTH pruning
	// granularity and writer memory: a row group is buffered before it is flushed,
	// so a bigger number prunes better and costs more memory.
	RowGroupRows int

	// Stats writes column statistics. On by default — without them a reader cannot
	// prune anything, which throws away the main reason to use Parquet.
	Stats bool
}

// DefaultWriteOptions returns Snappy, 128k-row groups, statistics on.
func DefaultWriteOptions() WriteOptions {
	return WriteOptions{
		Compression:  compress.Codecs.Snappy,
		RowGroupRows: 128 * 1024,
		Stats:        true,
	}
}

func (o WriteOptions) normalise() WriteOptions {
	if o.RowGroupRows <= 0 {
		o.RowGroupRows = 128 * 1024
	}
	return o
}

// Writer writes batches as Parquet.
//
// Memory is one ROW GROUP, not the whole result: arrow-go buffers a row group
// before flushing it, and this closes the group once RowGroupRows is reached. A
// query producing more data than fits in memory therefore writes correctly, which
// is the half of "larger than RAM" that Collect cannot give.
type Writer struct {
	w      *file.Writer
	closer io.Closer
	opts   WriteOptions
	schema *dtype.Schema

	rg   file.BufferedRowGroupWriter
	rows int

	defs []int16 // reused per column
}

// NewWriter builds a writer for the given schema.
func NewWriter(w io.Writer, s *dtype.Schema, o WriteOptions) (*Writer, error) {
	o = o.normalise()

	nodes := make(schema.FieldList, s.Len())
	for i, f := range s.All() {
		n, err := toNode(f)
		if err != nil {
			return nil, err
		}
		nodes[i] = n
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required, nodes, -1)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindInternal, "sink_parquet", "building the schema")
	}

	props := parquet.NewWriterProperties(
		parquet.WithCompression(o.Compression),
		parquet.WithStats(o.Stats),
	)
	// writeOnly, not w: arrow-go's file.Writer.Close type-asserts its destination to
	// io.Closer and closes it. That would close a caller's os.Stdout, and it makes
	// an outer `defer f.Close()` fail with "file already closed" — which is how this
	// was found. Hiding every method but Write keeps the close where it belongs.
	pw, err := file.NewParquetWriterWithError(writeOnly{w}, root, file.WithWriterProps(props))
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "sink_parquet", "starting the file")
	}
	return &Writer{w: pw, opts: o, schema: s}, nil
}

// writeOnly hides everything but Write, so a destination that happens to be an
// io.Closer is not closed by the library that was only asked to write to it.
type writeOnly struct{ w io.Writer }

func (o writeOnly) Write(p []byte) (int, error) { return o.w.Write(p) }

// WriteBatch appends a batch, flushing a row group when the target is reached.
func (w *Writer) WriteBatch(b *data.Batch) error {
	if !w.schema.Equal(b.Schema()) {
		return uerr.Internalf("sink_parquet: batch schema %s does not match %s",
			b.Schema(), w.schema)
	}
	if b.Rows() == 0 {
		return nil
	}

	// A batch is SPLIT at row-group boundaries rather than appended whole.
	//
	// Appending whole means RowGroupRows is only honoured where a batch happens to
	// end: one 500-row batch with a 50-row target produced a single 500-row group,
	// and the option silently did nothing. Row-group size is the granularity of
	// pruning, so a writer that ignores it hands the reader a file it cannot skip
	// anything in — which is most of the reason to use Parquet at all.
	for off := 0; off < b.Rows(); {
		if w.rg == nil {
			w.rg = w.w.AppendBufferedRowGroup()
			w.rows = 0
		}
		n := min(b.Rows()-off, w.opts.RowGroupRows-w.rows)
		if err := w.appendRows(b.Slice(off, n)); err != nil {
			return err
		}
		off += n
		w.rows += n
		if w.rows >= w.opts.RowGroupRows {
			if err := w.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *Writer) appendRows(b *data.Batch) error {
	for i, c := range b.Columns() {
		cw, err := w.rg.Column(i)
		if err != nil {
			return uerr.Wrap(err, uerr.KindIO, "sink_parquet",
				"opening column %q", c.Name())
		}
		if err := w.writeColumn(cw, c); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) flush() error {
	if w.rg == nil {
		return nil
	}
	if err := w.rg.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_parquet", "closing a row group")
	}
	w.rg = nil
	w.rows = 0
	return nil
}

// Close flushes the final row group and writes the footer. Without the footer the
// file is not a Parquet file at all, so this is not optional.
func (w *Writer) Close() error {
	if err := w.flush(); err != nil {
		w.w.Close()
		return err
	}
	if err := w.w.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_parquet", "writing the footer")
	}
	return nil
}

// levels fills w.defs with 1 for a present value and 0 for a null, and returns it.
//
// Every column is written OPTIONAL (see toNode), so max definition level is 1 and
// the levels are exactly the validity bitmap in int16 form.
func (w *Writer) levels(c *data.Column) []int16 {
	n := c.Len()
	if cap(w.defs) < n {
		w.defs = make([]int16, n)
	}
	w.defs = w.defs[:n]
	for i := range n {
		if c.IsValid(i) {
			w.defs[i] = 1
		} else {
			w.defs[i] = 0
		}
	}
	return w.defs
}

func (w *Writer) writeColumn(cw file.ColumnChunkWriter, c *data.Column) error {
	defs := w.levels(c)

	switch t := cw.(type) {
	case *file.BooleanColumnChunkWriter:
		s, err := data.TypedColumn[bool](c)
		if err != nil {
			return err
		}
		return writeVals(t, c, defs, func(row int) bool { v, _ := s.Get(row); return v })

	case *file.Int32ColumnChunkWriter:
		return writeInt32(t, c, defs)

	case *file.Int64ColumnChunkWriter:
		return writeInt64(t, c, defs)

	case *file.Float32ColumnChunkWriter:
		s, err := data.TypedColumn[float32](c)
		if err != nil {
			return err
		}
		return writeVals(t, c, defs, func(row int) float32 { v, _ := s.Get(row); return v })

	case *file.Float64ColumnChunkWriter:
		s, err := data.TypedColumn[float64](c)
		if err != nil {
			return err
		}
		return writeVals(t, c, defs, func(row int) float64 { v, _ := s.Get(row); return v })

	case *file.ByteArrayColumnChunkWriter:
		s, err := data.TypedColumn[string](c)
		if err != nil {
			return err
		}
		return writeVals(t, c, defs, func(row int) parquet.ByteArray {
			v, _ := s.Get(row)
			return parquet.ByteArray(v)
		})

	case *file.FixedLenByteArrayColumnChunkWriter:
		s, err := data.TypedColumn[i128.Int128](c)
		if err != nil {
			return err
		}
		// Two's-complement big-endian, which is what Parquet's DECIMAL wants. Note
		// this is NOT i128.AppendBigEndian, which flips the sign bit so that byte
		// order matches value order — right for a sort key, wrong for a file format.
		return writeVals(t, c, defs, func(row int) parquet.FixedLenByteArray {
			v, _ := s.Get(row)
			var buf [16]byte
			putI128BE(buf[:], v)
			return buf[:]
		})

	default:
		return uerr.New(uerr.KindUnsupported, "sink_parquet",
			"no writer for column %q of type %s", c.Name(), c.DType())
	}
}

// batchWriter is the WriteBatch shape shared by every typed column chunk writer.
type batchWriter[T any] interface {
	WriteBatch(values []T, defLevels, repLevels []int16) (int64, error)
}

// writeVals writes only the NON-NULL values, which is what a Parquet column chunk
// contains. The definition levels carry the nulls; the values array has no gaps.
//
// Writing a placeholder for a null would shift every value after it on read, since
// the reader maps the i'th value to the i'th SET definition level. It is the same
// packing trap as on the read side, in the other direction.
func writeVals[T any](cw batchWriter[T], c *data.Column, defs []int16, get func(int) T) error {
	vals := make([]T, 0, c.Len())
	for row := range c.Len() {
		if c.IsValid(row) {
			vals = append(vals, get(row))
		}
	}
	if _, err := cw.WriteBatch(vals, defs, nil); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_parquet", "writing column %q", c.Name())
	}
	return nil
}

// writeInt32 narrows the small integer types, which Parquet stores as INT32 and
// distinguishes only by the logical annotation toNode attached.
func writeInt32(cw *file.Int32ColumnChunkWriter, c *data.Column, defs []int16) error {
	switch c.DType().ID() {
	case dtype.TypeInt8:
		return narrow[int8](cw, c, defs)
	case dtype.TypeInt16:
		return narrow[int16](cw, c, defs)
	case dtype.TypeInt32, dtype.TypeDate:
		return narrow[int32](cw, c, defs)
	case dtype.TypeUint8:
		return narrow[uint8](cw, c, defs)
	case dtype.TypeUint16:
		return narrow[uint16](cw, c, defs)
	case dtype.TypeUint32:
		return narrow[uint32](cw, c, defs)
	case dtype.TypeTime:
		// TIME(MILLIS) is the one temporal type Parquet puts on INT32, and ursus
		// stores every Time as int64 ticks — so this narrows where Date, whose ursus
		// storage is already 32 bits, does not. narrow[int32] cannot do it: Values
		// checks the column's PHYSICAL id, which is Int64 here.
		s, err := data.TypedColumn[int64](c)
		if err != nil {
			return err
		}
		return writeVals(cw, c, defs, func(row int) int32 {
			v, _ := s.Get(row)
			return int32(v)
		})
	default:
		return uerr.Internalf("sink_parquet: %s mapped to INT32", c.DType())
	}
}

func writeInt64(cw *file.Int64ColumnChunkWriter, c *data.Column, defs []int16) error {
	switch c.DType().ID() {
	case dtype.TypeInt64, dtype.TypeDatetime, dtype.TypeDuration:
		// The three temporal types are int64 tick counts at 64 bits, and their
		// Physical() is Int64, so narrow[int64] reads them unchanged — the logical
		// type in the schema is what tells a reader what the ticks mean. Duration
		// never reaches here today because toNode refuses it; it is listed so that
		// enabling it is a one-line change in one place rather than two.
		return narrow[int64](cw, c, defs)
	case dtype.TypeTime:
		return narrow[int64](cw, c, defs)
	case dtype.TypeUint64:
		s, err := data.TypedColumn[uint64](c)
		if err != nil {
			return err
		}
		// A uint64 above 2^63 wraps to a negative int64. That is exactly how Parquet
		// stores UINT_64, and the UINT_64 annotation is what tells a reader to
		// reinterpret it.
		return writeVals(cw, c, defs, func(row int) int64 { v, _ := s.Get(row); return int64(v) })
	default:
		return uerr.Internalf("sink_parquet: %s mapped to INT64", c.DType())
	}
}

type pqInt interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32
}

func narrow[T interface {
	data.Fixed
	pqInt
}, W int32 | int64](cw batchWriter[W], c *data.Column, defs []int16) error {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return err
	}
	return writeVals(cw, c, defs, func(row int) W { v, _ := s.Get(row); return W(v) })
}

// putI128BE writes a 16-byte big-endian two's-complement encoding.
func putI128BE(b []byte, v i128.Int128) {
	hi, lo := uint64(v.Hi), v.Lo
	for i := range 8 {
		b[i] = byte(hi >> (56 - 8*i))
		b[8+i] = byte(lo >> (56 - 8*i))
	}
}
