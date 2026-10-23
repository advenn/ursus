package ursus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	arrowpq "github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"

	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/source/hive"
	"github.com/advenn/ursus/internal/source/parquet"
	"github.com/advenn/ursus/internal/uerr"
)

// ParquetOption configures ScanParquet.
type ParquetOption func(*parquet.Options)

// WithPruning enables or disables row-group skipping from column statistics.
//
// It is on by default. The switch exists because pruning is the one part of the
// Parquet reader that can change which rows come back, so a wrong answer must be
// bisectable to it — the same reason plan.Flags can disable each optimizer rule.
func WithPruning(b bool) ParquetOption { return func(o *parquet.Options) { o.Prune = b } }

// WithHivePartitioning reads a hive dataset's partition columns from its files'
// paths, as Polars' hive_partitioning: each directory named key=value gives the rows
// of the files beneath it a column key of value, its type inferred from every file's
// value, as Polars infers it. On by default for a directory, where the directories
// below it count, and off otherwise; on for a file, a list or a glob, every directory
// of each path counts, as in Polars. New in 0.6.7.
func WithHivePartitioning(b bool) ParquetOption {
	return func(o *parquet.Options) { o.Hive.Partitioning = &b }
}

// WithHiveSchema gives the types of the partition keys it names, as Polars'
// hive_schema; the others are inferred, where Polars refuses a key it does not
// name. New in 0.6.7.
func WithHiveSchema(s *Schema) ParquetOption {
	return func(o *parquet.Options) { o.Hive.Schema = s }
}

// WithHiveDates reads a partition key of dates or of instants as a Date or a
// Datetime, as Polars' try_parse_hive_dates; on by default. New in 0.6.7.
func WithHiveDates(b bool) ParquetOption {
	return func(o *parquet.Options) { o.Hive.NoDates = !b }
}

// WithIncludeFilePaths adds a column named name of each row's file path, last, as
// Polars' include_file_paths. New in 0.6.7.
func WithIncludeFilePaths(name string) ParquetOption {
	return func(o *parquet.Options) { o.Hive.PathColumn = name }
}

// WithParquetSchema reads the files as schema's columns, by name, as Polars'
// scan_parquet schema: a file's other columns refused unless WithIgnoreExtraColumns,
// one it lacks refused unless WithInsertMissingColumns, and one of a narrower type
// refused unless widened. Without it, the files are read as the first file's columns.
// New in 0.6.7.
func WithParquetSchema(s *Schema) ParquetOption {
	return func(o *parquet.Options) { o.Evolve.Schema = s }
}

// WithInsertMissingColumns reads a column a file lacks as nulls in its rows, as
// Polars' missing_columns="insert", where it is refused by default. New in 0.6.7.
func WithInsertMissingColumns(b bool) ParquetOption {
	return func(o *parquet.Options) { o.Evolve.InsertMissing = b }
}

// WithIgnoreExtraColumns passes over a column a file has beyond the schema's, as
// Polars' extra_columns="ignore", where it is refused by default. New in 0.6.7.
func WithIgnoreExtraColumns(b bool) ParquetOption {
	return func(o *parquet.Options) { o.Evolve.IgnoreExtra = b }
}

// WithUpcastIntegers reads an integer column a file holds in a narrower type than the
// schema's as the schema's, an Int32 as an Int64, as Polars'
// ScanCastOptions(integer_cast="upcast"), where it is refused by default. New in
// 0.6.7.
func WithUpcastIntegers(b bool) ParquetOption {
	return func(o *parquet.Options) { o.Evolve.UpcastInts = b }
}

// WithUpcastFloats reads a Float32 column as the schema's Float64, as Polars'
// ScanCastOptions(float_cast="upcast"), where it is refused by default. New in 0.6.7.
func WithUpcastFloats(b bool) ParquetOption {
	return func(o *parquet.Options) { o.Evolve.UpcastFloats = b }
}

func parquetOptions(opts []ParquetOption) parquet.Options {
	o := parquet.DefaultOptions()
	for _, f := range opts {
		f(&o)
	}
	return o
}

