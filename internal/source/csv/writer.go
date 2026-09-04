package csv

import (
	"bufio"
	"io"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// WriteOptions configures a CSV writer.
type WriteOptions struct {
	Separator byte
	Quote     byte

	// Header writes the column names as the first record.
	Header bool

	// LineTerminator defaults to "\n". Set it to "\r\n" for consumers that need it.
	LineTerminator string

	// NullValue is the text written for a null. It defaults to the empty string,
	// which reads back as null for every type EXCEPT String — where "" is a real
	// value and the reader cannot tell the two apart.
	//
	// That asymmetry is a property of CSV, not a bug here: the format has no way to
	// distinguish an absent value from an empty one. A round trip that must
	// preserve null strings needs a sentinel on both sides — WithNullValue("\\N")
	// when writing and WithNullValues("\\N") when reading.
	NullValue string
}

// DefaultWriteOptions returns RFC 4180 defaults with a header.
func DefaultWriteOptions() WriteOptions {
	return WriteOptions{Separator: ',', Quote: '"', Header: true, LineTerminator: "\n"}
}

func (o WriteOptions) normalise() WriteOptions {
	if o.Separator == 0 {
		o.Separator = ','
	}
	if o.Quote == 0 {
		o.Quote = '"'
	}
	if o.LineTerminator == "" {
		o.LineTerminator = "\n"
	}
	return o
}

// Writer writes batches as CSV.
//
// It is a streaming sink: nothing accumulates, so a query whose result does not
// fit in memory writes correctly as long as each batch does.
type Writer struct {
	w    *bufio.Writer
	o    WriteOptions
	line []byte // reused per record

	schema *dtype.Schema
	fmts   []func([]byte, int) []byte // one formatter per column, resolved once
}

// NewWriter builds a writer over w. Close must be called to flush.
func NewWriter(w io.Writer, o WriteOptions) *Writer {
	return &Writer{w: bufio.NewWriter(w), o: o.normalise()}
}

// WriteBatch appends a batch. The first call fixes the schema and writes the
// header; later batches must match it.
func (w *Writer) WriteBatch(b *data.Batch) error {
	if w.schema == nil {
		if err := w.begin(b.Schema()); err != nil {
			return err
		}
	} else if !w.schema.Equal(b.Schema()) {
		return uerr.Internalf("sink_csv: batch schema %s does not match %s",
			b.Schema(), w.schema)
	}

	// Formatters are resolved per BATCH, not per schema, because each one closes
	// over that batch's columns. Resolving per row would repeat a type dispatch for
	// every value in the file.
	if err := w.bind(b); err != nil {
		return err
	}

	for row := range b.Rows() {
		w.line = w.line[:0]
		for c := range b.NumCols() {
			if c > 0 {
				w.line = append(w.line, w.o.Separator)
			}
			w.line = w.fmts[c](w.line, row)
		}
		// A record that renders to nothing at all is a blank line, and every CSV
		// reader — this one and encoding/csv both — skips blank lines. That silently
		// DELETES the row. It happens for a one-column frame holding "" (or a null
		// under the default empty NullValue), which is not an exotic case.
		//
		// Writing a bare pair of quotes makes it an empty field rather than an empty
		// line. encoding/csv's writer has the same special case for the same reason.
		if len(w.line) == 0 {
			w.line = append(w.line, w.o.Quote, w.o.Quote)
		}
		w.line = append(w.line, w.o.LineTerminator...)
		if _, err := w.w.Write(w.line); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "sink_csv", "writing a record")
		}
	}
	return nil
}

// Close flushes. It does not close the underlying writer — whoever opened it
// closes it, which is the only rule that works when the destination might be
// os.Stdout.
func (w *Writer) Close() error {
	if w.schema == nil {
		return nil // nothing was ever written; there is no header to emit
	}
	if err := w.w.Flush(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_csv", "flushing")
	}
	return nil
}

// WriteEmpty emits just the header for a result with no rows, so a downstream
// reader still sees the columns.
func (w *Writer) WriteEmpty(s *dtype.Schema) error {
	if w.schema != nil {
		return nil
	}
	return w.begin(s)
}

func (w *Writer) begin(s *dtype.Schema) error {
	w.schema = s
	if !w.o.Header {
		return nil
	}
	w.line = w.line[:0]
	for i, f := range s.All() {
		if i > 0 {
			w.line = append(w.line, w.o.Separator)
		}
		w.line = w.appendQuoted(w.line, f.Name)
	}
	w.line = append(w.line, w.o.LineTerminator...)
	if _, err := w.w.Write(w.line); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_csv", "writing the header")
	}
	return nil
}

// appendQuoted writes s, quoting and escaping only when it must.
//
// The rule is the conventional one: quote if the value contains the separator, a
// quote, or a line terminator, or if it begins with a space (which some readers
// would otherwise strip).
func (w *Writer) appendQuoted(dst []byte, s string) []byte {
	need := len(s) > 0 && s[0] == ' '
	for i := 0; i < len(s) && !need; i++ {
		switch s[i] {
		case w.o.Separator, w.o.Quote, '\n', '\r':
			need = true
		}
	}
	if !need {
		return append(dst, s...)
	}
	dst = append(dst, w.o.Quote)
	for i := range len(s) {
		if s[i] == w.o.Quote {
			dst = append(dst, w.o.Quote) // double it
		}
		dst = append(dst, s[i])
	}
	return append(dst, w.o.Quote)
}

