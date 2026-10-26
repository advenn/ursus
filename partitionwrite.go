package ursus

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/source/hive"
	"github.com/advenn/ursus/internal/uerr"
)

// Partitioned writes (step 367, L2 of v0.6.8-scope.md): Polars' write_parquet(path,
// partition_by=...) and sink_parquet(pl.PartitionBy(path, key=..., include_key=...,
// max_rows_per_file=...)), and sink_csv's, sink_ipc's and sink_ndjson's.

// PartitionOption makes SinkParquet, SinkCSV, SinkIPC or SinkNDJSON write a hive
// dataset to its path, a directory, as Polars' PartitionBy does. New in 0.6.8.
type PartitionOption func(*partitionCfg)

type partitionCfg struct {
	keys    []string
	dropKey bool
	maxRows int
}

func (o PartitionOption) applyParquetSink(c *parquetSinkCfg) { o(&c.part) }
func (o PartitionOption) applyCSVSink(c *csvSinkCfg)         { o(&c.part) }
func (o PartitionOption) applyIPCSink(c *ipcSinkCfg)         { o(&c.part) }
func (o PartitionOption) applyNDJSONSink(c *ndjsonSinkCfg)   { o(&c.part) }

// WithPartitionBy writes a hive dataset partitioned by the columns keys: each key's
// rows to the directory k=v below the path, a directory a key, in the keys' order, in
// files numbered from 00000000, as Polars writes one. A value is percent-encoded where
// it holds `/`, `=`, a space, `%`, `:`, a control byte or a byte past ASCII, a null is
// __HIVE_DEFAULT_PARTITION__, and an instant is its wall clock,
// 2024-01-02 03:04:05.123456, with its offset where it has a zone; ScanParquet and its
// kin read the dataset back. New in 0.6.8.
func WithPartitionBy(keys ...string) PartitionOption {
	keys = append([]string(nil), keys...)
	return func(c *partitionCfg) { c.keys = keys }
}

// WithPartitionIncludeKey says whether the key columns are written in each file, as
// well as in its path. Default true, as Polars' include_key. New in 0.6.8.
func WithPartitionIncludeKey(include bool) PartitionOption {
	return func(c *partitionCfg) { c.dropKey = !include }
}

// WithMaxRowsPerFile ends a key's file at n rows and begins its next, numbered after
// it, as Polars' max_rows_per_file. Default none. New in 0.6.8.
func WithMaxRowsPerFile(n int) PartitionOption {
	return func(c *partitionCfg) { c.maxRows = n }
}

// fileWriter writes one file's batches; Close ends the file, its footer written.
type fileWriter interface {
	WriteBatch(*data.Batch) error
	Close() error
}

// maxOpenPartitions is how many files a partitioned write holds open at once: past
// it, the least recently written closes, and a later row of its key begins the key's
// next file, so a dataset of many keys does not hold a file a key.
const maxOpenPartitions = 64

// sinkPartitioned runs the query and writes its rows to a hive dataset below dir, by
// pc's keys, each file of ext opened by open.
func (lf *LazyFrame) sinkPartitioned(ctx context.Context, op, dir string, collect collectCfg,
	pc partitionCfg, ext string, open func(io.Writer, *dtype.Schema) (fileWriter, error)) (err error) {
	defer uerr.Catch(&err, op)
	defer collect.report()
	if pc.maxRows < 0 {
		return uerr.New(uerr.KindValue, op, "%d rows a file", pc.maxRows).Hint("give a positive number, or none")
	}
	root, err := lf.compile(ctx, collect)
	if err != nil {
		return err
	}
	defer root.Close()
	schema := exec.Schema(root)

	p := &partitioner{op: op, dir: dir, ext: ext, open: open, maxRows: pc.maxRows,
		parts: map[string]*hivePart{}}
	isKey := map[int]bool{}
	for _, k := range pc.keys {
		i := schema.IndexOf(k)
		if i < 0 {
			return uerr.UnknownColumn(op, k, schema.Names())
		}
		if isKey[i] {
			return uerr.New(uerr.KindValue, op, "the partition key %q is named twice", k)
		}
		if t := schema.Field(i).Type; t.IsNested() || t.ID() == dtype.TypeBinary {
			return uerr.New(uerr.KindType, op, "a %s column cannot be a partition key", t).
				Hint("a key's value is a directory's name: partition by a column of numbers, text, Booleans or instants")
		}
		isKey[i] = true
		p.keys = append(p.keys, i)
	}
	var fields []dtype.Field
	for i, f := range schema.All() {
		if !pc.dropKey || !isKey[i] {
			p.cols = append(p.cols, i)
			fields = append(fields, f)
		}
	}
	if len(fields) == 0 {
		return uerr.New(uerr.KindValue, op, "every column is a partition key, and no column is left to write").
			Hint("keep the keys in the files with WithPartitionIncludeKey(true)")
	}
	if p.fileSchema, err = dtype.NewSchema(fields...); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return uerr.Wrap(err, uerr.KindIO, op, "creating %s", dir)
	}
	defer func() {
		if err != nil {
			p.abort()
		}
	}()
	for b, err := range exec.Batches(ctx, root) {
		if err != nil {
			return err
		}
		if err := p.write(b); err != nil {
			return err
		}
	}
	return p.closeAll()
}

