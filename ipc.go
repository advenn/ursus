package ursus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/source/arrowsrc"
	"github.com/advenn/ursus/internal/source/files"
	"github.com/advenn/ursus/internal/source/hive"
	"github.com/advenn/ursus/internal/uerr"
)

// Arrow IPC (step 332): Polars' scan_ipc, read_ipc, read_ipc_stream, write_ipc,
// write_ipc_stream, sink_ipc and read_ipc_schema, and read_parquet_schema.

// ScanIPC reads an Arrow IPC file, Feather version 2, the format of Polars'
// write_ipc and read_ipc: its record batches a batch at a time, each column as
// ScanArrow converts one, string views among them, which Polars writes. The file is
// opened each time the query is planned or run, and closed after.
//
// New in 0.6.7: a directory is read whole, every file below it in the order of their
// paths, as a hive dataset, each directory below it named key=value giving its files'
// rows a column key after the files' own, as Polars' scan_ipc reads one; see
// WithIPCHivePartitioning, WithIPCHiveSchema, WithIPCHiveDates and
// WithIPCIncludeFilePaths. Several files are read as the first one's columns, by
// name, as ScanIPCFiles reads them.
func ScanIPC(path string, opts ...IPCScanOption) *LazyFrame {
	o := ipcScanOptions(opts)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		paths, err := datasetFiles("scan_ipc", path)
		if err != nil {
			return &LazyFrame{err: err}
		}
		return scanIPCPaths(paths, path, path, o)
	}
	if o == (ipcScan{}) {
		return ScanArrow(func() (array.RecordReader, error) { return openIPCFile("scan_ipc", path) })
	}
	return scanIPCPaths([]string{path}, path, "", o)
}

// ScanIPCFiles reads IPC files as one frame, in the order given, as the first one's
// columns: every other file's are read by name, and one it lacks, one more or one of
// another type is refused, as Polars refuses them. New in 0.6.7.
func ScanIPCFiles(paths []string, opts ...IPCScanOption) *LazyFrame {
	return scanIPCPaths(paths, fmt.Sprintf("%d files", len(paths)), "", ipcScanOptions(opts))
}

// ScanIPCGlob reads every IPC file a shell pattern matches as one frame, sorted by
// name, an element of ** matching any number of directories, as ScanIPCFiles reads
// them. New in 0.6.7.
func ScanIPCGlob(pattern string, opts ...IPCScanOption) *LazyFrame {
	paths, err := globFiles(pattern)
	if err != nil {
		return &LazyFrame{err: uerr.Wrap(err, uerr.KindValue, "scan_ipc", "bad glob pattern %q", pattern)}
	}
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindIO, "scan_ipc", "no files match %q", pattern)}
	}
	return scanIPCPaths(paths, pattern, "", ipcScanOptions(opts))
}

// IPCScanOption configures ScanIPC, ScanIPCFiles and ScanIPCGlob, and ScanIPCFrom
// and ScanIPCBytes. New in 0.6.7.
type IPCScanOption func(*ipcScan)

type ipcScan struct {
	hive       *bool
	hiveSchema *Schema
	noDates    bool
	pathColumn string
}

func ipcScanOptions(opts []IPCScanOption) ipcScan {
	var o ipcScan
	for _, f := range opts {
		f(&o)
	}
	return o
}

// WithIPCHivePartitioning reads the key=value directories of each file's path as
// columns, as Polars' hive_partitioning: on by default for a directory, off for a
// file, a list and a glob. New in 0.6.7.
func WithIPCHivePartitioning(b bool) IPCScanOption { return func(o *ipcScan) { o.hive = &b } }

// WithIPCHiveSchema gives the types of the partition keys it names, as Polars'
// hive_schema; the others are inferred. New in 0.6.7.
func WithIPCHiveSchema(s *Schema) IPCScanOption { return func(o *ipcScan) { o.hiveSchema = s } }

// WithIPCHiveDates reads a partition key of dates or of instants as a Date or a
// Datetime, as Polars' try_parse_hive_dates; on by default. New in 0.6.7.
func WithIPCHiveDates(b bool) IPCScanOption { return func(o *ipcScan) { o.noDates = !b } }

