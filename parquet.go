package ursus

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	arrowpq "github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"

	"github.com/advenn/ursus/internal/exec"
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

func parquetOptions(opts []ParquetOption) parquet.Options {
	o := parquet.DefaultOptions()
	for _, f := range opts {
		f(&o)
	}
	return o
}

func fileOpener(path string) parquet.Opener {
	return func() (arrowpq.ReaderAtSeeker, io.Closer, error) {
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
// Flat schemas only. A file with a nested, repeated or encrypted column is refused
// with an error naming the column, rather than read with that column dropped.
func ScanParquet(path string, opts ...ParquetOption) *LazyFrame {
	return Scan(parquet.New([]parquet.Opener{fileOpener(path)}, path, parquetOptions(opts)))
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

// ScanParquetFiles reads several files as one frame, in the order given. The schema
// comes from the first.
func ScanParquetFiles(paths []string, opts ...ParquetOption) *LazyFrame {
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_parquet", "no files given")}
	}
	opens := make([]parquet.Opener, len(paths))
	for i, p := range paths {
		opens[i] = fileOpener(p)
	}
	desc := paths[0]
	if len(paths) > 1 {
		desc += " and " + strconv.Itoa(len(paths)-1) + " more"
	}
	return Scan(parquet.New(opens, desc, parquetOptions(opts)))
}

// ScanParquetBytes reads a Parquet file from memory.
func ScanParquetBytes(b []byte, name string, opts ...ParquetOption) *LazyFrame {
	open := func() (arrowpq.ReaderAtSeeker, io.Closer, error) {
		return bytes.NewReader(b), nil, nil
	}
	return Scan(parquet.New([]parquet.Opener{open}, name, parquetOptions(opts)))
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
func (lf *LazyFrame) WriteParquet(ctx context.Context, w io.Writer, opts ...ParquetSinkOption) error {
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