func fileOpener(path string) parquet.Opener {
	return func(context.Context) (arrowpq.ReaderAtSeeker, io.Closer, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		return f, f, nil
	}
}

// ScanParquet reads a Parquet file lazily.
//
// Only the footer is read until Collect. Projection reaches the reader, so a query
// selecting two columns of a hundred never decompresses the other ninety-eight,
// and a predicate on a column with statistics skips whole row groups.
//
//	df, err := ursus.ScanParquet("events.parquet").
//	    Filter(ursus.Col("ts").Gt(cutoff)).
//	    Select(ursus.Col("user"), ursus.Col("ts")).
//	    Collect(ctx)
//
// Lists and structs are read; a column ursus cannot read — an encrypted one, a map,
// a list of lists — is refused with an error naming it when a query reads it,
// rather than dropped.
//
// A directory is read whole, every file below it in the order of their paths, as
// Polars reads one, and as a hive dataset: each directory below it named key=value
// gives its files' rows a column key, after the files' own columns, of the type the
// values infer; a filter on the keys skips the files it rules out before any is
// opened. A name beginning '.' or '_' is passed over. See WithHivePartitioning,
// WithHiveSchema, WithHiveDates and WithIncludeFilePaths. New in 0.6.7.
func ScanParquet(path string, opts ...ParquetOption) *LazyFrame {
	o := parquetOptions(opts)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		paths, err := datasetFiles(path)
		if err != nil {
			return &LazyFrame{err: err}
		}
		return scanParquetPaths(paths, path, path, o)
	}
	return scanParquetPaths([]string{path}, path, "", o)
}

// datasetFiles is every file below dir, as Polars reads a directory, sorted by path:
// a name beginning '.' or '_', a file's or a directory's, is passed over, as Spark and
// Hive name what they keep beside the data: _SUCCESS, _temporary, .crc files.
func datasetFiles(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p != dir && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")):
			if d.IsDir() {
				return filepath.SkipDir
			}
		case !d.IsDir():
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet", "listing %s", dir)
	}
	if len(paths) == 0 {
		return nil, uerr.New(uerr.KindIO, "scan_parquet", "no files in %s", dir)
	}
	sort.Strings(paths)
	return paths, nil
}

// scanParquetPaths reads paths as one frame, described as desc: their partition keys
// read below root, a directory scanned, or from the whole of each where root is "",
// where hive partitioning is on, and their paths where asked for.
func scanParquetPaths(paths []string, desc, root string, o parquet.Options) *LazyFrame {
	opens := make([]parquet.Opener, len(paths))
	for i, p := range paths {
		opens[i] = fileOpener(p)
	}
	if err := partition(&o, paths, root); err != nil {
		return &LazyFrame{err: err}
	}
	return scan(parquet.NewNamed(opens, paths, desc, o))
}

// partition sets o's partition columns from paths, as o.Hive asks: hive partitioning
// is on by default where a directory is scanned, root.
func partition(o *parquet.Options, paths []string, root string) error {
	on := root != ""
	if o.Hive.Partitioning != nil {
		on = *o.Hive.Partitioning
	}
	if !on && o.Hive.PathColumn == "" {
		return nil
	}
	pt := &parquet.Partitioned{Dates: !o.Hive.NoDates, PathColumn: o.Hive.PathColumn, Paths: paths}
	if on {
		parts, err := hive.Parse(root, paths)
		if err != nil {
			return err
		}
		if len(parts.Keys) > 0 {
			pt.Parts = parts
		}
		if s := o.Hive.Schema; s != nil {
			pt.Types = make(map[string]DataType, s.Len())
			for _, f := range s.FieldSlice() {
				pt.Types[f.Name] = f.Type
			}
		}
	}
	o.Partitioned = pt
	return nil
}

// ScanParquetGlob reads every file matching a shell pattern as one frame, sorted by
// name so `part-*.parquet` reads in the order the names imply.
func ScanParquetGlob(pattern string, opts ...ParquetOption) *LazyFrame {
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return &LazyFrame{err: uerr.Wrap(err, uerr.KindValue, "scan_parquet",
			"bad glob pattern %q", pattern)}
	}
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindIO, "scan_parquet",
			"no files match %q", pattern)}
	}
	sort.Strings(paths)
	return ScanParquetFiles(paths, opts...)
}

