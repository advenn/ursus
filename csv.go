package ursus

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/source/csv"
	"github.com/advenn/ursus/internal/uerr"
)

// CSVOption configures ScanCSV.
type CSVOption func(*csv.Options)

// WithSeparator sets the field delimiter. Default ','.
func WithSeparator(c byte) CSVOption { return func(o *csv.Options) { o.Separator = c } }

// WithQuote sets the quote character. Default '"'.
func WithQuote(c byte) CSVOption { return func(o *csv.Options) { o.Quote = c } }

// WithComment makes lines starting with prefix comments. Multi-byte prefixes such
// as "//" are supported.
func WithComment(prefix string) CSVOption {
	return func(o *csv.Options) { o.Comment = []byte(prefix) }
}

// WithHasHeader says whether the first record holds column names. Default true.
func WithHasHeader(b bool) CSVOption { return func(o *csv.Options) { o.HasHeader = b } }

// WithColumnNames overrides the column names positionally.
func WithColumnNames(names ...string) CSVOption {
	return func(o *csv.Options) { o.ColumnNames = names }
}

// WithSkipRows discards n records before the header.
func WithSkipRows(n int) CSVOption { return func(o *csv.Options) { o.SkipRows = n } }

// WithSchema supplies the schema and skips inference entirely. On a large file
// this is the option that matters: inference costs a read of the sample, and an
// explicit schema also removes the risk of a column being typed from rows that do
// not represent it.
func WithSchema(s *Schema) CSVOption { return func(o *csv.Options) { o.Schema = s } }

// WithSchemaOverrides fixes individual columns while inferring the rest. The usual
// case is an identity column that looks numeric and must not be.
func WithSchemaOverrides(m map[string]dtype.DataType) CSVOption {
	return func(o *csv.Options) { o.SchemaOverrides = m }
}

// WithInferRows bounds how many records inference reads. 0 reads the whole file,
// which is exact and, on a large file, expensive. Default 100.
func WithInferRows(n int) CSVOption { return func(o *csv.Options) { o.InferRows = n } }

// WithNullValues adds texts that mean NULL. An empty field already means null for
// every type; in a String column a quoted "" is the empty string, as Polars reads
// it, so the two stay apart.
func WithNullValues(vals ...string) CSVOption {
	return func(o *csv.Options) { o.NullValues = vals }
}

// WithTruncateRaggedLines accepts a record of too many fields, the extra ones
// dropped, as Polars' truncate_ragged_lines; by default it is refused. A record of
// too few is padded with nulls either way, as Polars pads one.
func WithTruncateRaggedLines(b bool) CSVOption {
	return func(o *csv.Options) { o.TruncateRaggedLines = b }
}

// WithMaxRecordSize bounds a single record. Default 16 MiB. The bound exists so an
// unterminated quote is an error rather than an out-of-memory kill.
func WithMaxRecordSize(n int) CSVOption { return func(o *csv.Options) { o.MaxRecordSize = n } }

// WithTryParseDates infers a column of dates, instants or times of day as a Date, a
// Datetime in microseconds, or a Time, as Polars' try_parse_dates: 2024-01-02 and
// 02/01/2024 dates, the year first or the day, split by '-', '/' or '.';
// 2024-01-02 03:04:05.123 instants, a 'T' or a space between, and in UTC where they
// have an offset, Z, +09:00 or +0900; and 03:04:05 times. A month first is not
// read: 01/02/2024 is the first of February. A date beside an instant, or an
// instant in a zone beside one in none, is text. Off by default, as Polars'. A Date,
// Datetime or Time column a schema gives is read by the same patterns. New in 0.6.7.
func WithTryParseDates(b bool) CSVOption { return func(o *csv.Options) { o.TryParseDates = b } }

// WithIgnoreErrors reads a value its column's type cannot take as a null, as Polars'
// ignore_errors. A record of too many fields is refused still, and text that is not
// UTF-8, as Polars refuses them. New in 0.6.7.
func WithIgnoreErrors(b bool) CSVOption { return func(o *csv.Options) { o.IgnoreErrors = b } }

// WithInferSchema(false) reads every column as a String, but those a schema or an
// override types, as Polars' infer_schema=False. New in 0.6.7.
func WithInferSchema(b bool) CSVOption { return func(o *csv.Options) { o.NoInference = !b } }

// WithRaiseIfEmpty(false) reads an empty file as a frame of no columns and no rows,
// as Polars' raise_if_empty=False, where by default it is refused. New in 0.6.7.
func WithRaiseIfEmpty(b bool) CSVOption { return func(o *csv.Options) { o.AllowEmpty = !b } }