type partitioner struct {
	op, dir, ext string
	open         func(io.Writer, *dtype.Schema) (fileWriter, error)
	maxRows      int
	keys         []int // the key columns, by index in the query's schema
	cols         []int // the columns written
	fileSchema   *dtype.Schema
	parts        map[string]*hivePart // by encoded key
	live         []*hivePart          // the partitions with a file open
	tick         int64
}

// partition is one key's directory and its file open, if one is.
type hivePart struct {
	dir  string
	next int // the next file's number
	f    *openFile
	used int64 // the tick it was last written at
}

type openFile struct {
	tmp   *os.File
	final string
	w     fileWriter
	rows  int
}

// write routes b's rows to their keys' files, each key's rows in their order.
func (p *partitioner) write(b *data.Batch) error {
	keyCols := make([]*data.Column, len(p.keys))
	for j, i := range p.keys {
		keyCols[j] = b.Column(i)
	}
	enc, err := kernel.NewGroupKeyEncoder(p.op, keyCols)
	if err != nil {
		return err
	}
	var (
		order []*hivePart
		rows  = map[*hivePart][]int32{}
		texts [][]string // the keys' text, read once a batch meets a new key
	)
	for r := range b.Rows() {
		k := enc.Encode(r)
		part := p.parts[string(k)]
		if part == nil {
			if texts == nil {
				if texts, err = keyTexts(keyCols); err != nil {
					return err
				}
			}
			segs := make([]string, len(p.keys))
			for j, i := range p.keys {
				segs[j] = hive.Escape(b.Schema().Field(i).Name) + "=" + texts[j][r]
			}
			part = &hivePart{dir: filepath.Join(append([]string{p.dir}, segs...)...)}
			p.parts[string(k)] = part
		}
		if _, ok := rows[part]; !ok {
			order = append(order, part)
		}
		rows[part] = append(rows[part], int32(r))
	}

	cols := make([]*data.Column, len(p.cols))
	for j, i := range p.cols {
		cols[j] = b.Column(i)
	}
	src := data.NewBatchRows(p.fileSchema, cols, b.Rows())
	for _, part := range order {
		sel := rows[part]
		for len(sel) > 0 {
			if part.f == nil {
				if err := p.begin(part); err != nil {
					return err
				}
			}
			n := len(sel)
			if p.maxRows > 0 {
				n = min(n, p.maxRows-part.f.rows)
			}
			out := src
			if n != src.Rows() {
				if out, err = takeRows(p.fileSchema, src, sel[:n]); err != nil {
					return err
				}
			}
			if err := part.f.w.WriteBatch(out); err != nil {
				return uerr.Annotate(err, p.op, "writing "+part.f.final)
			}
			p.tick++
			part.f.rows, part.used, sel = part.f.rows+n, p.tick, sel[n:]
			if p.maxRows > 0 && part.f.rows == p.maxRows {
				if err := p.end(part); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// takeRows is b's rows sel, in their order.
func takeRows(s *dtype.Schema, b *data.Batch, sel []int32) (*data.Batch, error) {
	cols := make([]*data.Column, b.NumCols())
	for j, c := range b.Columns() {
		t, err := kernel.Take(c, sel)
		if err != nil {
			return nil, err
		}
		cols[j] = t
	}
	return data.NewBatchRows(s, cols, len(sel)), nil
}

// begin opens part's next file, to a temporary name, closing the least recently
// written past maxOpenPartitions.
func (p *partitioner) begin(part *hivePart) error {
	if len(p.live) >= maxOpenPartitions {
		lru := p.live[0]
		for _, o := range p.live[1:] {
			if o.used < lru.used {
				lru = o
			}
		}
		if err := p.end(lru); err != nil {
			return err
		}
	}
	if part.next == 0 {
		if err := os.MkdirAll(part.dir, 0o777); err != nil {
			return uerr.Wrap(err, uerr.KindIO, p.op, "creating %s", part.dir)
		}
	}
	name := fmt.Sprintf("%08d%s", part.next, p.ext)
	part.next++
	tmp, err := os.CreateTemp(part.dir, "."+name+".*")
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, p.op, "creating a temporary file in %s", part.dir)
	}
	w, err := p.open(tmp, p.fileSchema)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	part.f = &openFile{tmp: tmp, final: filepath.Join(part.dir, name), w: w}
	p.live = append(p.live, part)
	return nil
}

// end closes part's file and gives it its name.
func (p *partitioner) end(part *hivePart) error {
	f := part.f
	part.f = nil
	for i, o := range p.live {
		if o == part {
			p.live = append(p.live[:i], p.live[i+1:]...)
			break
		}
	}
	err := f.w.Close()
	if cerr := f.tmp.Close(); err == nil && cerr != nil {
		err = uerr.Wrap(cerr, uerr.KindIO, p.op, "closing %s", f.tmp.Name())
	}
	if err == nil {
		if rerr := os.Rename(f.tmp.Name(), f.final); rerr != nil {
			err = uerr.Wrap(rerr, uerr.KindIO, p.op, "renaming to %s", f.final)
		}
	}
	if err != nil {
		os.Remove(f.tmp.Name())
	}
	return err
}

func (p *partitioner) closeAll() error {
	var errs []error
	for len(p.live) > 0 {
		errs = append(errs, p.end(p.live[0]))
	}
	return errors.Join(errs...)
}

// abort drops every file still open, as a failed write leaves none half written; the
// files already ended stay, as Polars leaves them.
func (p *partitioner) abort() {
	for _, part := range p.live {
		part.f.w.Close()
		part.f.tmp.Close()
		os.Remove(part.f.tmp.Name())
		part.f = nil
	}
	p.live = nil
}

// keyTexts is each key column's values as a path segment spells them: escaped, a
// null __HIVE_DEFAULT_PARTITION__, a date 2024-01-02, an instant its wall clock with
// its unit's digits and, with a zone, its offset, as Polars writes them; a time of day
// with its fraction, which Polars drops; anything else its text, as a cast gives it.
func keyTexts(cols []*data.Column) ([][]string, error) {
	out := make([][]string, len(cols))
	for j, c := range cols {
		vals := make([]string, c.Len())
		var at func(int) string
		switch t := c.DType(); t.ID() {
		case dtype.TypeDate, dtype.TypeDatetime, dtype.TypeTime:
			read, err := cellReader(c)
			if err != nil {
				return nil, err
			}
			layout := instantLayout(t)
			at = func(i int) string {
				switch v := read(i).(type) {
				case time.Time:
					return v.Format(layout)
				case time.Duration:
					return timeOfDay(v)
				}
				return ""
			}
		default:
			text, err := kernel.Cast(c.Name(), dtype.String, true, c)
			if err != nil {
				return nil, err
			}
			acc := text.Strings()
			at = func(i int) string { return acc.Get(i) }
		}
		for i := range vals {
			if !c.IsValid(i) {
				vals[i] = hive.DefaultPartition
				continue
			}
			vals[i] = hive.Escape(at(i))
		}
		out[j] = vals
	}
	return out, nil
}

// instantLayout is the layout of a date's or an instant's path text.
func instantLayout(t dtype.DataType) string {
	if t.ID() == dtype.TypeDate {
		return "2006-01-02"
	}
	layout := "2006-01-02 15:04:05"
	switch t.TimeUnit() {
	case dtype.Milli:
		layout += ".000"
	case dtype.Micro:
		layout += ".000000"
	case dtype.Nano:
		layout += ".000000000"
	}
	if t.TimeZone() != "" {
		layout += "-07:00"
	}
	return layout
}

// The file writers of IPC and NDJSON; Parquet's and CSV's writers are file writers.

// ipcPart writes an Arrow IPC file a record batch a batch.
type ipcPart struct {
	w      *ipc.FileWriter
	op     string
	schema *arrow.Schema
}

func (f ipcPart) WriteBatch(b *data.Batch) error { return writeRecords(f.w, f.schema, b, f.op) }

func (f ipcPart) Close() error {
	if err := f.w.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, f.op, "closing")
	}
	return nil
}

