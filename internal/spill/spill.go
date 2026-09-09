// Package spill writes batches to a file and reads them back exactly.
//
// # Why ursus has its own format
//
// Parquet is the only structured writer in the engine and it REFUSES Int128 —
// which is the accumulator and output type of every integer Sum, and therefore
// the engine's most common intermediate. It also refuses Time, Datetime, Duration
// and Enum on write. Arrow IPC is not implemented at all. So the one format
// available cannot serialise the values that most need spilling.
//
// The favourable fact is that a data.Column is only four payload slots and there
// are no List or Struct columns to handle, so a length-prefixed dump of
// {schema, rows, validity, payload} is small, total, and exactly round-tripping.
//
// # It is internal, and that is the point
//
// A spill file is written and read by one process, within one query. It has no
// compatibility obligation, no cross-language readers and no endianness question
// — which is precisely what makes writing one cheaper than extending Parquet.
// Nothing here should be exposed as an interchange format; if that is ever wanted
// the answer is Arrow IPC, not this.
package spill

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"

	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowx"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// magic identifies a spill file and pins the layout. A mismatch is a bug in
// ursus, never a user error, so it reports as one.
const magic = "URSSPIL1"

// Column payload kinds, matching data.Column's four representations.
const (
	kindNull uint8 = iota // no payload buffer at all
	kindFixed
	kindBool
	kindString
)

// Validity and Bool payload flags.
const (
	bitsAllSet uint8 = iota
	bitsBuffer
)

// Record markers. A batch is preceded by recBatch; recEnd closes the file, so a
// truncated file is distinguishable from a complete one.
const (
	recEnd uint8 = iota
	recBatch
)

// --- writing -----------------------------------------------------------------

// Writer appends batches to a spill file.
type Writer struct {
	f    *os.File
	w    *bufio.Writer
	sch  *dtype.Schema
	rows int64
	scr  []byte
}

// DefaultWriteBuffer is the buffer Create gives one sequential run.
const DefaultWriteBuffer = 1 << 16

// Create opens path for writing and records schema in its header.
func Create(path string, sch *dtype.Schema) (*Writer, error) {
	return CreateSized(path, sch, DefaultWriteBuffer)
}

// CreateSized is Create with an explicit write buffer.
//
// A partitioned spill holds N writers open at once, so the 64 KiB that is right
// for one sequential run becomes N × 64 KiB of allocation the budget would have to
// cover — 1 MiB at sixteen partitions, which is larger than most of the limits the
// memory tests use. Write flushes at the end of every batch anyway, so a partition
// writer never holds more than one batch-slice and a smaller buffer costs nothing.
func CreateSized(path string, sch *dtype.Schema, bufBytes int) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindResource, "spill", "creating spill file %s", path).
			Hint("set the spill directory with WithSpillDir")
	}
	w := &Writer{f: f, w: bufio.NewWriterSize(f, bufBytes), sch: sch}
	w.w.WriteString(magic)
	if err := w.putSchema(sch); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return w, nil
}

// Rows returns how many rows have been written.
func (w *Writer) Rows() int64 { return w.rows }

// Write appends one batch.
func (w *Writer) Write(b *data.Batch) error {
	if !b.Schema().Equal(w.sch) {
		return uerr.Internalf("spill: batch schema %s does not match the file's %s",
			b.Schema(), w.sch)
	}
	w.putByte(recBatch)
	w.putUint(uint64(b.Rows()))
	w.putUint(uint64(b.NumCols()))
	for _, c := range b.Columns() {
		if err := w.putColumn(c); err != nil {
			return err
		}
	}
	w.rows += int64(b.Rows())
	return w.w.Flush()
}

// Close writes the terminator and closes the file.
func (w *Writer) Close() error {
	w.putByte(recEnd)
	if err := w.w.Flush(); err != nil {
		w.f.Close()
		return uerr.Wrap(err, uerr.KindIO, "spill", "flushing %s", w.f.Name())
	}
	if err := w.f.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "spill", "closing %s", w.f.Name())
	}
	return nil
}