// WithIPCIncludeFilePaths adds a String column named name of each row's file path,
// last, as Polars' include_file_paths. New in 0.6.7.
func WithIPCIncludeFilePaths(name string) IPCScanOption {
	return func(o *ipcScan) { o.pathColumn = name }
}

// scanIPCPaths reads paths as one frame, described as desc, their partition keys read
// below root, a directory scanned, or from the whole of each where root is "", where
// hive partitioning is on (step 360).
func scanIPCPaths(paths []string, desc, root string, o ipcScan) *LazyFrame {
	opens := make([]recordOpener, len(paths))
	for i, p := range paths {
		opens[i] = func(context.Context) (array.RecordReader, error) { return openIPCFile("scan_ipc", p) }
	}
	return scanIPCOpened("scan_ipc", paths, opens, desc, root, o)
}

// recordOpener opens one file's record batches, for the query whose context it is.
type recordOpener = func(context.Context) (array.RecordReader, error)

// scanIPCOpened reads the files opens opens, named by names, as one frame, as
// scanIPCPaths reads paths (step 404).
func scanIPCOpened(op string, paths []string, opens []recordOpener, desc, root string, o ipcScan) *LazyFrame {
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, op, "no files to read")}
	}
	fs := make([]files.File, len(paths))
	for i, p := range paths {
		fs[i] = files.File{Path: p, Src: arrowsrc.NewContext(opens[i])}
	}
	with := files.Shared{Dates: !o.noDates, PathColumn: o.pathColumn}
	on := root != ""
	if o.hive != nil {
		on = *o.hive
	}
	if on {
		parts, err := hive.ParseFor(op, root, paths)
		if err != nil {
			return &LazyFrame{err: err}
		}
		with.Parts = parts
		if s := o.hiveSchema; s != nil {
			with.Types = make(map[string]DataType, s.Len())
			for _, f := range s.FieldSlice() {
				with.Types[f.Name] = f.Type
			}
		}
	}
	return scan(files.New(op, "ipc", desc, fs, with))
}

// IPCFile is one Arrow IPC file, read through ReadAt from wherever it lives, as a
// ParquetFile is: an object store, an HTTP server, an archive. New in 0.8.
type IPCFile struct {
	// Name identifies the file in errors and in Explain, and is its path where hive
	// partitioning reads keys from one.
	Name string

	// Open returns the file's bytes and their size, as ParquetFile's does: a fresh
	// reader each call, once for the schema and once for each execution, given the
	// query's context. If the reader is also an io.Closer, ursus closes it.
	Open func(ctx context.Context) (r io.ReaderAt, size int64, err error)
}

// ScanIPCFrom reads Arrow IPC files the caller opens, as one frame in the order given,
// under ScanIPCFiles' rules for several files (step 404, C4 of v0.8-scope.md). New in
// 0.8.
func ScanIPCFrom(files []IPCFile, opts ...IPCScanOption) *LazyFrame {
	return scanIPCFrom(files, "", ipcScanOptions(opts))
}

// scanIPCFrom is ScanIPCFrom of files whose hive keys are read below root, a directory
// ScanIPCFS read, or from the whole of each name where root is "".
func scanIPCFrom(files []IPCFile, root string, o ipcScan) *LazyFrame {
	names, err := fileNames("scan_ipc", "ipc file", len(files), func(i int) (string, bool) {
		return files[i].Name, files[i].Open != nil
	})
	if err != nil {
		return &LazyFrame{err: err}
	}
	opens := make([]recordOpener, len(files))
	for i, f := range files {
		opens[i] = func(ctx context.Context) (array.RecordReader, error) {
			ra, size, err := f.Open(ctx)
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindIO, "scan_ipc", "opening %s", names[i])
			}
			c, _ := ra.(io.Closer)
			return openIPC("scan_ipc", names[i], io.NewSectionReader(ra, 0, size), c)
		}
	}
	return scanIPCOpened("scan_ipc", names, opens, describe(names), root, o)
}

// ScanIPCBytes reads an Arrow IPC file from memory. New in 0.8.
func ScanIPCBytes(b []byte, name string, opts ...IPCScanOption) *LazyFrame {
	return ScanIPCFrom([]IPCFile{{Name: name, Open: func(context.Context) (io.ReaderAt, int64, error) {
		return bytes.NewReader(b), int64(len(b)), nil
	}}}, opts...)
}

