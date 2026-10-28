package ursus

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/advenn/ursus/internal/source/ndjson"
	"github.com/advenn/ursus/internal/uerr"
)

// Reading from an fs.FS (step 405, C4 of v0.8-scope.md): an embed.FS a program carries
// its data in, a test's fstest.MapFS, os.DirFS, a zip.Reader's archive. Each format's
// FS reader takes a pattern, as template.ParseFS does: a file's name in fsys; a
// directory's, read whole as the format's path reader reads one, every file below
// it but those whose names begin '.' or '_', Parquet's and IPC's hive keys read below
// it; or an fs.Glob pattern, an element of ** matching any number of directories as
// the globs do, the matches sorted. A file is opened from fsys each time the query
// is planned or run, as a path is.

// ScanCSVFS reads the CSV files pattern names in fsys as one frame, as ScanCSVFiles
// reads them. New in 0.8.
func ScanCSVFS(fsys fs.FS, pattern string, opts ...CSVOption) *LazyFrame {
	names, _, err := fsFiles("scan_csv", fsys, pattern)
	if err != nil {
		return &LazyFrame{err: err}
	}
	files := make([]CSVFile, len(names))
	for i, n := range names {
		files[i] = CSVFile{Name: n, Open: fsOpener(fsys, n)}
	}
	return ScanCSVFrom(files, opts...)
}

// ScanNDJSONFS reads the NDJSON files pattern names in fsys as one frame, as
// ScanNDJSONFiles reads them. New in 0.8.
func ScanNDJSONFS(fsys fs.FS, pattern string, opts ...NDJSONOption) *LazyFrame {
	names, _, err := fsFiles("scan_ndjson", fsys, pattern)
	if err != nil {
		return &LazyFrame{err: err}
	}
	files := make([]NDJSONFile, len(names))
	for i, n := range names {
		files[i] = NDJSONFile{Name: n, Open: fsOpener(fsys, n)}
	}
	return ScanNDJSONFrom(files, opts...)
}

// ScanJSONFS reads the JSON document named name in fsys, as ScanJSON reads one. New in
// 0.8.
func ScanJSONFS(fsys fs.FS, name string, opts ...NDJSONOption) *LazyFrame {
	if err := fsName("scan_json", fsys, name); err != nil {
		return &LazyFrame{err: err}
	}
	return scanJSON(name, ndjson.File{Name: name, Open: fsOpener(fsys, name)}, opts)
}

// ScanParquetFS reads the Parquet files pattern names in fsys as one frame, as
// ScanParquetFiles reads them, or a directory as ScanParquet reads one. A file fsys
// opens with ReadAt, as embed.FS's, os.DirFS's and fstest.MapFS's do, is read a
// column chunk at a time; another is read whole into memory first. New in 0.8.
func ScanParquetFS(fsys fs.FS, pattern string, opts ...ParquetOption) *LazyFrame {
	names, root, err := fsFiles("scan_parquet", fsys, pattern)
	if err != nil {
		return &LazyFrame{err: err}
	}
	files := make([]ParquetFile, len(names))
	for i, n := range names {
		files[i] = ParquetFile{Name: n, Open: fsReaderAt(fsys, n)}
	}
	return scanParquetFrom(files, root, opts)
}

// ScanIPCFS reads the Arrow IPC files pattern names in fsys as one frame, as
// ScanIPCFiles reads them, or a directory as ScanIPC reads one, each file through
// ReadAt as ScanParquetFS reads one. New in 0.8.
func ScanIPCFS(fsys fs.FS, pattern string, opts ...IPCScanOption) *LazyFrame {
	names, root, err := fsFiles("scan_ipc", fsys, pattern)
	if err != nil {
		return &LazyFrame{err: err}
	}
	files := make([]IPCFile, len(names))
	for i, n := range names {
		files[i] = IPCFile{Name: n, Open: fsReaderAt(fsys, n)}
	}
	return scanIPCFrom(files, root, ipcScanOptions(opts))
}

// ScanIPCStreamFS reads the Arrow IPC stream named name in fsys, as ScanIPCStream
// reads one. New in 0.8.
func ScanIPCStreamFS(fsys fs.FS, name string) *LazyFrame {
	if err := fsName("scan_ipc_stream", fsys, name); err != nil {
		return &LazyFrame{err: err}
	}
	return ScanIPCStreamFrom([]IPCStreamFile{{Name: name, Open: fsOpener(fsys, name)}})
}