func (w *Writer) putByte(b uint8)    { w.w.WriteByte(b) }
func (w *Writer) putBytes(b []byte)  { w.putUint(uint64(len(b))); w.w.Write(b) }
func (w *Writer) putString(s string) { w.putUint(uint64(len(s))); w.w.WriteString(s) }
func (w *Writer) putUint(v uint64)   { w.scr = binary.AppendUvarint(w.scr[:0], v); w.w.Write(w.scr) }
func (w *Writer) putBool(b bool) {
	if b {
		w.putByte(1)
		return
	}
	w.putByte(0)
}

func (w *Writer) putView(v bitmap.View, n int) {
	buf := v.Buffer()
	if buf == nil || n == 0 {
		w.putByte(bitsAllSet)
		return
	}
	w.putByte(bitsBuffer)
	// Only the bytes n bits occupy. View.Buffer() returns the WHOLE backing array
	// when the bit offset is zero, and Column.Slice(0, k) produces exactly that — so
	// a 100-row slice of an 8192-row nullable column used to write 1024 validity
	// bytes instead of 13. Read-back was still correct, because getView reads n
	// bits, which is why this was invisible: a size bug, not a wrong answer.
	raw := buf.Bytes()
	if need := (n + 7) / 8; need < len(raw) {
		raw = raw[:need]
	}
	w.putBytes(raw)
}

func (w *Writer) putSchema(s *dtype.Schema) error {
	w.putUint(uint64(s.Len()))
	for _, f := range s.All() {
		w.putString(f.Name)
		w.putBool(f.Nullable)
		if err := w.putDType(f.Type); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) putDType(d dtype.DataType) error {
	switch d.ID() {
	case dtype.TypeList, dtype.TypeArray, dtype.TypeStruct, dtype.TypeCategorical:
		return uerr.New(uerr.KindUnsupported, "spill",
			"cannot spill a %s column", d).
			// The reason, corrected in step 46: List has had a data.Column
			// representation since step 28 and a mid-query producer since step 45.
			// What is missing is a putColumn arm — the serialisation format, not
			// the layout.
			Hint("the spill format has no encoding for a nested column yet")
	}
	w.putByte(uint8(d.ID()))
	w.putByte(uint8(d.TimeUnit()))
	w.putByte(d.Precision())
	w.putByte(d.Scale())
	w.putString(d.TimeZone())
	cats := d.Categories()
	w.putUint(uint64(len(cats)))
	for _, c := range cats {
		w.putString(c)
	}
	return nil
}

func (w *Writer) putColumn(c *data.Column) error {
	n := c.Len()
	dt := c.DType()
	switch {
	case dt.ID() == dtype.TypeBool && !c.IsPayloadFree():
		w.putByte(kindBool)
		w.putView(c.Validity(), n)
		w.putView(c.Bools(), n)

	case dt.HasStringStorage() && !c.IsPayloadFree():
		w.putByte(kindString)
		w.putView(c.Validity(), n)
		// Offsets are rebased so the file carries only the characters this column
		// actually addresses. A sliced String column keeps the ORIGINAL character
		// buffer and re-windows the offsets, so writing the buffer as it stands
		// would spill every character the column no longer refers to.
		offs := data.Reinterpret[int32](c.RawOffsets())
		if len(offs) < n+1 {
			return uerr.Internalf("spill: column %q has %d offsets for %d rows",
				c.Name(), len(offs), n)
		}
		lo, hi := offs[0], offs[n]
		w.putUint(uint64((n + 1) * 4))
		var b4 [4]byte
		for i := 0; i <= n; i++ {
			binary.LittleEndian.PutUint32(b4[:], uint32(offs[i]-lo))
			w.w.Write(b4[:])
		}
		w.putBytes(c.RawChars()[lo:hi])

	case c.IsPayloadFree():
		// NewNull carries no payload at all, which is a genuine representation and
		// not a degenerate one — a typed null literal is free precisely because of
		// it. Round-tripping through NewNull preserves that.
		w.putByte(kindNull)

	default:
		width := dt.Physical().BitWidth() / 8
		if width == 0 {
			return uerr.New(uerr.KindUnsupported, "spill",
				"cannot spill a %s column: no fixed-width payload", dt)
		}
		w.putByte(kindFixed)
		w.putView(c.Validity(), n)
		raw := c.RawFixed()
		if len(raw) < n*width {
			return uerr.Internalf("spill: column %q has %d payload bytes for %d %s rows",
				c.Name(), len(raw), n, dt)
		}
		w.putBytes(raw[:n*width])
	}
	return nil
}

// --- reading -----------------------------------------------------------------

// Reader replays a spill file.
type Reader struct {
	f   *os.File
	r   *bufio.Reader
	sch *dtype.Schema
}

// Open opens a spill file and reads its header.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "spill", "opening spill file %s", path)
	}
	r := &Reader{f: f, r: bufio.NewReaderSize(f, 1<<16)}
	var m [len(magic)]byte
	if _, err := io.ReadFull(r.r, m[:]); err != nil || string(m[:]) != magic {
		f.Close()
		return nil, uerr.Internalf("spill: %s is not a spill file", path)
	}
	sch, err := r.getSchema()
	if err != nil {
		f.Close()
		return nil, err
	}
	r.sch = sch
	return r, nil
}

