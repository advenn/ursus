package ursus

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"

	"github.com/advenn/ursus/internal/source/ndjson"
	"github.com/advenn/ursus/internal/uerr"
)

// Lines read (step 492): Polars 2.x's scan_lines and read_lines.

// LinesOption configures a scan of lines.
type LinesOption func(*linesCfg)

type linesCfg struct{ name, paths string }

// WithLinesName names the column of lines, "line" unless set, as Polars' name=.
func WithLinesName(name string) LinesOption { return func(c *linesCfg) { c.name = name } }

// WithLinesIncludeFilePaths adds a String column named name of each row's file path,
// after the lines, as Polars' include_file_paths: the path as it was given, or a
// glob's as it found it; bytes', their name.
func WithLinesIncludeFilePaths(name string) LinesOption {
	return func(c *linesCfg) { c.paths = name }
}

func scanLines(files []ndjson.File, opts []LinesOption) *LazyFrame {
	cfg := linesCfg{name: "line"}
	for _, o := range opts {
		o(&cfg)
	}
	if len(files) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_lines", "no files given")}
	}
	desc := files[0].Name
	if len(files) > 1 {
		desc += " and " + strconv.Itoa(len(files)-1) + " more"
	}
	return scan(ndjson.New(desc, files, ndjson.Options{Lines: true, LineName: cfg.name, PathColumn: cfg.paths}))
}

// ScanLines reads a text file's lines as one String column, named "line", as Polars
// 2.x's scan_lines and read_lines: a row a line, without its newline, and without
// one "\r" before it, so a file of "\r\n" ends reads as one of "\n". An empty line is
// an empty row, a file's last line needs no newline, and an empty file has no rows.
// Nothing else is changed: a "\r" within a line is kept, and so is a UTF-8
// byte-order mark, the first line's first character, as Polars keeps both. Text that
// is not UTF-8 is refused, naming its line; a count, which reads no text, does not
// check it, as Polars' does not.
//
// The file is read as ScanNDJSON reads one, in blocks cut at their lines, a part a
// thread; a gzip, zlib or Zstandard file is decompressed, a query that wants only the
// first rows reads little more than them, and a directory is read as ScanLinesFiles of
// every file below it, sorted by path, a name beginning '.' or '_' passed over. A
// row index and n_rows are the frame's: WithRowIndex and Head. Polars' eager
// read_lines is ScanLines(path).Collect(ctx), as every format's is here. New in 0.8.
func ScanLines(path string, opts ...LinesOption) *LazyFrame {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		paths, err := datasetFiles("scan_lines", path)
		if err != nil {
			return &LazyFrame{err: err}
		}
		return ScanLinesFiles(paths, opts...)
	}
	return scanLines([]ndjson.File{ndjsonFile(path)}, opts)
}

// ScanLinesFiles reads several files' lines as one column, in the order given, each
// file's last line its own where it has no newline. New in 0.8.
func ScanLinesFiles(paths []string, opts ...LinesOption) *LazyFrame {
	files := make([]ndjson.File, len(paths))
	for i, p := range paths {
		files[i] = ndjsonFile(p)
	}
	return scanLines(files, opts)
}

// ScanLinesGlob reads the lines of every file matching a shell pattern, as
// ScanLinesFiles of the matches sorted by name, ** among them as ScanNDJSONGlob's.
// New in 0.8.
func ScanLinesGlob(pattern string, opts ...LinesOption) *LazyFrame {
	paths, err := globFiles(pattern)
	if err != nil {
		return &LazyFrame{err: uerr.Wrap(err, uerr.KindValue, "scan_lines", "bad glob pattern %q", pattern)}
	}
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindIO, "scan_lines", "no files match %q", pattern)}
	}
	sort.Strings(paths)
	return ScanLinesFiles(paths, opts...)
}

// ScanLinesBytes is ScanLines of text in memory, named name in Explain. New in 0.8.
func ScanLinesBytes(b []byte, name string, opts ...LinesOption) *LazyFrame {
	open := func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	return scanLines([]ndjson.File{{Name: name, Open: open}}, opts)
}

// LinesFile is one text file, read as a stream from wherever it lives by code that
// knows how to reach it, as NDJSONFile. New in 0.8.
type LinesFile struct {
	// Name identifies the file in errors and in Explain.
	Name string

	// Open returns the file's bytes from the start, a fresh stream each run. ctx is
	// the query's and outlives the stream, which ursus closes when it is done with
	// it.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// ScanLinesFrom reads the lines of files the caller opens, as one column in the order
// given, under ScanLinesFiles' rules: an object store's, say, each Open the body of a
// GET. New in 0.8.
func ScanLinesFrom(files []LinesFile, opts ...LinesOption) *LazyFrame {
	fs := make([]ndjson.File, len(files))
	for i, f := range files {
		fs[i] = ndjson.File{Name: f.Name, Open: f.Open}
		if fs[i].Name == "" {
			fs[i].Name = "text file " + strconv.Itoa(i+1)
		}
		if f.Open == nil {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_lines", "%s has no Open", fs[i].Name)}
		}
	}
	return scanLines(fs, opts)
}

// ScanLinesFS reads the lines of the files pattern names in fsys, as ScanLinesFiles
// reads them, or a directory's, under ScanCSVFS's rules for a pattern. New in 0.8.
func ScanLinesFS(fsys fs.FS, pattern string, opts ...LinesOption) *LazyFrame {
	names, _, err := fsFiles("scan_lines", fsys, pattern)
	if err != nil {
		return &LazyFrame{err: err}
	}
	files := make([]LinesFile, len(names))
	for i, n := range names {
		files[i] = LinesFile{Name: n, Open: fsOpener(fsys, n)}
	}
	return ScanLinesFrom(files, opts...)
}
