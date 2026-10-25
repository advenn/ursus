package ursus

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"

	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/arrowout"
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

// IPCScanOption configures ScanIPC, ScanIPCFiles and ScanIPCGlob. New in 0.6.7.
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
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_ipc", "no files to read")}
	}
	fs := make([]files.File, len(paths))
	for i, p := range paths {
		fs[i] = files.File{Path: p, Src: arrowsrc.New(func() (array.RecordReader, error) {
			return openIPCFile("scan_ipc", p)
		})}
	}
	with := files.Shared{Dates: !o.noDates, PathColumn: o.pathColumn}
	on := root != ""
	if o.hive != nil {
		on = *o.hive
	}
	if on {
		parts, err := hive.ParseFor("scan_ipc", root, paths)
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
	return scan(files.New("scan_ipc", "ipc", desc, fs, with))
}

// ScanIPCStream reads an Arrow IPC stream, the format of Polars' write_ipc_stream and
// read_ipc_stream, as ScanIPC reads a file.
func ScanIPCStream(path string) *LazyFrame {
	return ScanArrow(func() (array.RecordReader, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "scan_ipc_stream", "opening %s", path)
		}
		r, err := ipc.NewReader(f)
		if err != nil {
			f.Close()
			return nil, uerr.Wrap(err, uerr.KindIO, "scan_ipc_stream", "reading %s as an Arrow IPC stream", path).
				Hint("an IPC file, Feather version 2, is ScanIPC's")
		}
		return &closingReader{RecordReader: r, close: f.Close}, nil
	})
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
	r, err := ipc.NewFileReader(f)
	if err != nil {
		f.Close()
		return nil, uerr.Wrap(err, uerr.KindIO, op, "reading %s as an Arrow IPC file", path).
			Hint("an IPC stream is ScanIPCStream's")
	}
	return &fileRecords{r: r, f: f, refs: 1}, nil
}

// fileRecords walks an IPC file's record batches as a RecordReader.
type fileRecords struct {
	r    *ipc.FileReader
	f    *os.File
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
	f.f.Close()
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
func (lf *LazyFrame) SinkIPC(ctx context.Context, path string, opts ...IPCSinkOption) error {
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

func (lf *LazyFrame) writeIPC(ctx context.Context, op string, w io.Writer, stream bool, opts []IPCSinkOption) (err error) {
	defer uerr.Catch(&err, op)
	cfg := ipcSinkOptions(opts)
	defer cfg.collect.report()
	root, err := lf.compile(ctx, cfg.collect)
	if err != nil {
		return err
	}
	defer root.Close()

	schema, err := arrowout.Schema(exec.Schema(root))
	if err != nil {
		return err
	}
	wopts := []ipc.Option{ipc.WithSchema(schema)}
	switch cfg.write.compression {
	case IPCLZ4:
		wopts = append(wopts, ipc.WithLZ4())
	case IPCZstd:
		wopts = append(wopts, ipc.WithZstd())
	}
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
		rec, err := arrowout.Record(b)
		if err != nil {
			rw.Close()
			return err
		}
		err = rw.Write(rec)
		rec.Release()
		if err != nil {
			rw.Close()
			return uerr.Wrap(err, uerr.KindIO, op, "writing a record batch")
		}
	}
	// Close writes a file's footer, and a stream's end: without them neither reads.
	if err := rw.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, op, "closing")
	}
	return nil
}
