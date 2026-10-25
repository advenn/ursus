package csv

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/strftime"
	"github.com/advenn/ursus/internal/uerr"
)

// QuoteStyle is when a field is quoted: Polars' quote_style (step 354).
type QuoteStyle uint8

const (
	// QuoteNecessary quotes a field holding the separator, the quote or a line
	// break, the empty string, and one beginning with a space: the default.
	QuoteNecessary QuoteStyle = iota
	// QuoteAlways quotes every field, the header's and a null's too.
	QuoteAlways
	// QuoteNever quotes no field.
	QuoteNever
	// QuoteNonNumeric quotes every field but a number and a null.
	QuoteNonNumeric
)

// Compression is the codec the text is written in (step 354).
type Compression uint8

const (
	Uncompressed Compression = iota
	Gzip
	Zstd
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
	// which reads back as null for every type. An empty String is written quoted,
	// as "", which reads back as the empty string, so the two stay apart — as
	// Polars writes and reads them. This used to say the format cannot tell them
	// apart; quoting can.
	NullValue string

	// QuoteStyle is when a field is quoted, Polars' quote_style.
	QuoteStyle QuoteStyle

	// DateFormat, DatetimeFormat and TimeFormat are the strftime formats a Date, a
	// Datetime and a Time are written in, Polars' date_format, datetime_format and
	// time_format. Empty, each is Polars' own: 2024-01-02; 2024-01-02T03:04:05.123456
	// to the instant's unit, and its offset where it has a zone, +0900; and
	// 03:04:05.000000000.
	DateFormat, DatetimeFormat, TimeFormat string

	// FloatPrecision is the digits after a float's point, Polars' float_precision;
	// -1 is as many as the float needs.
	FloatPrecision int

	// FloatScientific is 1 for every float in scientific notation, 1.5e0, and -1 for
	// none, Polars' float_scientific; 0 is Polars' text, scientific past 1e16 and
	// below 1e-5.
	FloatScientific int

	// DecimalComma writes a float's and a Decimal's point as a comma.
	DecimalComma bool

	// BOM begins the text with a UTF-8 byte-order mark, Polars' include_bom.
	BOM bool

	// Compression and CompressionLevel are the codec and its level, Polars'
	// compression and compression_level: gzip's 0 to 9, Zstandard's 1 to 22, and -1
	// the codec's default, 6 and 3.
	Compression      Compression
	CompressionLevel int

	// SkipExtensionCheck lets a file's name end as another codec's files do, which
	// CheckExtension refuses, as Polars' check_extension=False.
	SkipExtensionCheck bool
}

// CheckExtension refuses a path whose name does not end as the codec's files do, .gz
// for gzip and .zst for Zstandard, or which ends so without the codec, as Polars'
// check_extension does.
func CheckExtension(path string, o WriteOptions) error {
	if o.SkipExtensionCheck {
		return nil
	}
	want := map[Compression]string{Gzip: ".gz", Zstd: ".zst"}[o.Compression]
	if want != "" && !strings.HasSuffix(path, want) {
		return uerr.New(uerr.KindValue, "sink_csv",
			"the path %s does not end %s, as a %s file's does", path, want, map[Compression]string{Gzip: "gzip", Zstd: "Zstandard"}[o.Compression]).
			Hint("name it so, or pass WithCSVCheckExtension(false)")
	}
	if want == "" && (strings.HasSuffix(path, ".gz") || strings.HasSuffix(path, ".zst")) {
		return uerr.New(uerr.KindValue, "sink_csv",
			"the path %s ends as a compressed file's does, and the text is not compressed", path).
			Hint("compress it with WithCSVCompression, or pass WithCSVCheckExtension(false)")
	}
	return nil
}