// WithDecimalComma reads a float's and a Decimal's point as a comma, 1,5, as Polars'
// read_csv(decimal_comma=True), inferred so too: a value with a point is then text,
// and under a comma separator a number with a comma is quoted. New in 0.6.7.
func WithDecimalComma(b bool) CSVOption { return func(o *csv.Options) { o.DecimalComma = b } }

// CSVEncoding is how a CSV file's text is read, as Polars' read_csv encoding.
type CSVEncoding int

const (
	// UTF8 refuses a String's text that is not UTF-8, as Polars does: the default. A
	// column's name is read as UTF8Lossy reads it either way, as Polars reads one.
	UTF8 CSVEncoding = iota
	// UTF8Lossy reads each invalid sequence as U+FFFD, as Polars' "utf8-lossy".
	UTF8Lossy
)

// WithEncoding sets how text is read: UTF8, the default, or UTF8Lossy. New in 0.6.7.
func WithEncoding(e CSVEncoding) CSVOption {
	return func(o *csv.Options) { o.Lossy = e == UTF8Lossy }
}

func csvOptions(opts []CSVOption) csv.Options {
	o := csv.DefaultOptions()
	for _, f := range opts {
		f(&o)
	}
	return o
}

// ScanCSV reads a CSV file lazily, or every file below a directory, as Polars reads
// one: as ScanCSVFiles of them, sorted by path, a name beginning '.' or '_' passed over
// and files of two extensions refused (the directory new in 0.6.7).
//
// Nothing is read until Collect, except the sample inference needs — and not even
// that if WithSchema is given. Projection reaches the reader, so selecting two
// columns of a hundred parses two.
//
// A file compressed by gzip, zlib or Zstandard is read as itself, known by its
// first bytes whatever its name, as Polars reads one, and decompressed as it is
// read; so is every CSV, NDJSON and JSON scan's stream. New in 0.6.7.
//
//	df, err := ursus.ScanCSV("sales.csv").
//	    Filter(ursus.Col("qty").Gt(10)).
//	    Select(ursus.Col("region"), ursus.Col("qty")).
//	    Collect(ctx)
func ScanCSV(path string, opts ...CSVOption) *LazyFrame {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		paths, err := datasetFiles("scan_csv", path)
		if err != nil {
			return &LazyFrame{err: err}
		}
		return ScanCSVFiles(paths, opts...)
	}
	return scan(csv.FromFile(path, csvOptions(opts)))
}

// ScanCSVGlob reads every file matching a shell pattern as one frame.
//
// The files must share their columns; the types are inferred from the first match,
// and every other file's header is matched to the first's by NAME — see
// ScanCSVFiles. Matches are sorted, so `part-*.csv` reads in the order the names imply rather than whatever
// the filesystem returns — row order is a property of the data here, and leaving
// it to readdir would make the same query return different orders on different
// machines. An element of ** matches any number of directories, none among them, as
// Polars' globs do: `logs/**/*.csv`. New in 0.6.7.
func ScanCSVGlob(pattern string, opts ...CSVOption) *LazyFrame {
	paths, err := globFiles(pattern)
	if err != nil {
		return &LazyFrame{err: uerr.Wrap(err, uerr.KindValue, "scan_csv",
			"bad glob pattern %q", pattern)}
	}
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindIO, "scan_csv",
			"no files match %q", pattern)}
	}
	sort.Strings(paths)
	return ScanCSVFiles(paths, opts...)
}

// ScanCSVFiles reads several files as one frame, in the order given.
//
// Columns are matched by the NAMES in each file's header: every file must name the
// same columns, in any order. A file that differs — a missing, extra, renamed or
// repeated column, or no header at all — is refused with ErrSchema, naming both
// files, when the scan reaches it. The types are inferred from the first file
// (or given by WithSchema). WithColumnNames renames the first file's columns by
// position, and later files are still matched to it by what the files say. With
// WithHasHeader(false) there is nothing to match, and files are read by position.
// A UTF-8 byte-order mark at the start of a file is dropped.
func ScanCSVFiles(paths []string, opts ...CSVOption) *LazyFrame {
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_csv", "no files given")}
	}
	if len(paths) == 1 {
		return ScanCSV(paths[0], opts...)
	}
	desc := paths[0] + " and " + strconv.Itoa(len(paths)-1) + " more"
	return scan(csv.FromFiles(paths, desc, csvOptions(opts)))
}

// ScanCSVReader reads CSV from memory.
//
// It takes the bytes rather than an io.Reader because a source is opened more than
// once — once for inference, once per execution — and a Reader cannot be rewound.
func ScanCSVReader(b []byte, name string, opts ...CSVOption) *LazyFrame {
	open := func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	return scan(csv.New(open, name, csvOptions(opts)))
}