// IPCStreamFile is one Arrow IPC stream, read from the start to its end by whatever
// reaches it, as a CSVFile is. New in 0.8.
type IPCStreamFile struct {
	// Name identifies the stream in errors and in Explain.
	Name string

	// Open returns a fresh reader of the stream each call, once for the schema and
	// once for each execution, given the query's context; ursus closes it.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// ScanIPCStreamFrom reads Arrow IPC streams the caller opens, as one frame in the
// order given, as the first one's columns, by name, as ScanIPCFiles reads files. New
// in 0.8.
func ScanIPCStreamFrom(streams []IPCStreamFile) *LazyFrame {
	names, err := fileNames("scan_ipc_stream", "ipc stream", len(streams), func(i int) (string, bool) {
		return streams[i].Name, streams[i].Open != nil
	})
	if err != nil {
		return &LazyFrame{err: err}
	}
	opens := make([]recordOpener, len(streams))
	for i, f := range streams {
		opens[i] = func(ctx context.Context) (array.RecordReader, error) {
			rc, err := f.Open(ctx)
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindIO, "scan_ipc_stream", "opening %s", names[i])
			}
			return openIPCStream(names[i], rc)
		}
	}
	return scanIPCOpened("scan_ipc_stream", names, opens, describe(names), "", ipcScan{})
}

// ScanIPCStreamBytes reads an Arrow IPC stream from memory. New in 0.8.
func ScanIPCStreamBytes(b []byte, name string) *LazyFrame {
	return ScanIPCStreamFrom([]IPCStreamFile{{Name: name, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}}})
}

// fileNames are n files' names, kind and its place where one has none, or an error
// for none at all or one with no Open.
func fileNames(op, kind string, n int, at func(int) (name string, opens bool)) ([]string, error) {
	if n == 0 {
		return nil, uerr.New(uerr.KindValue, op, "no files given")
	}
	names := make([]string, n)
	for i := range n {
		name, opens := at(i)
		if name == "" {
			name = kind + " " + strconv.Itoa(i+1)
		}
		if !opens {
			return nil, uerr.New(uerr.KindValue, op, "%s has no Open function", name)
		}
		names[i] = name
	}
	return names, nil
}

// describe is files' description in Explain: the first's name, and how many more.
func describe(names []string) string {
	if len(names) > 1 {
		return names[0] + " and " + strconv.Itoa(len(names)-1) + " more"
	}
	return names[0]
}

// ScanIPCStream reads an Arrow IPC stream, the format of Polars' write_ipc_stream and
// read_ipc_stream, as ScanIPC reads a file.
func ScanIPCStream(path string) *LazyFrame {
	return ScanArrow(func() (array.RecordReader, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "scan_ipc_stream", "opening %s", path)
		}
		return openIPCStream(path, f)
	})
}

// openIPCStream reads name's IPC stream from rc, which it closes when released.
func openIPCStream(name string, rc io.ReadCloser) (array.RecordReader, error) {
	r, err := ipc.NewReader(rc)
	if err != nil {
		rc.Close()
		return nil, uerr.Wrap(err, uerr.KindIO, "scan_ipc_stream", "reading %s as an Arrow IPC stream", name).
			Hint("an IPC file, Feather version 2, is ScanIPC's")
	}
	return &closingReader{RecordReader: r, close: rc.Close}, nil
}

// ReadIPCSchema is an Arrow IPC file's schema, read from its footer, as Polars'
// read_ipc_schema; no record is read.
func ReadIPCSchema(path string) (*Schema, error) {
	r, err := openIPCFile("read_ipc_schema", path)
	if err != nil {
		return nil, err
	}
	defer r.Release()
	return arrowin.Schema(r.Schema())
}

// ReadParquetSchema is a Parquet file's schema, read from its footer, as Polars'
// read_parquet_schema.
func ReadParquetSchema(ctx context.Context, path string) (*Schema, error) {
	return ScanParquet(path).CollectSchema(ctx)
}

// openIPCFile opens path's IPC file as a RecordReader over its batches, which closes
// the file when released.
func openIPCFile(op, path string) (*fileRecords, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, op, "opening %s", path)
	}
	return openIPC(op, path, f, f)
}