// DefaultWriteOptions returns RFC 4180 defaults with a header.
func DefaultWriteOptions() WriteOptions {
	return WriteOptions{Separator: ',', Quote: '"', Header: true, LineTerminator: "\n",
		FloatPrecision: -1, CompressionLevel: -1}
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
	w     *bufio.Writer
	codec io.Closer // a compressor between w and the destination, closed by Close
	o     WriteOptions
	line  []byte // reused per record

	dateFmt, datetimeFmt, timeFmt *strftime.Format
	null, yes, no                 []byte // a null's, true's and false's text, quoted as the style says

	schema *dtype.Schema
	fmts   []func([]byte, int) []byte // one formatter per column, resolved once
}

// NewWriter builds a writer over w, its formats compiled and its codec begun. Close
// must be called to flush.
func NewWriter(w io.Writer, o WriteOptions) (*Writer, error) {
	o = o.normalise()
	cw := &Writer{o: o}
	for _, f := range []struct {
		src  string
		to   **strftime.Format
		kind string
	}{{o.DateFormat, &cw.dateFmt, "date"}, {o.DatetimeFormat, &cw.datetimeFmt, "datetime"}, {o.TimeFormat, &cw.timeFmt, "time"}} {
		if f.src == "" {
			continue
		}
		c, err := strftime.Compile(f.src)
		if err != nil {
			return nil, uerr.New(uerr.KindValue, "sink_csv", "the %s format %q: %v", f.kind, f.src, err)
		}
		*f.to = c
	}
	if o.FloatPrecision < -1 {
		return nil, uerr.New(uerr.KindValue, "sink_csv",
			"a float precision of %d digits; it is 0 or more", o.FloatPrecision)
	}
	switch o.Compression {
	case Gzip:
		level := o.CompressionLevel
		if level == -1 {
			level = 6
		}
		if level < 0 || level > 9 {
			return nil, uerr.New(uerr.KindValue, "sink_csv", "gzip's level is 0 to 9, not %d", o.CompressionLevel)
		}
		gz, err := gzip.NewWriterLevel(w, level)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindValue, "sink_csv", "starting gzip")
		}
		cw.codec, w = gz, gz
	case Zstd:
		level := o.CompressionLevel
		if level == -1 {
			level = 3
		}
		if level < 1 || level > 22 {
			return nil, uerr.New(uerr.KindValue, "sink_csv", "Zstandard's level is 1 to 22, not %d", o.CompressionLevel)
		}
		zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)))
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindValue, "sink_csv", "starting Zstandard")
		}
		cw.codec, w = zw, zw
	}
	cw.w = bufio.NewWriter(w)
	cw.null = []byte(o.NullValue)
	if o.QuoteStyle == QuoteAlways {
		cw.null = cw.quoted(nil, o.NullValue)
	}
	cw.yes, cw.no = cw.text(nil, "true"), cw.text(nil, "false")
	return cw, nil
}

// WriteBatch appends a batch. The first call fixes the schema and writes the
// header; later batches must match it.
func (w *Writer) WriteBatch(b *data.Batch) error {
	if err := noColumns("sink_csv", "CSV", b); err != nil {
		return err
	}
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
		// A record that renders to nothing at all is a blank line: a one-column
		// frame's null under the default empty NullValue. This writer used to write
		// a bare pair of quotes instead, because a blank line was skipped; it no
		// longer is in a one-column file, and `""` is now the empty string, so the
		// blank line is what reads back as the null — as it does in Polars and
		// DuckDB. An empty string is "" from appendQuoted, never blank.
		w.line = append(w.line, w.o.LineTerminator...)
		if _, err := w.w.Write(w.line); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "sink_csv", "writing a record")
		}
	}
	return nil
}

// Close flushes, and ends the codec's stream. It does not close the underlying
// writer — whoever opened it closes it, which is the only rule that works when the
// destination might be os.Stdout.
func (w *Writer) Close() error {
	if w.schema == nil && w.codec == nil {
		return nil // nothing was ever written; there is no header to emit
	}
	if err := w.w.Flush(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_csv", "flushing")
	}
	if w.codec != nil {
		if err := w.codec.Close(); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "sink_csv", "ending the compressed stream")
		}
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