// Schema returns the schema every batch in the file has.
func (r *Reader) Schema() *dtype.Schema { return r.sch }

// Next returns the next batch, or io.EOF once the file is exhausted.
func (r *Reader) Next() (*data.Batch, error) {
	tag, err := r.r.ReadByte()
	if err != nil {
		return nil, r.wrap(err)
	}
	if tag == recEnd {
		return nil, io.EOF
	}
	if tag != recBatch {
		return nil, uerr.Internalf("spill: unknown record tag %d in %s", tag, r.f.Name())
	}
	rows, err := r.getUint()
	if err != nil {
		return nil, err
	}
	ncols, err := r.getUint()
	if err != nil {
		return nil, err
	}
	if ncols == 0 {
		// A frame can legitimately have rows and no columns, after Select() with
		// nothing. NewBatch cannot express that; NewBatchRows can.
		return data.NewBatchRows(r.sch, nil, int(rows)), nil
	}
	cols := make([]*data.Column, ncols)
	for i := range cols {
		f := r.sch.Field(i)
		c, err := r.getColumn(f, int(rows))
		if err != nil {
			return nil, err
		}
		cols[i] = c
	}
	return data.NewBatch(r.sch, cols)
}

// Close closes the underlying file.
func (r *Reader) Close() error {
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "spill", "closing spill file")
	}
	return nil
}

func (r *Reader) wrap(err error) error {
	if err == io.EOF {
		// A file that ends without its terminator was truncated: the writer failed
		// or the process died. Saying so beats an opaque unexpected-EOF.
		return uerr.New(uerr.KindIO, "spill", "spill file %s ends mid-record", r.f.Name())
	}
	return uerr.Wrap(err, uerr.KindIO, "spill", "reading %s", r.f.Name())
}

func (r *Reader) getUint() (uint64, error) {
	v, err := binary.ReadUvarint(r.r)
	if err != nil {
		return 0, r.wrap(err)
	}
	return v, nil
}

func (r *Reader) getByte() (uint8, error) {
	b, err := r.r.ReadByte()
	if err != nil {
		return 0, r.wrap(err)
	}
	return b, nil
}

func (r *Reader) getString() (string, error) {
	n, err := r.getUint()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r.r, b); err != nil {
		return "", r.wrap(err)
	}
	return string(b), nil
}

// getBuffer reads a length-prefixed payload into a fresh arrowx buffer, so the
// result has the alignment every kernel's fast path expects. A spill file written
// by another implementation could not promise that; one ursus wrote could, but
// re-allocating is cheaper than proving it.
func (r *Reader) getBuffer() (*memory.Buffer, error) {
	n, err := r.getUint()
	if err != nil {
		return nil, err
	}
	buf := arrowx.NewBuffer(int(n))
	if n > 0 {
		if _, err := io.ReadFull(r.r, buf.Bytes()); err != nil {
			return nil, r.wrap(err)
		}
	}
	return buf, nil
}

func (r *Reader) getView(n int) (bitmap.View, error) {
	flag, err := r.getByte()
	if err != nil {
		return bitmap.View{}, err
	}
	if flag == bitsAllSet {
		return bitmap.AllSet(n), nil
	}
	buf, err := r.getBuffer()
	if err != nil {
		return bitmap.View{}, err
	}
	return bitmap.NewView(buf.Bytes(), 0, n), nil
}