// openIPC reads name's IPC file from r, closing c, where it is not nil, when released
// or refused.
func openIPC(op, name string, r ipc.ReadAtSeeker, c io.Closer) (*fileRecords, error) {
	fr, err := ipc.NewFileReader(r)
	if err != nil {
		if c != nil {
			c.Close()
		}
		return nil, uerr.Wrap(err, uerr.KindIO, op, "reading %s as an Arrow IPC file", name).
			Hint("an IPC stream is ScanIPCStream's")
	}
	return &fileRecords{r: fr, f: c, refs: 1}, nil
}

// fileRecords walks an IPC file's record batches as a RecordReader.
type fileRecords struct {
	r    *ipc.FileReader
	f    io.Closer // nil for none
	i    int
	rec  arrow.RecordBatch
	err  error
	refs int64
}

func (f *fileRecords) Retain() { atomic.AddInt64(&f.refs, 1) }

func (f *fileRecords) Release() {
	if atomic.AddInt64(&f.refs, -1) != 0 {
		return
	}
	if f.rec != nil {
		f.rec.Release()
		f.rec = nil
	}
	f.r.Close()
	if f.f != nil {
		f.f.Close()
	}
}

func (f *fileRecords) Schema() *arrow.Schema { return f.r.Schema() }

func (f *fileRecords) Next() bool {
	if f.rec != nil {
		f.rec.Release()
		f.rec = nil
	}
	if f.err != nil || f.i >= f.r.NumRecords() {
		return false
	}
	// The reader owns what RecordBatch returns until its next call: this one's own.
	f.rec, f.err = f.r.RecordBatchAt(f.i)
	f.i++
	return f.err == nil
}

func (f *fileRecords) RecordBatch() arrow.RecordBatch { return f.rec }

// Record is RecordBatch, arrow-go's deprecated name, which RecordReader still has.
func (f *fileRecords) Record() arrow.RecordBatch { return f.rec }

func (f *fileRecords) Err() error { return f.err }

// closingReader is a RecordReader that closes its file when last released.
type closingReader struct {
	array.RecordReader
	close func() error
	refs  atomic.Int64
}

func (c *closingReader) Retain() {
	c.refs.Add(1)
	c.RecordReader.Retain()
}

func (c *closingReader) Release() {
	c.RecordReader.Release()
	if c.refs.Add(-1) < 0 {
		c.close()
	}
}

// IPCCompression is the codec an Arrow IPC file's buffers are written in.
type IPCCompression int

const (
	IPCUncompressed IPCCompression = iota // Polars' default
	IPCLZ4                                // LZ4 frames
	IPCZstd                               // Zstandard
)

// IPCWriteOption configures an IPC writer.
type IPCWriteOption func(*ipcWriteCfg)

type ipcWriteCfg struct {
	compression IPCCompression
}

// options are the IPC writer's options for a file of schema.
func (c ipcWriteCfg) options(schema *arrow.Schema) []ipc.Option {
	opts := []ipc.Option{ipc.WithSchema(schema)}
	switch c.compression {
	case IPCLZ4:
		opts = append(opts, ipc.WithLZ4())
	case IPCZstd:
		opts = append(opts, ipc.WithZstd())
	}
	return opts
}

// WithIPCCompression sets the codec, as Polars' write_ipc(compression=...): none by
// default, as Polars'.
func WithIPCCompression(c IPCCompression) IPCWriteOption {
	return func(o *ipcWriteCfg) { o.compression = c }
}

// IPCSinkOption is an option to WriteIPC, WriteIPCStream and SinkIPC: an
// IPCWriteOption or a CollectOption.
type IPCSinkOption interface{ applyIPCSink(*ipcSinkCfg) }

type ipcSinkCfg struct {
	write   ipcWriteCfg
	collect collectCfg
	part    partitionCfg
}

func (o IPCWriteOption) applyIPCSink(c *ipcSinkCfg) { o(&c.write) }
func (o CollectOption) applyIPCSink(c *ipcSinkCfg)  { o(&c.collect) }

func ipcSinkOptions(opts []IPCSinkOption) ipcSinkCfg {
	cfg := ipcSinkCfg{collect: baseCollectCfg()}
	for _, o := range opts {
		o.applyIPCSink(&cfg)
	}
	cfg.collect.finish()
	return cfg
}