// fsName refuses no fsys, and a name fs.ValidPath does not take.
func fsName(op string, fsys fs.FS, name string) error {
	if fsys == nil {
		return uerr.New(uerr.KindValue, op, "no file system to read %q from", name)
	}
	if !fs.ValidPath(name) {
		return uerr.New(uerr.KindValue, op, "%q is not a name in a file system", name).
			Hint("an fs.FS names its files unrooted, by slashes, with no . or .. element: data/x.csv")
	}
	return nil
}

// fsFiles is what pattern names in fsys: itself, a file; every file below it, sorted,
// a directory, and it the root its hive keys are read below; or what it matches, a
// glob, sorted.
func fsFiles(op string, fsys fs.FS, pattern string) (names []string, root string, err error) {
	if err := fsName(op, fsys, pattern); err != nil {
		return nil, "", err
	}
	if info, err := fs.Stat(fsys, pattern); err == nil && info.IsDir() {
		names, err := fsDataset(op, fsys, pattern)
		return names, pattern, err
	}
	if !strings.ContainsAny(pattern, "*?[\\") {
		return []string{pattern}, "", nil
	}
	names, err = fsGlob(fsys, pattern)
	if err != nil {
		return nil, "", uerr.Wrap(err, uerr.KindValue, op, "bad glob pattern %q", pattern)
	}
	if len(names) == 0 {
		return nil, "", uerr.New(uerr.KindIO, op, "no files match %q", pattern)
	}
	return names, "", nil
}

// fsGlob is globFiles in fsys: fs.Glob, and an element of ** any number of
// directories, none among them, files alone.
func fsGlob(fsys fs.FS, pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		names, err := fs.Glob(fsys, pattern)
		sort.Strings(names)
		return names, err
	}
	elems := strings.Split(pattern, "/")
	k := 0
	for k < len(elems) && !strings.ContainsAny(elems[k], "*?[\\") {
		k++
	}
	root, rest := strings.Join(elems[:k], "/"), elems[k:]
	if root == "" {
		root = "."
	}
	for _, e := range rest {
		if e != "**" {
			if _, err := path.Match(e, ""); err != nil {
				return nil, err
			}
		}
	}
	var out []string
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return fs.SkipAll // no such root: no match, as fs.Glob
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := p
		if root != "." {
			rel = strings.TrimPrefix(p, root+"/")
		}
		if matchElems(path.Match, rest, strings.Split(rel, "/")) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// fsDataset is datasetFiles in fsys: every file below dir, sorted, but those whose
// names begin '.' or '_', and refused where they are of two extensions.
func fsDataset(op string, fsys fs.FS, dir string) ([]string, error) {
	var names []string
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p != dir && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")):
			if d.IsDir() {
				return fs.SkipDir
			}
		case !d.IsDir():
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, op, "listing %s", dir)
	}
	if len(names) == 0 {
		return nil, uerr.New(uerr.KindIO, op, "no files in %s", dir)
	}
	sort.Strings(names)
	for _, n := range names[1:] {
		if path.Ext(n) != path.Ext(names[0]) {
			return nil, uerr.New(uerr.KindValue, op, "%s holds files of two extensions: %s and %s", dir, names[0], n).
				Hint("a glob, %s, names the files to read", path.Join(dir, "**", "*"+path.Ext(names[0])))
		}
	}
	return names, nil
}

// fsOpener opens name in fsys, for a reader of a stream.
func fsOpener(fsys fs.FS, name string) func(context.Context) (io.ReadCloser, error) {
	return func(context.Context) (io.ReadCloser, error) { return fsys.Open(name) }
}

// fsReaderAt opens name in fsys for ReadAt: the file itself, which the scan closes,
// where it reads at an offset; else its bytes, read whole.
func fsReaderAt(fsys fs.FS, name string) func(context.Context) (io.ReaderAt, int64, error) {
	return func(context.Context) (io.ReaderAt, int64, error) {
		f, err := fsys.Open(name)
		if err != nil {
			return nil, 0, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, err
		}
		if ra, ok := f.(io.ReaderAt); ok {
			return readerAtFile{ra, f}, info.Size(), nil
		}
		b, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, 0, err
		}
		return bytes.NewReader(b), int64(len(b)), nil
	}
}

// readerAtFile is an fs.File that reads at an offset, closed through its Close.
type readerAtFile struct {
	io.ReaderAt
	f fs.File
}

func (r readerAtFile) Close() error { return r.f.Close() }