func (r *Reader) getSchema() (*dtype.Schema, error) {
	n, err := r.getUint()
	if err != nil {
		return nil, err
	}
	fields := make([]dtype.Field, n)
	for i := range fields {
		name, err := r.getString()
		if err != nil {
			return nil, err
		}
		nullable, err := r.getByte()
		if err != nil {
			return nil, err
		}
		dt, err := r.getDType()
		if err != nil {
			return nil, err
		}
		fields[i] = dtype.Field{Name: name, Type: dt, Nullable: nullable != 0}
	}
	return dtype.NewSchema(fields...)
}

func (r *Reader) getDType() (dtype.DataType, error) {
	var raw [4]uint8
	for i := range raw {
		b, err := r.getByte()
		if err != nil {
			return dtype.Null, err
		}
		raw[i] = b
	}
	tz, err := r.getString()
	if err != nil {
		return dtype.Null, err
	}
	ncats, err := r.getUint()
	if err != nil {
		return dtype.Null, err
	}
	cats := make([]string, ncats)
	for i := range cats {
		if cats[i], err = r.getString(); err != nil {
			return dtype.Null, err
		}
	}

	id, unit, prec, scale := dtype.TypeID(raw[0]), dtype.TimeUnit(raw[1]), raw[2], raw[3]
	switch id {
	case dtype.TypeTime:
		return dtype.Time(unit), nil
	case dtype.TypeDuration:
		return dtype.Duration(unit), nil
	case dtype.TypeDatetime:
		return dtype.Datetime(unit, tz), nil
	case dtype.TypeDecimal:
		return dtype.Decimal(prec, scale), nil
	case dtype.TypeEnum:
		return dtype.Enum(cats...), nil
	}
	d, ok := simpleTypes[id]
	if !ok {
		return dtype.Null, uerr.Internalf("spill: cannot decode type id %d", id)
	}
	return d, nil
}

// simpleTypes is every type whose identity is its TypeID alone.
//
// Written as a table rather than a switch so that a type added to dtype and
// forgotten here fails as "cannot decode type id N" at read time rather than
// silently decoding as Null.
var simpleTypes = map[dtype.TypeID]dtype.DataType{
	dtype.TypeNull:    dtype.Null,
	dtype.TypeBool:    dtype.Bool,
	dtype.TypeInt8:    dtype.Int8,
	dtype.TypeInt16:   dtype.Int16,
	dtype.TypeInt32:   dtype.Int32,
	dtype.TypeInt64:   dtype.Int64,
	dtype.TypeUint8:   dtype.Uint8,
	dtype.TypeUint16:  dtype.Uint16,
	dtype.TypeUint32:  dtype.Uint32,
	dtype.TypeUint64:  dtype.Uint64,
	dtype.TypeFloat32: dtype.Float32,
	dtype.TypeFloat64: dtype.Float64,
	dtype.TypeInt128:  dtype.Int128,
	dtype.TypeString:  dtype.String,
	dtype.TypeBinary:  dtype.Binary,
	dtype.TypeDate:    dtype.Date,
}

func (r *Reader) getColumn(f dtype.Field, n int) (*data.Column, error) {
	kind, err := r.getByte()
	if err != nil {
		return nil, err
	}
	if kind == kindNull {
		return data.NewNull(f.Name, f.Type, n), nil
	}
	valid, err := r.getView(n)
	if err != nil {
		return nil, err
	}
	switch kind {
	case kindBool:
		bits, err := r.getView(n)
		if err != nil {
			return nil, err
		}
		return data.NewBool(f.Name, bits, valid), nil

	case kindString:
		offs, err := r.getBuffer()
		if err != nil {
			return nil, err
		}
		chars, err := r.getBuffer()
		if err != nil {
			return nil, err
		}
		// NewStringBuffers hard-codes String, so Binary is recovered by relabelling.
		// WithDType reinterprets rather than converts, which is exactly right here:
		// the two share a representation and differ only in meaning.
		return data.NewStringBuffers(f.Name, offs, chars, n, valid).WithDType(f.Type), nil

	case kindFixed:
		raw, err := r.getBuffer()
		if err != nil {
			return nil, err
		}
		return data.NewFixedBuffer(f.Name, f.Type, raw, n, valid), nil

	default:
		return nil, uerr.Internalf("spill: unknown column kind %d", kind)
	}
}