// ScanParquetFiles reads several files as one frame, in the order given.
//
// Columns are matched by NAME: every file must have the same columns, of the same
// types, in any order. A file that differs is refused with ErrSchema, naming both
// files and the difference, when the query is planned — every file's footer is
// read then, before any row is produced. A column is nullable if it is nullable in
// any file. To combine files that differ, scan them separately and Concat them.
func ScanParquetFiles(paths []string, opts ...ParquetOption) *LazyFrame {
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_parquet", "no files given")}
	}
	desc := paths[0]
	if len(paths) > 1 {
		desc += " and " + strconv.Itoa(len(paths)-1) + " more"
	}
	return scanParquetPaths(paths, desc, "", parquetOptions(opts))
}

// ScanParquetBytes reads a Parquet file from memory.
func ScanParquetBytes(b []byte, name string, opts ...ParquetOption) *LazyFrame {
	open := func(context.Context) (arrowpq.ReaderAtSeeker, io.Closer, error) {
		return bytes.NewReader(b), nil, nil
	}
	return scan(parquet.New([]parquet.Opener{open}, name, parquetOptions(opts)))
}

// ParquetFile is one Parquet file, read through ReadAt from wherever it lives — an
// object store, an HTTP server, an archive — by code that knows how to reach it.
//
// ursus reads a file the way an object store wants to be read: the footer from the
// end, then one ranged read for each column chunk the query needs, after the
// statistics have pruned the row groups it does not. A ReadAt over a ranged GET
// therefore fetches what the query reads and nothing else.
type ParquetFile struct {
	// Name identifies the file in errors and in Explain.
	Name string

	// Open returns the file's bytes and their size.
	//
	// It is called more than once per query — once to read the schema when the
	// query is planned, once for each execution — and must return a fresh reader
	// each time. ctx is the query's and outlives the reader, so a reader over a
	// network may keep it for its reads. If the reader is also an io.Closer, ursus
	// closes it when it is done with it.
	Open func(ctx context.Context) (r io.ReaderAt, size int64, err error)
}

// ScanParquetFrom reads Parquet files the caller opens, as one frame in the order
// given, under ScanParquetFiles' rules for several files.
//
// It is how ursus reaches an object store without depending on one: give each file
// an Open that returns a ReaderAt over ranged GETs.
//
//	files := []ursus.ParquetFile{{
//	    Name: "s3://logs/2026/10/part-0.parquet",
//	    Open: func(ctx context.Context) (io.ReaderAt, int64, error) {
//	        return newRangedReader(ctx, client, "logs", "2026/10/part-0.parquet")
//	    },
//	}}
//	df, err := ursus.ScanParquetFrom(files).Filter(ursus.Col("status").Eq(500)).Collect(ctx)
func ScanParquetFrom(files []ParquetFile, opts ...ParquetOption) *LazyFrame {
	if len(files) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_parquet", "no files given")}
	}
	opens := make([]parquet.Opener, len(files))
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
		if names[i] == "" {
			names[i] = "parquet file " + strconv.Itoa(i+1)
		}
		if f.Open == nil {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_parquet",
				"%s has no Open function", names[i])}
		}
		opens[i] = readerAtOpener(f.Open)
	}
	desc := names[0]
	if len(names) > 1 {
		desc += " and " + strconv.Itoa(len(names)-1) + " more"
	}
	o := parquetOptions(opts)
	if err := partition(&o, names, ""); err != nil {
		return &LazyFrame{err: err}
	}
	return scan(parquet.NewNamed(opens, names, desc, o))
}

// readerAtOpener adapts a ParquetFile's Open to the reader's Opener: a section of
// the given size is what the footer is found at the end of.
func readerAtOpener(open func(context.Context) (io.ReaderAt, int64, error)) parquet.Opener {
	return func(ctx context.Context) (arrowpq.ReaderAtSeeker, io.Closer, error) {
		r, size, err := open(ctx)
		if err != nil {
			return nil, nil, err
		}
		c, _ := r.(io.Closer)
		if r == nil || size < 0 {
			if c != nil {
				c.Close()
			}
			return nil, nil, fmt.Errorf("Open returned a nil reader or a negative size (%d)", size)
		}
		return io.NewSectionReader(r, 0, size), c, nil
	}
}

