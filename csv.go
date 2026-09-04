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

// WithNullValues adds texts that mean NULL. The empty string already means null
// for every type except String, where "" is a value.
func WithNullValues(vals ...string) CSVOption {
	return func(o *csv.Options) { o.NullValues = vals }
}

// WithTruncateRaggedLines accepts records with the wrong field count: extra fields
// are dropped and missing ones become null. Off by default, because a changed
// field count usually means the file is not what the reader thinks it is.
func WithTruncateRaggedLines(b bool) CSVOption {
	return func(o *csv.Options) { o.TruncateRaggedLines = b }
}

// WithMaxRecordSize bounds a single record. Default 16 MiB. The bound exists so an
// unterminated quote is an error rather than an out-of-memory kill.
func WithMaxRecordSize(n int) CSVOption { return func(o *csv.Options) { o.MaxRecordSize = n } }

func csvOptions(opts []CSVOption) csv.Options {
	o := csv.DefaultOptions()
	for _, f := range opts {
		f(&o)
	}
	return o
}

// ScanCSV reads a CSV file lazily.
//
// Nothing is read until Collect, except the sample inference needs — and not even
// that if WithSchema is given. Projection reaches the reader, so selecting two
// columns of a hundred parses two.
//
//	df, err := ursus.ScanCSV("sales.csv").
//	    Filter(ursus.Col("qty").Gt(10)).
//	    Select(ursus.Col("region"), ursus.Col("qty")).
//	    Collect(ctx)
func ScanCSV(path string, opts ...CSVOption) *LazyFrame {
	return Scan(csv.FromFile(path, csvOptions(opts)))
}

// ScanCSVGlob reads every file matching a shell pattern as one frame.
//
// The files must share a schema; it is taken from the first match. Matches are
// sorted, so `part-*.csv` reads in the order the names imply rather than whatever
// the filesystem returns — row order is a property of the data here, and leaving
// it to readdir would make the same query return different orders on different
// machines.
func ScanCSVGlob(pattern string, opts ...CSVOption) *LazyFrame {
	paths, err := filepath.Glob(pattern)
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
func ScanCSVFiles(paths []string, opts ...CSVOption) *LazyFrame {
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_csv", "no files given")}
	}
	if len(paths) == 1 {
		return ScanCSV(paths[0], opts...)
	}
	desc := paths[0] + " and " + strconv.Itoa(len(paths)-1) + " more"
	return Scan(csv.FromFiles(paths, desc, csvOptions(opts)))
}

// ScanCSVReader reads CSV from memory.
//
// It takes the bytes rather than an io.Reader because a source is opened more than
// once — once for inference, once per execution — and a Reader cannot be rewound.
func ScanCSVReader(b []byte, name string, opts ...CSVOption) *LazyFrame {
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	return Scan(csv.New(open, name, csvOptions(opts)))
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
// The default reads back as null for every type except String, where "" is a real
// value the reader cannot distinguish from a missing one. A round trip that must
// preserve null strings needs a sentinel on both sides: WithNullValue("\\N") here
// and WithNullValues("\\N") on the read.
func WithNullValue(s string) CSVWriteOption { return func(o *csv.WriteOptions) { o.NullValue = s } }

// SinkCSV runs the query and writes the result to path, streaming.
//
// This is the half that makes "larger than RAM" true end to end. Collect holds the
// whole result; this holds one batch, so a query that reads more data than fits in
// memory and writes more data than fits in memory works.
//
// The file is written to a temporary name and renamed on success, so a failed
// query leaves no half-written file where a complete one is expected.
func (lf *LazyFrame) SinkCSV(ctx context.Context, path string, opts ...CSVSinkOption) error {
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
func (lf *LazyFrame) WriteCSV(ctx context.Context, w io.Writer, opts ...CSVSinkOption) error {
	cfg := csvSinkOptions(opts)
	defer cfg.collect.report()
	root, err := lf.compile(ctx, cfg.collect)
	if err != nil {
		return err
	}
	defer root.Close()

	cw := csv.NewWriter(w, cfg.write)
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
