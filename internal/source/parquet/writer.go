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

	// arrow-go declares LZ4 (the deprecated Hadoop framing) and LZO and implements
	// neither, and it panicked on them as the first row group started — after the
	// whole query had run. Asked here, it is a refusal before any work.
	if _, err := compress.GetCodec(o.Compression); err != nil {
		return nil, uerr.Wrap(err, uerr.KindUnsupported, "sink_parquet",
			"cannot write %s compression", o.Compression).
			Hint("for LZ4, use compress.Codecs.Lz4Raw, the framing the Parquet spec now names; " +
				"Snappy, Gzip, Brotli and Zstd are supported too")
	}

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
	// A row with no cells has no column chunk to hold it, and the file came back as
	// no rows at all: (3, 0) written, (0, 0) read. The CSV writer refuses the same.
	if b.NumCols() == 0 {
		return uerr.New(uerr.KindValue, "sink_parquet",
			"a frame with no columns cannot be written to Parquet: its %d rows have no cells", b.Rows()).
			Hint("select at least one column before writing")
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
			// The Checked form: the other one panics where this returns an error.
			rg, err := w.w.AppendBufferedRowGroupChecked()
			if err != nil {
				return uerr.Wrap(err, uerr.KindIO, "sink_parquet", "starting a row group")
			}
			w.rg, w.rows = rg, 0
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
	// rg.Column counts LEAVES, not columns: a struct has one per field.
	leaf := 0
	for _, c := range b.Columns() {
		for _, l := range shred(c) {
			cw, err := w.rg.Column(leaf)
			if err != nil {
				return uerr.Wrap(err, uerr.KindIO, "sink_parquet",
					"opening column %q", c.Name())
			}
			leaf++
			if err := writeColumn(cw, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// leafData is one Parquet leaf column's contents for a batch: the column its values
// come from, the indices of its PRESENT values in order, and its levels.
type leafData struct {
	col  *data.Column
	rows []int
	defs []int16
	reps []int16 // nil for a leaf with no repeated ancestor
}

// shred splits a top-level column into its Parquet leaves, with the definition and
// repetition levels of the schema toNode built for it (Dremel's encoding):
//
//	flat leaf        def 1 present, 0 null
//	struct field     def 2 present, 1 a null field, 0 a null struct
//	list element     def 3 present, 2 a null element, 1 an empty list, 0 a null list;
//	                 rep 0 for a row's first level, 1 for the elements after it
//
// A null or empty list still contributes one level and no value: the levels say
// what is absent, and the values hold only what is present, which is how the
// reader's level machine — listCol's four states — reads them back.
func shred(c *data.Column) []leafData {
	n := c.Len()
	switch c.DType().ID() {
	case dtype.TypeStruct:
		var out []leafData
		for _, f := range c.Fields() {
			l := leafData{col: f, defs: make([]int16, n)}
			for row := range n {
				switch {
				case !c.IsValid(row):
					l.defs[row] = 0
				case !f.IsValid(row):
					l.defs[row] = 1
				default:
					l.defs[row] = 2
					l.rows = append(l.rows, row)
				}
			}
			out = append(out, l)
		}
		return out

	case dtype.TypeList:
		acc := c.Lists()
		child := acc.Child()
		l := leafData{col: child, defs: make([]int16, 0, n), reps: make([]int16, 0, n)}
		for row := range n {
			start, end, _ := acc.Get(row)
			switch {
			case !c.IsValid(row):
				l.defs, l.reps = append(l.defs, 0), append(l.reps, 0)
			case start == end:
				l.defs, l.reps = append(l.defs, 1), append(l.reps, 0)
			default:
				for k := start; k < end; k++ {
					rep := int16(1)
					if k == start {
						rep = 0
					}
					l.reps = append(l.reps, rep)
					if child.IsValid(int(k)) {
						l.defs = append(l.defs, 3)
						l.rows = append(l.rows, int(k))
					} else {
						l.defs = append(l.defs, 2)
					}
				}
			}
		}
		return []leafData{l}
	}

	l := leafData{col: c, defs: make([]int16, n)}
	for row := range n {
		if c.IsValid(row) {
			l.defs[row] = 1
			l.rows = append(l.rows, row)
		}
	}
	return []leafData{l}
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

func writeColumn(cw file.ColumnChunkWriter, l leafData) error {
	c := l.col

	switch t := cw.(type) {
	case *file.BooleanColumnChunkWriter:
		s, err := data.TypedColumn[bool](c)
		if err != nil {
			return err
		}
		return writeVals(t, l, func(row int) bool { v, _ := s.Get(row); return v })

	case *file.Int32ColumnChunkWriter:
		return writeInt32(t, l)

	case *file.Int64ColumnChunkWriter:
		return writeInt64(t, l)

	case *file.Float32ColumnChunkWriter:
		s, err := data.TypedColumn[float32](c)
		if err != nil {
			return err
		}
		return writeVals(t, l, func(row int) float32 { v, _ := s.Get(row); return v })

	case *file.Float64ColumnChunkWriter:
		s, err := data.TypedColumn[float64](c)
		if err != nil {
			return err
		}
		return writeVals(t, l, func(row int) float64 { v, _ := s.Get(row); return v })

	case *file.ByteArrayColumnChunkWriter:
		s, err := data.TypedColumn[string](c)
		if err != nil {
			return err
		}
		return writeVals(t, l, func(row int) parquet.ByteArray {
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
		return writeVals(t, l, func(row int) parquet.FixedLenByteArray {
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
func writeVals[T any](cw batchWriter[T], l leafData, get func(int) T) error {
	vals := make([]T, 0, len(l.rows))
	for _, row := range l.rows {
		vals = append(vals, get(row))
	}
	if _, err := cw.WriteBatch(vals, l.defs, l.reps); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_parquet", "writing column %q", l.col.Name())
	}
	return nil
}

// writeInt32 narrows the small integer types, which Parquet stores as INT32 and
// distinguishes only by the logical annotation toNode attached.
func writeInt32(cw *file.Int32ColumnChunkWriter, l leafData) error {
	c := l.col
	switch c.DType().ID() {
	case dtype.TypeNull:
		// Only levels: shred found no valid row, so there is no value to write.
		return writeVals(cw, l, func(int) int32 { return 0 })
	case dtype.TypeInt8:
		return narrow[int8](cw, l)
	case dtype.TypeInt16:
		return narrow[int16](cw, l)
	case dtype.TypeInt32, dtype.TypeDate:
		return narrow[int32](cw, l)
	case dtype.TypeUint8:
		return narrow[uint8](cw, l)
	case dtype.TypeUint16:
		return narrow[uint16](cw, l)
	case dtype.TypeUint32:
		return narrow[uint32](cw, l)
	case dtype.TypeTime:
		// TIME(MILLIS) is the one temporal type Parquet puts on INT32, and ursus
		// stores every Time as int64 ticks — so this narrows where Date, whose ursus
		// storage is already 32 bits, does not. narrow[int32] cannot do it: Values
		// checks the column's PHYSICAL id, which is Int64 here.
		//
		// The bare `int32(v)` this used to be is the construct step 49 replaced in
		// rescaleTemporal, where it was "a silently wrapped date". Here it also wrote
		// an out-of-spec TIME(MILLIS): Parquet's range is 0..86_399_999 and nothing
		// bounded the tick.
		//
		// Step 57 makes the value always fit, by wrapping at both producers, so this
		// guard is a PROOF rather than a repair — and that is the right order. It is
		// reachable only through a Time column that never went near a cast or the
		// arithmetic kernel, which is exactly the case a future source could produce.
		perDay, ok := dtype.TicksPerDay(c.DType())
		if !ok {
			return uerr.Internalf("sink_parquet: %s has no tick length", c.DType())
		}
		s, err := data.TypedColumn[int64](c)
		if err != nil {
			return err
		}
		for row := range c.Len() {
			v, valid := s.Get(row)
			if valid && (v < 0 || v >= perDay) {
				return uerr.New(uerr.KindValue, "sink_parquet",
					"time %s at row %d is outside [0, 24h) and cannot be written as "+
						"TIME(MILLIS)", dtype.FormatTemporal(c.DType(), v), row).
					Hint("a Time is a time of day; this column holds tick %d", v)
			}
		}
		return writeVals(cw, l, func(row int) int32 {
			v, _ := s.Get(row)
			return int32(v)
		})
	default:
		return uerr.Internalf("sink_parquet: %s mapped to INT32", c.DType())
	}
}

func writeInt64(cw *file.Int64ColumnChunkWriter, l leafData) error {
	c := l.col
	switch c.DType().ID() {
	case dtype.TypeInt64, dtype.TypeDatetime, dtype.TypeDuration:
		// The three temporal types are int64 tick counts at 64 bits, and their
		// Physical() is Int64, so narrow[int64] reads them unchanged — the logical
		// type in the schema is what tells a reader what the ticks mean. Duration
		// never reaches here today because toNode refuses it; it is listed so that
		// enabling it is a one-line change in one place rather than two.
		return narrow[int64](cw, l)
	case dtype.TypeTime:
		return narrow[int64](cw, l)
	case dtype.TypeUint64:
		s, err := data.TypedColumn[uint64](c)
		if err != nil {
			return err
		}
		// A uint64 above 2^63 wraps to a negative int64. That is exactly how Parquet
		// stores UINT_64, and the UINT_64 annotation is what tells a reader to
		// reinterpret it.
		return writeVals(cw, l, func(row int) int64 { v, _ := s.Get(row); return int64(v) })
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
}, W int32 | int64](cw batchWriter[W], l leafData) error {
	s, err := data.TypedColumn[T](l.col)
	if err != nil {
		return err
	}
	return writeVals(cw, l, func(row int) W { v, _ := s.Get(row); return W(v) })
}

// putI128BE writes a 16-byte big-endian two's-complement encoding.
func putI128BE(b []byte, v i128.Int128) {
	hi, lo := uint64(v.Hi), v.Lo
	for i := range 8 {
		b[i] = byte(hi >> (56 - 8*i))
		b[8+i] = byte(lo >> (56 - 8*i))
	}
}