// CSVFile is one CSV file, read as a stream from wherever it lives — an object
// store, an HTTP server, an archive — by code that knows how to reach it.
type CSVFile struct {
	// Name identifies the file in errors and in Explain.
	Name string

	// Open returns the file's bytes from the start.
	//
	// It is called more than once per query — once to infer the schema, once for
	// each execution — and must return a fresh stream each time. ctx is the query's
	// and outlives the stream, which ursus closes when it is done with it.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// ScanCSVFrom reads CSV files the caller opens, as one frame in the order given,
// under ScanCSVFiles' rules for several files.
//
// It is how ursus reaches an object store without depending on one: give each file
// an Open that returns the body of a GET.
func ScanCSVFrom(files []CSVFile, opts ...CSVOption) *LazyFrame {
	if len(files) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_csv", "no files given")}
	}
	opens := make([]csv.Opener, len(files))
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
		if names[i] == "" {
			names[i] = "csv file " + strconv.Itoa(i+1)
		}
		if f.Open == nil {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_csv",
				"%s has no Open function", names[i])}
		}
		opens[i] = csv.Opener(f.Open)
	}
	desc := names[0]
	if len(names) > 1 {
		desc += " and " + strconv.Itoa(len(names)-1) + " more"
	}
	return scan(csv.NewNamed(opens, names, desc, csvOptions(opts)))
}

// --- writing -------------------------------------------------------------------

// CSVWriteOption configures the CSV writer.
type CSVWriteOption func(*csv.WriteOptions)

// CSVSinkOption is anything SinkCSV and WriteCSV accept: a writer option or an
// execution option. See ParquetSinkOption for why it is an interface.
type CSVSinkOption interface{ applyCSVSink(*csvSinkCfg) }

type csvSinkCfg struct {
	write   csv.WriteOptions
	collect collectCfg
}

func (o CSVWriteOption) applyCSVSink(c *csvSinkCfg) { o(&c.write) }
func (o CollectOption) applyCSVSink(c *csvSinkCfg)  { o(&c.collect) }

func csvSinkOptions(opts []CSVSinkOption) csvSinkCfg {
	cfg := csvSinkCfg{write: csv.DefaultWriteOptions(), collect: baseCollectCfg()}
	for _, o := range opts {
		o.applyCSVSink(&cfg)
	}
	cfg.collect.finish()
	return cfg
}

// WithWriteSeparator sets the delimiter. Default ','.
func WithWriteSeparator(c byte) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.Separator = c }
}

// WithWriteHeader says whether to write column names first. Default true.
func WithWriteHeader(b bool) CSVWriteOption { return func(o *csv.WriteOptions) { o.Header = b } }

// WithLineTerminator sets the record separator. Default "\n".
func WithLineTerminator(s string) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.LineTerminator = s }
}

// WithNullValue sets the text written for a null. Default "".
//
// The default reads back as null for every type. An empty String is written as a
// quoted "", which reads back as the empty string, so null and empty strings both
// round-trip — as Polars writes and reads them.
func WithNullValue(s string) CSVWriteOption { return func(o *csv.WriteOptions) { o.NullValue = s } }

// QuoteStyle is when the CSV writer quotes a field, as Polars' quote_style. New in
// 0.6.7.
type QuoteStyle = csv.QuoteStyle

const (
	// QuoteNecessary quotes a field holding the separator, the quote or a line
	// break, the empty string, and one beginning with a space: the default.
	QuoteNecessary = csv.QuoteNecessary
	// QuoteAlways quotes every field, the header's and a null's too.
	QuoteAlways = csv.QuoteAlways
	// QuoteNever quotes no field.
	QuoteNever = csv.QuoteNever
	// QuoteNonNumeric quotes every field but a number and a null.
	QuoteNonNumeric = csv.QuoteNonNumeric
)

// WithQuoteStyle sets when a field is quoted, as Polars' quote_style. New in 0.6.7.
func WithQuoteStyle(s QuoteStyle) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.QuoteStyle = s }
}

// WithWriteQuote sets the quote, as Polars' quote_char. Default '"'. New in 0.6.7.
func WithWriteQuote(c byte) CSVWriteOption { return func(o *csv.WriteOptions) { o.Quote = c } }

// WithDateFormat writes a Date in a strftime format, "%d/%m/%Y", as Polars'
// date_format, where the default is 2024-01-02. A format of a time of day is refused.
// New in 0.6.7.
func WithDateFormat(f string) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.DateFormat = f }
}

// WithDatetimeFormat writes a Datetime in a strftime format, "%Y-%m-%d %H:%M", as
// Polars' datetime_format, a zoned one on its zone's wall clock. The default is
// 2024-01-02T03:04:05.123456, to the instant's unit, with its offset, +0900, where it
// has a zone. A naive instant's format of a zone, %z or %Z, is refused. New in 0.6.7.
func WithDatetimeFormat(f string) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.DatetimeFormat = f }
}