// WriteIPC runs the query and writes the answer to w as an Arrow IPC file, Feather
// version 2, a record batch a batch, streaming, as Polars' write_ipc writes one. It
// does not close w.
func (lf *LazyFrame) WriteIPC(ctx context.Context, w io.Writer, opts ...IPCSinkOption) error {
	return lf.writeIPC(ctx, "write_ipc", w, false, opts)
}

// WriteIPCStream runs the query and writes the answer to w as an Arrow IPC stream, as
// Polars' write_ipc_stream.
func (lf *LazyFrame) WriteIPCStream(ctx context.Context, w io.Writer, opts ...IPCSinkOption) error {
	return lf.writeIPC(ctx, "write_ipc_stream", w, true, opts)
}

// SinkIPC runs the query and writes the answer to path as an Arrow IPC file,
// streaming, as Polars' sink_ipc: to a temporary name, renamed on success, since a
// file without its footer cannot be read.
//
// With WithPartitionBy or WithMaxRowsPerFile, path is a directory, and the result a
// hive dataset below it, of files named .ipc, as Polars names them.
func (lf *LazyFrame) SinkIPC(ctx context.Context, path string, opts ...IPCSinkOption) error {
	if cfg := ipcSinkOptions(opts); cfg.part.partitioned() {
		return lf.sinkPartitioned(ctx, "sink_ipc", path, cfg.collect, cfg.part, ".ipc",
			func(w io.Writer, s *dtype.Schema) (fileWriter, error) {
				schema, err := arrowout.Schema(s)
				if err != nil {
					return nil, err
				}
				fw, err := ipc.NewFileWriter(w, cfg.write.options(schema)...)
				if err != nil {
					return nil, uerr.Wrap(err, uerr.KindIO, "sink_ipc", "starting the file")
				}
				return ipcPart{w: fw, op: "sink_ipc", schema: schema}, nil
			})
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_ipc", "creating a temporary file for %s", path)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // a no-op on the success path, which ends with a rename
	}()
	if err := lf.WriteIPC(ctx, tmp, opts...); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_ipc", "closing %s", tmpName)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_ipc", "renaming to %s", path)
	}
	return nil
}

// writeRecords writes b to w, whose schema is schema, as one record, or, where a
// column's text passes what 32-bit offsets hold and its record would be the
// LargeString the schema does not say (step 368), as its halves, each so, down to a
// row, which w refuses.
func writeRecords(w interface{ Write(arrow.RecordBatch) error }, schema *arrow.Schema, b *data.Batch, op string) error {
	rec, err := arrowout.Record(b)
	if err != nil {
		return err
	}
	if n := b.Rows(); n > 1 && !rec.Schema().Equal(schema) {
		rec.Release()
		if err := writeRecords(w, schema, b.Slice(0, n/2), op); err != nil {
			return err
		}
		return writeRecords(w, schema, b.Slice(n/2, n-n/2), op)
	}
	err = w.Write(rec)
	rec.Release()
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, op, "writing a record batch")
	}
	return nil
}

func (lf *LazyFrame) writeIPC(ctx context.Context, op string, w io.Writer, stream bool, opts []IPCSinkOption) (err error) {
	var cfg ipcSinkCfg
	defer cfg.collect.end(ctx, op, &err)
	cfg = ipcSinkOptions(opts)
	if err := noPartition(op, cfg.part); err != nil {
		return err
	}
	defer cfg.collect.done()
	root, err := lf.compile(ctx, cfg.collect)
	if err != nil {
		return err
	}
	defer root.Close()

	schema, err := arrowout.Schema(exec.Schema(root))
	if err != nil {
		return err
	}
	wopts := cfg.write.options(schema)
	type recordWriter interface {
		Write(arrow.RecordBatch) error
		Close() error
	}
	var rw recordWriter
	if stream {
		rw = ipc.NewWriter(w, wopts...)
	} else if rw, err = ipc.NewFileWriter(w, wopts...); err != nil {
		return uerr.Wrap(err, uerr.KindIO, op, "starting the file")
	}
	for b, err := range exec.Batches(ctx, root) {
		if err != nil {
			rw.Close()
			return err
		}
		if err := writeRecords(rw, schema, b, op); err != nil {
			rw.Close()
			return err
		}
	}
	// Close writes a file's footer, and a stream's end: without them neither reads.
	if err := rw.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, op, "closing")
	}
	return nil
}