// utf8BOM is the byte-order mark Polars' include_bom writes, U+FEFF in UTF-8.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func (w *Writer) begin(s *dtype.Schema) error {
	w.schema = s
	if w.o.BOM {
		if _, err := w.w.Write(utf8BOM); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "sink_csv", "writing the byte-order mark")
		}
	}
	// No columns, no header: an empty line is not a header naming nothing, and the
	// reader took it for one column named "". A frame with no columns and no rows
	// writes an empty file.
	if !w.o.Header || s.Len() == 0 {
		return nil
	}
	w.line = w.line[:0]
	for i, f := range s.All() {
		if i > 0 {
			w.line = append(w.line, w.o.Separator)
		}
		w.line = w.text(w.line, f.Name)
	}
	w.line = append(w.line, w.o.LineTerminator...)
	if _, err := w.w.Write(w.line); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_csv", "writing the header")
	}
	return nil
}

// text appends s, a field that is not a number, quoted as the style says: when it
// must be, always, or never. A string's own functions are concrete, as a column of
// text is the writer's hottest loop; textOf is the same for a value formatted into a
// buffer.
func (w *Writer) text(dst []byte, s string) []byte {
	switch w.o.QuoteStyle {
	case QuoteAlways, QuoteNonNumeric:
		return w.quoted(dst, s)
	case QuoteNever:
		return append(dst, s...)
	}
	return w.appendQuoted(dst, s)
}

func textOf[S ~string | ~[]byte](w *Writer, dst []byte, s S) []byte {
	switch w.o.QuoteStyle {
	case QuoteAlways, QuoteNonNumeric:
		return quotedOf(w, dst, s)
	case QuoteNever:
		return append(dst, s...)
	}
	return appendQuotedOf(w, dst, s)
}

// number appends a number's text, s: quoted where every field is, and, as Polars
// quotes them, a float or a Decimal, pointed, where a decimal comma meets a comma's
// separator.
func (w *Writer) number(dst []byte, s []byte, pointed bool) []byte {
	switch {
	case w.o.QuoteStyle == QuoteAlways:
		return quotedOf(w, dst, s)
	case w.o.QuoteStyle != QuoteNever && pointed && w.o.DecimalComma && w.o.Separator == ',':
		return quotedOf(w, dst, s)
	}
	return append(dst, s...)
}

// appendQuoted writes s, quoting and escaping only when it must.
//
// The rule is the conventional one: quote if the value contains the separator, a
// quote, or a line terminator, or if it begins with a space (which some readers
// would otherwise strip) — and quote the empty string, so that `""` is "" and an
// empty field is a null, which is how the reader tells them apart.
func (w *Writer) appendQuoted(dst []byte, s string) []byte {
	need := len(s) == 0 || s[0] == ' '
	for i := 0; i < len(s) && !need; i++ {
		switch s[i] {
		case w.o.Separator, w.o.Quote, '\n', '\r':
			need = true
		}
	}
	if !need {
		return append(dst, s...)
	}
	return w.quoted(dst, s)
}

func appendQuotedOf[S ~string | ~[]byte](w *Writer, dst []byte, s S) []byte {
	need := len(s) == 0 || s[0] == ' '
	for i := 0; i < len(s) && !need; i++ {
		switch s[i] {
		case w.o.Separator, w.o.Quote, '\n', '\r':
			need = true
		}
	}
	if !need {
		return append(dst, s...)
	}
	return quotedOf(w, dst, s)
}

// quoted writes s between quotes, each quote within it doubled.
func (w *Writer) quoted(dst []byte, s string) []byte {
	dst = append(dst, w.o.Quote)
	for i := range len(s) {
		if s[i] == w.o.Quote {
			dst = append(dst, w.o.Quote) // double it
		}
		dst = append(dst, s[i])
	}
	return append(dst, w.o.Quote)
}