// bind resolves one formatter per column of b.
func (w *Writer) bind(b *data.Batch) error {
	w.fmts = w.fmts[:0]
	for _, c := range b.Columns() {
		f, err := w.formatter(c)
		if err != nil {
			return err
		}
		w.fmts = append(w.fmts, f)
	}
	return nil
}

func (w *Writer) formatter(c *data.Column) (func([]byte, int) []byte, error) {
	null := w.o.NullValue

	// wrap adds the null check once, so no individual formatter repeats it.
	wrap := func(f func([]byte, int) []byte) func([]byte, int) []byte {
		return func(dst []byte, row int) []byte {
			if !c.IsValid(row) {
				return append(dst, null...)
			}
			return f(dst, row)
		}
	}

	dt := c.DType()
	switch dt.ID() {
	case dtype.TypeString:
		s, err := data.TypedColumn[string](c)
		if err != nil {
			return nil, err
		}
		return wrap(func(dst []byte, row int) []byte {
			v, _ := s.Get(row)
			return w.appendQuoted(dst, v)
		}), nil

	case dtype.TypeBool:
		s, err := data.TypedColumn[bool](c)
		if err != nil {
			return nil, err
		}
		return wrap(func(dst []byte, row int) []byte {
			v, _ := s.Get(row)
			return strconv.AppendBool(dst, v)
		}), nil

	case dtype.TypeFloat32:
		return appendFmt[float32](c, wrap, func(dst []byte, v float32) []byte {
			return strconv.AppendFloat(dst, float64(v), 'g', -1, 32)
		})
	case dtype.TypeFloat64:
		return appendFmt[float64](c, wrap, func(dst []byte, v float64) []byte {
			return strconv.AppendFloat(dst, v, 'g', -1, 64)
		})

	case dtype.TypeInt8:
		return appendInt[int8](c, wrap)
	case dtype.TypeInt16:
		return appendInt[int16](c, wrap)
	case dtype.TypeInt32:
		return appendInt[int32](c, wrap)
	case dtype.TypeInt64:
		return appendInt[int64](c, wrap)
	case dtype.TypeUint8:
		return appendUint[uint8](c, wrap)
	case dtype.TypeUint16:
		return appendUint[uint16](c, wrap)
	case dtype.TypeUint32:
		return appendUint[uint32](c, wrap)
	case dtype.TypeUint64:
		return appendUint[uint64](c, wrap)

	case dtype.TypeInt128:
		return appendFmt[i128.Int128](c, wrap, func(dst []byte, v i128.Int128) []byte {
			return append(dst, v.String()...)
		})

	case dtype.TypeDate, dtype.TypeTime, dtype.TypeDatetime, dtype.TypeDuration:
		// The same formatter the frame renderer uses, so what is printed and what is
		// written agree — and so this writer's output is readable by its own reader,
		// which is the property that made temporal a refusal here until now.
		if dt.Physical().ID() == dtype.TypeInt32 {
			return appendFmt[int32](c, wrap, func(dst []byte, v int32) []byte {
				return w.appendQuoted(dst, dtype.FormatTemporal(dt, int64(v)))
			})
		}
		return appendFmt[int64](c, wrap, func(dst []byte, v int64) []byte {
			return w.appendQuoted(dst, dtype.FormatTemporal(dt, v))
		})

	case dtype.TypeDecimal:
		scale := dt.Scale()
		return appendFmt[i128.Int128](c, wrap, func(dst []byte, v i128.Int128) []byte {
			return append(dst, dtype.FormatDecimal(v.String(), scale)...)
		})

	default:
		// Temporal, Enum and nested types are refused rather than written as their
		// storage integers. Writing a Datetime as 1735689600000000 would produce a
		// file this package cannot read back, and a writer whose output its own
		// reader rejects is worse than one that says so up front.
		return nil, uerr.New(uerr.KindUnsupported, "sink_csv",
			"cannot write column %q of type %s to CSV", c.Name(), dt).
			Hint("CSV supports Bool, the integer and float types, Decimal, the " +
				"temporal types and String").
			Hint("cast or format the column before writing")
	}
}

func appendFmt[T data.Fixed](c *data.Column,
	wrap func(func([]byte, int) []byte) func([]byte, int) []byte,
	f func([]byte, T) []byte,
) (func([]byte, int) []byte, error) {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return nil, err
	}
	return wrap(func(dst []byte, row int) []byte {
		v, _ := s.Get(row)
		return f(dst, v)
	}), nil
}

func appendInt[T interface {
	data.Fixed
	~int8 | ~int16 | ~int32 | ~int64
}](c *data.Column, wrap func(func([]byte, int) []byte) func([]byte, int) []byte,
) (func([]byte, int) []byte, error) {
	return appendFmt[T](c, wrap, func(dst []byte, v T) []byte {
		return strconv.AppendInt(dst, int64(v), 10)
	})
}

func appendUint[T interface {
	data.Fixed
	~uint8 | ~uint16 | ~uint32 | ~uint64
}](c *data.Column, wrap func(func([]byte, int) []byte) func([]byte, int) []byte,
) (func([]byte, int) []byte, error) {
	return appendFmt[T](c, wrap, func(dst []byte, v T) []byte {
		return strconv.AppendUint(dst, uint64(v), 10)
	})
}