// ndjsonPart writes a batch's rows as JSON lines, as WriteNDJSON does, in the caller's
// goroutine.
type ndjsonPart struct {
	bw   *bufio.Writer
	keys [][]byte
}

func newNDJSONFile(w io.Writer, s *dtype.Schema) (fileWriter, error) {
	keys := make([][]byte, s.Len())
	for j, f := range s.All() {
		if err := jsonWritable(f.Name, f.Type); err != nil {
			return nil, uerr.Wrap(err, uerr.KindType, "sink_ndjson", "column %q", f.Name)
		}
		keys[j] = append(appendJSONString(nil, f.Name), ':')
	}
	return &ndjsonPart{bw: bufio.NewWriterSize(w, 1<<16), keys: keys}, nil
}

func (f *ndjsonPart) WriteBatch(b *data.Batch) error {
	buf, err := encodeJSONRows(b, f.keys, '\n', false)
	if err != nil {
		return err
	}
	if _, err := f.bw.Write(buf); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_ndjson", "writing")
	}
	return nil
}

func (f *ndjsonPart) Close() error {
	if err := f.bw.Flush(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_ndjson", "writing")
	}
	return nil
}

// partitioned says whether a sink writes a dataset: by keys, or into files of at most
// so many rows, numbered in its directory.
func (pc partitionCfg) partitioned() bool { return len(pc.keys) > 0 || pc.maxRows != 0 }

// noPartition refuses a partition option given to a writer of one stream, which has
// no directory to write a dataset to.
func noPartition(op string, pc partitionCfg) error {
	if !pc.partitioned() {
		return nil
	}
	return uerr.New(uerr.KindValue, op, "a partitioned write goes to a directory, not to a stream").
		Hint("give WithPartitionBy or WithMaxRowsPerFile to SinkParquet, SinkCSV, SinkIPC or SinkNDJSON, whose path is the dataset's directory")
}