// WithTimeFormat writes a Time in a strftime format, "%H:%M", as Polars'
// time_format, where the default is 03:04:05.000000000. New in 0.6.7.
func WithTimeFormat(f string) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.TimeFormat = f }
}

// WithFloatPrecision writes every float with n digits after its point, as Polars'
// float_precision: 2 writes 1.5 as 1.50. New in 0.6.7.
func WithFloatPrecision(n int) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.FloatPrecision = n }
}

// WithFloatScientific writes every float in scientific notation, 1.5e0 and 1e-7, or
// with false none, 100000000000000000000, as Polars' float_scientific; by default a
// float is scientific past 1e16 and below 1e-5. New in 0.6.7.
func WithFloatScientific(b bool) CSVWriteOption {
	return func(o *csv.WriteOptions) {
		o.FloatScientific = -1
		if b {
			o.FloatScientific = 1
		}
	}
}

// WithWriteDecimalComma writes a float's and a Decimal's point as a comma, 1,5, as
// Polars' write_csv(decimal_comma=True); under a comma separator such a field is
// quoted. New in 0.6.7.
func WithWriteDecimalComma(b bool) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.DecimalComma = b }
}

// WithIncludeBOM begins the file with a UTF-8 byte-order mark, as Polars'
// include_bom, which ScanCSV skips. New in 0.6.7.
func WithIncludeBOM(b bool) CSVWriteOption { return func(o *csv.WriteOptions) { o.BOM = b } }

// CSVCompression is the codec a CSV file is written in, as Polars' write_csv
// compression. New in 0.6.7.
type CSVCompression = csv.Compression

const (
	CSVUncompressed = csv.Uncompressed // the default
	CSVGzip         = csv.Gzip         // gzip, a path's name ending .gz
	CSVZstd         = csv.Zstd         // Zstandard, .zst
)

// WithCSVCompression writes the text compressed, at the codec's default level, as
// Polars' compression; ScanCSV reads it back by its first bytes. SinkCSV's path must
// end as the codec's files do, .gz or .zst, and an uncompressed one must not, as
// Polars checks unless WithCSVCheckExtension(false) says otherwise. New in 0.6.7.
func WithCSVCompression(c CSVCompression) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.Compression = c }
}

// WithCSVCompressionLevel sets the codec's level, as Polars' compression_level:
// gzip's 0 to 9, where the default is 6, and Zstandard's 1 to 22, where it is 3.
// New in 0.6.7.
func WithCSVCompressionLevel(level int) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.CompressionLevel = level }
}

// WithCSVCheckExtension says whether SinkCSV checks its path's extension against the
// compression, as Polars' check_extension. Default true. New in 0.6.7.
func WithCSVCheckExtension(b bool) CSVWriteOption {
	return func(o *csv.WriteOptions) { o.SkipExtensionCheck = !b }
}

// SinkCSV runs the query and writes the result to path, streaming.
//
// This is the half that makes "larger than RAM" true end to end. Collect holds the
// whole result; this holds one batch, so a query that reads more data than fits in
// memory and writes more data than fits in memory works.
//
// The file is written to a temporary name and renamed on success, so a failed
// query leaves no half-written file where a complete one is expected.
func (lf *LazyFrame) SinkCSV(ctx context.Context, path string, opts ...CSVSinkOption) error {
	if err := csv.CheckExtension(path, csvSinkOptions(opts).write); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_csv", "creating a temporary file for %s", path)
	}
	tmpName := tmp.Name()
	// Both cleanups are no-ops on the success path, which ends with a rename.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if err := lf.WriteCSV(ctx, tmp, opts...); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_csv", "closing %s", tmpName)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_csv", "renaming to %s", path)
	}
	return nil
}

// WriteCSV runs the query and writes the result to w, streaming.
//
// It does not close w. Whoever opened it closes it — the only rule that works when
// the destination might be os.Stdout.
func (lf *LazyFrame) WriteCSV(ctx context.Context, w io.Writer, opts ...CSVSinkOption) (err error) {
	defer uerr.Catch(&err, "sink_csv")
	cfg := csvSinkOptions(opts)
	defer cfg.collect.report()
	root, err := lf.compile(ctx, cfg.collect)
	if err != nil {
		return err
	}
	defer root.Close()

	cw, err := csv.NewWriter(w, cfg.write)
	if err != nil {
		return err
	}
	// A result with no rows still has columns, and a file with no header would be
	// unreadable rather than empty.
	if err := cw.WriteEmpty(exec.Schema(root)); err != nil {
		return err
	}
	for b, err := range exec.Batches(ctx, root) {
		if err != nil {
			return err
		}
		if err := cw.WriteBatch(b); err != nil {
			return err
		}
	}
	return cw.Close()
}