func quotedOf[S ~string | ~[]byte](w *Writer, dst []byte, s S) []byte {
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
	// wrap adds the null check once, so no individual formatter repeats it.
	wrap := func(f func([]byte, int) []byte) func([]byte, int) []byte {
		return func(dst []byte, row int) []byte {
			if !c.IsValid(row) {
				return append(dst, w.null...)
			}
			return f(dst, row)
		}
	}
	// A number's text is made in scratch, then placed, as the quoting may need it
	// whole; text-like fields likewise.
	var scratch []byte
	place := func(pointed bool) func(func([]byte, int) []byte) func([]byte, int) []byte {
		return func(f func([]byte, int) []byte) func([]byte, int) []byte {
			if w.o.QuoteStyle != QuoteAlways && !(pointed && w.o.DecimalComma) {
				return wrap(f) // the common case, written in place
			}
			return wrap(func(dst []byte, row int) []byte {
				scratch = f(scratch[:0], row)
				return w.number(dst, scratch, pointed)
			})
		}
	}
	numeric, pointed := place(false), place(true)

	dt := c.DType()
	switch dt.ID() {
	case dtype.TypeNull:
		// Every row is null, written as the null value (audit.md I18).
		return func(dst []byte, _ int) []byte { return append(dst, w.null...) }, nil
	case dtype.TypeString, dtype.TypeCategorical:
		s, err := data.TypedColumn[string](c)
		if err != nil {
			return nil, err
		}
		// The style is chosen once, so the default's loop calls appendQuoted itself, as
		// it did before there were styles.
		switch w.o.QuoteStyle {
		case QuoteAlways, QuoteNonNumeric:
			return wrap(func(dst []byte, row int) []byte {
				v, _ := s.Get(row)
				return w.quoted(dst, v)
			}), nil
		case QuoteNever:
			return wrap(func(dst []byte, row int) []byte {
				v, _ := s.Get(row)
				return append(dst, v...)
			}), nil
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
			if v, _ := s.Get(row); v {
				return append(dst, w.yes...)
			}
			return append(dst, w.no...)
		}), nil

	case dtype.TypeFloat32:
		s, err := data.TypedColumn[float32](c)
		if err != nil {
			return nil, err
		}
		return pointed(func(dst []byte, row int) []byte {
			v, _ := s.Get(row)
			return w.floatText(dst, float64(v), 32)
		}), nil
	case dtype.TypeFloat64:
		s, err := data.TypedColumn[float64](c)
		if err != nil {
			return nil, err
		}
		return pointed(func(dst []byte, row int) []byte {
			v, _ := s.Get(row)
			return w.floatText(dst, v, 64)
		}), nil

	case dtype.TypeInt8:
		return appendInt[int8](c, numeric)
	case dtype.TypeInt16:
		return appendInt[int16](c, numeric)
	case dtype.TypeInt32:
		return appendInt[int32](c, numeric)
	case dtype.TypeInt64:
		return appendInt[int64](c, numeric)
	case dtype.TypeUint8:
		return appendUint[uint8](c, numeric)
	case dtype.TypeUint16:
		return appendUint[uint16](c, numeric)
	case dtype.TypeUint32:
		return appendUint[uint32](c, numeric)
	case dtype.TypeUint64:
		return appendUint[uint64](c, numeric)

	case dtype.TypeInt128:
		s, err := data.TypedColumn[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		return numeric(func(dst []byte, row int) []byte {
			v, _ := s.Get(row)
			return append(dst, v.String()...)
		}), nil

	case dtype.TypeDate, dtype.TypeTime, dtype.TypeDatetime:
		return w.temporal(c, wrap)

	case dtype.TypeDuration:
		// Polars refuses a Duration in CSV; it is written as the frame prints it.
		return appendFixed[int64](c, wrap, func(dst []byte, v int64) []byte {
			return w.text(dst, dtype.FormatTemporal(dt, v))
		})

	case dtype.TypeEnum:
		// The text, which is what the reader reads back under the same Enum schema,
		// and what Polars writes; the index would be storage, meaningless outside.
		cats := dt.Categories()
		return appendFixed[uint32](c, wrap, func(dst []byte, v uint32) []byte {
			if int(v) >= len(cats) {
				return dst // unreachable: a valid Enum index is a category
			}
			return w.text(dst, cats[v])
		})

	case dtype.TypeDecimal:
		scale := dt.Scale()
		s, err := data.TypedColumn[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		return pointed(func(dst []byte, row int) []byte {
			v, _ := s.Get(row)
			return w.point(dst, len(dst), dtype.FormatDecimal(v.String(), scale))
		}), nil

	default:
		// Binary and the nested types are refused rather than written as their
		// storage representation; a writer whose output its own reader rejects is
		// worse than one that says so up front. An Enum is written as its text since
		// step 79, which the reader reads back under the same schema. Temporal used to
		// be refused for exactly that reason and no longer is, because the case above
		// formats it the same way the reader parses it.
		return nil, uerr.New(uerr.KindUnsupported, "sink_csv",
			"cannot write column %q of type %s to CSV", c.Name(), dt).
			Hint("CSV supports Bool, the integer and float types, Decimal, the " +
				"temporal types, String and Categorical").
			Hint("cast or format the column before writing")
	}
}

// floatText is a float's text, as the options say: Polars' text, its digits after the
// point fixed, or scientific notation always or never. NaN and the infinities are
// NaN, inf and -inf in each.
func (w *Writer) floatText(dst []byte, v float64, bits int) []byte {
	start := len(dst)
	switch p := w.o.FloatPrecision; {
	case math.IsNaN(v) || math.IsInf(v, 0):
		return kernel.AppendFloatText(dst, v, bits)
	case w.o.FloatScientific > 0:
		dst = appendScientific(dst, v, p, bits)
	case p >= 0:
		dst = strconv.AppendFloat(dst, v, 'f', p, bits)
	case w.o.FloatScientific < 0:
		dst = strconv.AppendFloat(dst, v, 'f', -1, bits)
	default:
		dst = kernel.AppendFloatText(dst, v, bits)
	}
	return w.point(dst, start, "")
}

// point appends s and writes the point of dst's text from start as a comma, where the
// options say so.
func (w *Writer) point(dst []byte, start int, s string) []byte {
	dst = append(dst, s...)
	if w.o.DecimalComma {
		for i := start; i < len(dst); i++ {
			if dst[i] == '.' {
				dst[i] = ','
			}
		}
	}
	return dst
}

// appendScientific is v in scientific notation as Polars writes it, Rust's: its
// shortest digits, or p after the point, and an exponent of no padding and a sign only
// when it is negative, 1.5e0, 1e-7, 1.00e20.
func appendScientific(dst []byte, v float64, p, bits int) []byte {
	start := len(dst)
	dst = strconv.AppendFloat(dst, v, 'e', p, bits)
	at := start
	for dst[at] != 'e' {
		at++
	}
	var exp [8]byte
	n := copy(exp[:], dst[at+2:])
	neg := dst[at+1] == '-'
	dst = append(dst[:at], 'e')
	if neg {
		dst = append(dst, '-')
	}
	digits := exp[:n]
	for len(digits) > 1 && digits[0] == '0' {
		digits = digits[1:]
	}
	return append(dst, digits...)
}

// temporal is a Date, Time or Datetime column's formatter: in the format given for
// its type, or Polars' CSV text, an instant's to its unit, its zone's wall clock and
// offset where it has one. A format that writes what the type has not, a time of day
// of a Date, a date of a Time, a zone of a naive instant, is refused, as Polars
// refuses it.
func (w *Writer) temporal(c *data.Column, wrap func(func([]byte, int) []byte) func([]byte, int) []byte) (func([]byte, int) []byte, error) {
	dt := c.DType()
	var (
		f      *strftime.Format
		layout string
		loc    *time.Location
		zoned  = dt.ID() == dtype.TypeDatetime && dt.TimeZone() != ""
	)
	switch dt.ID() {
	case dtype.TypeDate:
		f, layout = w.dateFmt, "2006-01-02"
		if f != nil && (f.HasTime || f.HasZone || f.HasZoneName) {
			return nil, w.unfit("date", f, dt)
		}
	case dtype.TypeTime:
		f, layout = w.timeFmt, "15:04:05"+fracLayout(dt.TimeUnit())
		if f != nil && (f.HasDate || f.HasZone || f.HasZoneName) {
			return nil, w.unfit("time", f, dt)
		}
	default:
		f, layout = w.datetimeFmt, "2006-01-02T15:04:05"+fracLayout(dt.TimeUnit())
		if zoned {
			layout += "-0700"
			loc = time.UTC
			if tz := dt.TimeZone(); tz != "UTC" {
				l, err := time.LoadLocation(tz)
				if err != nil {
					return nil, uerr.Wrap(err, uerr.KindValue, "sink_csv", "the time zone %q", tz)
				}
				loc = l
			}
		} else if f != nil && (f.HasZone || f.HasZoneName) {
			return nil, w.unfit("datetime", f, dt)
		}
	}
	var scratch []byte
	put := func(dst []byte, ticks int64) []byte {
		t, ok := dt.ToTime(ticks)
		if !ok {
			return strconv.AppendInt(dst, ticks, 10)
		}
		if loc != nil {
			t = t.In(loc)
			// An offset with a seconds part, a local mean time's, has no +hhmm
			// spelling, so such an instant is written in UTC, which reads back exactly.
			if _, off := t.Zone(); off%60 != 0 {
				t = t.UTC()
			}
		}
		if f != nil {
			// The checks above leave no format that can fail on an instant.
			scratch, _ = f.Append(scratch[:0], t, zoned)
		} else {
			scratch = t.AppendFormat(scratch[:0], layout)
		}
		return textOf(w, dst, scratch)
	}
	if dt.Physical().ID() == dtype.TypeInt32 {
		return appendFixed[int32](c, wrap, func(dst []byte, v int32) []byte { return put(dst, int64(v)) })
	}
	return appendFixed[int64](c, wrap, put)
}

func (w *Writer) unfit(kind string, f *strftime.Format, dt dtype.DataType) error {
	return uerr.New(uerr.KindValue, "sink_csv",
		"the %s format %q writes what a %s has not", kind, f.String(), dt).
		Hint("a Date's format writes a date, a Time's a time of day, and a naive Datetime's no zone")
}

// fracLayout is the fractional seconds of a unit's layout: three, six or nine digits.
func fracLayout(u dtype.TimeUnit) string {
	switch u {
	case dtype.Milli:
		return ".000"
	case dtype.Micro:
		return ".000000"
	case dtype.Nano:
		return ".000000000"
	}
	return ""
}

func appendFixed[T data.Fixed](c *data.Column,
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
}](c *data.Column, numeric func(func([]byte, int) []byte) func([]byte, int) []byte,
) (func([]byte, int) []byte, error) {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return nil, err
	}
	return numeric(func(dst []byte, row int) []byte {
		v, _ := s.Get(row)
		return strconv.AppendInt(dst, int64(v), 10)
	}), nil
}

func appendUint[T interface {
	data.Fixed
	~uint8 | ~uint16 | ~uint32 | ~uint64
}](c *data.Column, numeric func(func([]byte, int) []byte) func([]byte, int) []byte,
) (func([]byte, int) []byte, error) {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return nil, err
	}
	return numeric(func(dst []byte, row int) []byte {
		v, _ := s.Get(row)
		return strconv.AppendUint(dst, uint64(v), 10)
	}), nil
}

// noColumns refuses a batch with rows and no columns. A row with no cells has no
// text in CSV and no column chunk in Parquet, so its rows cannot be written; CSV
// wrote them as `""` lines that read back as a column named "", and Parquet as a
// file that reads back as no rows at all.
func noColumns(op, format string, b *data.Batch) error {
	if b.NumCols() > 0 || b.Rows() == 0 {
		return nil
	}
	return uerr.New(uerr.KindValue, op,
		"a frame with no columns cannot be written to %s: its %d rows have no cells", format, b.Rows()).
		Hint("select at least one column before writing")
}