// --- writing -------------------------------------------------------------------

// ParquetWriteOption configures the Parquet writer.
type ParquetWriteOption func(*parquet.WriteOptions)

// ParquetSinkOption is anything SinkParquet and WriteParquet accept: a writer
// option (WithCompression, WithRowGroupRows, WithStatistics) or an execution
// option (WithMemoryLimit, WithSpillDir, WithBatchSize, WithThreads).
//
// A sink is where a memory limit matters most — it is the consumer that streams,
// so it is the one a larger-than-RAM query ends in — and before this the two
// option families could not meet.
//
// It is an interface rather than a variadic `...any` because the union has to be
// checked at compile time. A library that uses generic methods and an Operand
// constraint to stop `Col("x").Gt(struct{}{})` from compiling should not then
// accept SinkParquet(ctx, path, "oops").
type ParquetSinkOption interface{ applyParquetSink(*parquetSinkCfg) }

type parquetSinkCfg struct {
	write   parquet.WriteOptions
	collect collectCfg
}

func (o ParquetWriteOption) applyParquetSink(c *parquetSinkCfg) { o(&c.write) }
func (o CollectOption) applyParquetSink(c *parquetSinkCfg)      { o(&c.collect) }

func parquetSinkOptions(opts []ParquetSinkOption) parquetSinkCfg {
	cfg := parquetSinkCfg{write: parquet.DefaultWriteOptions(), collect: baseCollectCfg()}
	for _, o := range opts {
		o.applyParquetSink(&cfg)
	}
	cfg.collect.finish()
	return cfg
}

// WithCompression sets the codec applied to every column. Default Snappy.
func WithCompression(c compress.Compression) ParquetWriteOption {
	return func(o *parquet.WriteOptions) { o.Compression = c }
}

// WithRowGroupRows sets the target row-group size. It is the unit of both pruning
// granularity and writer memory: smaller groups prune better and buffer less.
func WithRowGroupRows(n int) ParquetWriteOption {
	return func(o *parquet.WriteOptions) { o.RowGroupRows = n }
}

// WithStatistics enables or disables column statistics. On by default — a file
// without them cannot be pruned, which gives up the main reason to use Parquet.
func WithStatistics(b bool) ParquetWriteOption {
	return func(o *parquet.WriteOptions) { o.Stats = b }
}

// SinkParquet runs the query and writes the result to path, streaming.
//
// Memory is one row group rather than the whole result, so this is the write half
// of "larger than RAM". The file is written to a temporary name and renamed on
// success: a Parquet file without its footer is not merely truncated, it is
// unreadable, so leaving a partial one where a complete one is expected would be
// worse than leaving nothing.
func (lf *LazyFrame) SinkParquet(ctx context.Context, path string, opts ...ParquetSinkOption) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_parquet",
			"creating a temporary file for %s", path)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // a no-op on the success path, which ends with a rename
	}()

	if err := lf.WriteParquet(ctx, tmp, opts...); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_parquet", "closing %s", tmpName)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_parquet", "renaming to %s", path)
	}
	return nil
}

// WriteParquet runs the query and writes the result to w, streaming. It does not
// close w.
func (lf *LazyFrame) WriteParquet(ctx context.Context, w io.Writer, opts ...ParquetSinkOption) (err error) {
	defer uerr.Catch(&err, "sink_parquet")
	cfg := parquetSinkOptions(opts)
	defer cfg.collect.report()
	root, err := lf.compile(ctx, cfg.collect)
	if err != nil {
		return err
	}
	defer root.Close()

	pw, err := parquet.NewWriter(w, exec.Schema(root), cfg.write)
	if err != nil {
		return err
	}
	for b, err := range exec.Batches(ctx, root) {
		if err != nil {
			pw.Close()
			return err
		}
		if err := pw.WriteBatch(b); err != nil {
			pw.Close()
			return err
		}
	}
	// Close writes the footer. A Parquet file without one cannot be opened at all.
	return pw.Close()
}
