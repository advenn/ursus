package ursus_test

// Step 405: every format read from an fs.FS, by a name, a glob, ** among it, and a
// directory, as the path readers read them (C4 of v0.8-scope.md).

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

//go:embed testdata/ipc/polars_types.arrow testdata/ipc/polars_types.arrows testdata/parquet/polars_categorical.parquet
var embedded embed.FS

// TestAnEmbeddedFileReadsAsItsPath: an IPC file, an IPC stream and a Parquet file
// embedded in the test binary read as the same files by their paths do.
func TestAnEmbeddedFileReadsAsItsPath(t *testing.T) {
	ctx := t.Context()
	for _, c := range []struct{ fs, path *ursus.LazyFrame }{
		{ursus.ScanIPCFS(embedded, "testdata/ipc/polars_types.arrow"), ursus.ScanIPC("testdata/ipc/polars_types.arrow")},
		{ursus.ScanIPCStreamFS(embedded, "testdata/ipc/polars_types.arrows"), ursus.ScanIPCStream("testdata/ipc/polars_types.arrows")},
		{ursus.ScanParquetFS(embedded, "testdata/parquet/polars_categorical.parquet"),
			ursus.ScanParquet("testdata/parquet/polars_categorical.parquet")},
	} {
		got, err := c.fs.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want, err := c.path.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}

// formats are the FS readers that read several files, and how to write one of each.
type fsFormat struct {
	ext   string
	scan  func(fs.FS, string) *ursus.LazyFrame
	write func(*ursus.LazyFrame, context.Context, io.Writer) error
	hive  bool
}

func fsFormats() []fsFormat {
	return []fsFormat{
		{"csv", func(f fs.FS, p string) *ursus.LazyFrame { return ursus.ScanCSVFS(f, p) },
			func(lf *ursus.LazyFrame, ctx context.Context, w io.Writer) error { return lf.WriteCSV(ctx, w) }, false},
		{"ndjson", func(f fs.FS, p string) *ursus.LazyFrame { return ursus.ScanNDJSONFS(f, p) },
			func(lf *ursus.LazyFrame, ctx context.Context, w io.Writer) error { return lf.WriteNDJSON(ctx, w) }, false},
		{"parquet", func(f fs.FS, p string) *ursus.LazyFrame { return ursus.ScanParquetFS(f, p) },
			func(lf *ursus.LazyFrame, ctx context.Context, w io.Writer) error { return lf.WriteParquet(ctx, w) }, true},
		{"arrow", func(f fs.FS, p string) *ursus.LazyFrame { return ursus.ScanIPCFS(f, p) },
			func(lf *ursus.LazyFrame, ctx context.Context, w io.Writer) error { return lf.WriteIPC(ctx, w) }, true},
	}
}

// closes is an fs.FS that counts the files it opens and their closes, and hides their
// ReadAt where noReadAt.
type closes struct {
	fs.FS
	noReadAt       bool
	opened, closed *atomic.Int64
	read           *atomic.Int64 // bytes read through Read, not ReadAt
}

func (c closes) Open(name string) (fs.File, error) {
	f, err := c.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err == nil && info.IsDir() {
		return f, nil // a directory as it is, for its ReadDir
	}
	c.opened.Add(1)
	if c.noReadAt {
		return closed{plain{f}, c.closed, c.read}, nil
	}
	return closedAt{closed{f, c.closed, c.read}}, nil
}

// plain is an fs.File of Read, Stat and Close alone.
type plain struct{ f fs.File }

func (p plain) Read(b []byte) (int, error) { return p.f.Read(b) }
func (p plain) Stat() (fs.FileInfo, error) { return p.f.Stat() }
func (p plain) Close() error               { return p.f.Close() }

// closed counts a file's close, and the bytes Read reads.
type closed struct {
	fs.File
	n, read *atomic.Int64
}

func (c closed) Close() error { c.n.Add(1); return c.File.Close() }

func (c closed) Read(b []byte) (int, error) {
	n, err := c.File.Read(b)
	c.read.Add(int64(n))
	return n, err
}

// closedAt is closed of a file that reads at an offset.
type closedAt struct{ closed }

func (c closedAt) ReadAt(b []byte, off int64) (int, error) {
	return c.File.(io.ReaderAt).ReadAt(b, off)
}

// TestEveryFormatReadsFromAnFS: three frames written in each format to an in-memory
// file system, under data/day=1, data/day=2 and data/day=2/more, read by a name, a
// glob, a ** glob and the directory, are the frames in their names' order, a
// Parquet and an IPC directory's with its day keys; every file opened is closed,
// one whose file does not read at an offset too, and a Parquet or IPC file that
// does is read at offsets, none of it whole.
func TestEveryFormatReadsFromAnFS(t *testing.T) {
	ctx := t.Context()
	frames := []*ursus.DataFrame{engineFrame(t, 300, 5), engineFrame(t, 200, 3), engineFrame(t, 100, 2)}
	c := ursus.Col
	for _, f := range fsFormats() {
		paths := []string{"data/day=1/a." + f.ext, "data/day=2/b." + f.ext, "data/day=2/more/c." + f.ext}
		mem := fstest.MapFS{}
		for i, p := range paths {
			var b bytes.Buffer
			if err := f.write(frames[i].Lazy(), ctx, &b); err != nil {
				t.Fatal(err)
			}
			mem[p] = &fstest.MapFile{Data: b.Bytes()}
		}
		mem["data/_SUCCESS"] = &fstest.MapFile{}
		cat := func(fs ...*ursus.DataFrame) *ursus.DataFrame {
			lfs := make([]*ursus.LazyFrame, len(fs))
			for i, d := range fs {
				lfs[i] = d.Lazy()
			}
			df, err := ursus.Concat(lfs).Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return df
		}
		for _, noReadAt := range []bool{false, true} {
			var opened, closedN, read atomic.Int64
			fsys := closes{mem, noReadAt, &opened, &closedN, &read}
			for _, r := range []struct {
				pattern string
				want    *ursus.DataFrame
			}{
				{paths[0], frames[0]},
				{"data/day=2/*." + f.ext, frames[1]},
				{"data/**/*." + f.ext, cat(frames...)},
				{"data", cat(frames...)},
			} {
				lf := f.scan(fsys, r.pattern)
				if f.hive && r.pattern == "data" {
					// The directory's day keys, after the files' columns.
					lf = lf.Select(c("k"), c("v"))
				}
				got, err := lf.Collect(ctx)
				if err != nil {
					t.Fatalf("%s %s: %v", f.ext, r.pattern, err)
				}
				ursustest.AssertFrameEqual(t, got, r.want, ursustest.IgnoreDTypes())
			}
			if f.hive {
				got, err := f.scan(fsys, "data").Select(c("day").Sum()).Collect(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if s := got.String(); !strings.Contains(s, " 900 ") {
					t.Errorf("%s: the day keys of 300 rows of day 1 and 300 of day 2 sum to 900: %s", f.ext, s)
				}
			}
			if opened.Load() == 0 || closedN.Load() != opened.Load() {
				t.Errorf("%s, ReadAt hidden %v: %d files opened, %d closed", f.ext, noReadAt, opened.Load(), closedN.Load())
			}
			// A Parquet or an IPC file that reads at an offset is read so, not whole.
			if f.hive && !noReadAt && read.Load() != 0 {
				t.Errorf("%s: %d bytes read whole of files that read at an offset", f.ext, read.Load())
			}
		}
	}
}

// TestAnFSPatternIsRefusedByName: no file system, a name an fs.FS cannot hold, a glob
// matching nothing, a file not there and a directory of two kinds of file are each
// refused, by name, with its kind.
func TestAnFSPatternIsRefusedByName(t *testing.T) {
	mem := fstest.MapFS{"d/a.csv": {Data: []byte("x\n1\n")}, "d/b.json": {Data: []byte("[]")}}
	for _, c := range []struct {
		name string
		lf   *ursus.LazyFrame
		kind error
		want string
	}{
		{"no file system", ursus.ScanCSVFS(nil, "a.csv"), ursus.ErrValue, "no file system"},
		{"a rooted name", ursus.ScanParquetFS(mem, "/d/a.parquet"), ursus.ErrValue, `"/d/a.parquet" is not a name`},
		{"a .. name", ursus.ScanJSONFS(mem, "../a.json"), ursus.ErrValue, `"../a.json" is not a name`},
		{"no match", ursus.ScanNDJSONFS(mem, "d/*.ndjson"), ursus.ErrIO, `no files match "d/*.ndjson"`},
		{"no file", ursus.ScanIPCFS(mem, "d/x.arrow"), ursus.ErrIO, "d/x.arrow"},
		{"two kinds", ursus.ScanCSVFS(mem, "d"), ursus.ErrValue, "d holds files of two extensions"},
	} {
		_, err := c.lf.Collect(t.Context())
		if !errors.Is(err, c.kind) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a %v saying %q", c.name, err, c.kind, c.want)
		}
	}
	if _, err := ursus.ScanIPCFS(mem, "d/x.arrow").Collect(t.Context()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file not there: %v, want fs.ErrNotExist through it", err)
	}
}
