package ursus_test

// The source seam: Parquet and CSV read through files the caller opens.
//
// ursus has no object-store client of its own (v0.3-scope.md §3 item 4 defers them
// to 0.4); ScanParquetFrom and ScanCSVFrom are how a caller reaches one. What makes
// that viable is pinned here: a Parquet scan reads the footer and the column chunks
// it needs and nothing else, the query's context reaches every Open, every reader
// is closed however the query ends, and a failure is an I/O error naming the file.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/advenn/ursus"
)

// recording counts what is read through it and whether it is closed, and can fail
// once failAfter bytes have been read.
type recording struct {
	r         *bytes.Reader
	read      *atomic.Int64
	closed    *atomic.Int64
	failAfter int64
	ctx       context.Context
}

func (r *recording) ReadAt(p []byte, off int64) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	if r.failAfter > 0 && r.read.Load()+int64(len(p)) > r.failAfter {
		return 0, errors.New("the connection was reset")
	}
	n, err := r.r.ReadAt(p, off)
	r.read.Add(int64(n))
	return n, err
}

func (r *recording) Close() error {
	r.closed.Add(1)
	return nil
}

// tally is what every reader one file handed out has done.
type tally struct{ opens, closed, read atomic.Int64 }

// parquetFile serves b as a ParquetFile, counting into t. failAfter and size, when
// non-zero, make its readers fail after that many bytes or claim that size.
func parquetFile(name string, b []byte, t *tally, failAfter, size int64) ursus.ParquetFile {
	return ursus.ParquetFile{Name: name, Open: func(ctx context.Context) (io.ReaderAt, int64, error) {
		t.opens.Add(1)
		n := int64(len(b))
		if size != 0 {
			n = size
		}
		return &recording{r: bytes.NewReader(b), read: &t.read, closed: &t.closed,
			failAfter: failAfter, ctx: ctx}, n, nil
	}}
}

// wideParquet is 4000 rows of eight Int64 columns that do not compress, in row
// groups of 1000, and its bytes.
func wideParquet(t *testing.T) (*ursus.LazyFrame, []byte) {
	t.Helper()
	cols := make([]*ursus.Column, 8)
	for c := range cols {
		v := make([]int64, 4000)
		for i := range v {
			v[i] = int64(i)*2654435761%1_000_000_007 + int64(c)
		}
		cols[c] = ursus.Values(fmt.Sprintf("c%d", c), v)
	}
	lf := ursus.Frame(cols...)
	var b bytes.Buffer
	if err := lf.WriteParquet(t.Context(), &b, ursus.WithRowGroupRows(1000)); err != nil {
		t.Fatal(err)
	}
	return lf, b.Bytes()
}

func TestScanParquetFrom(t *testing.T) {
	c := ursus.Col
	lf, b := wideParquet(t)
	footer := int64(binary.LittleEndian.Uint32(b[len(b)-8:])) + 8

	t.Run("answers as ScanParquetBytes", func(t *testing.T) {
		var n tally
		got, err := ursus.ScanParquetFrom([]ursus.ParquetFile{parquetFile("w.parquet", b, &n, 0, 0)}).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want, err := lf.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if d := framesDiffer(t, got, want); d != "" {
			t.Error(d)
		}
	})

	t.Run("one column of eight reads its chunks and the footers", func(t *testing.T) {
		var n tally
		if _, err := ursus.ScanParquetFrom([]ursus.ParquetFile{parquetFile("w.parquet", b, &n, 0, 0)}).
			Select(c("c3")).Collect(t.Context()); err != nil {
			t.Fatal(err)
		}
		if read := n.read.Load(); read > int64(len(b))/4 {
			t.Errorf("read %d of %d bytes for one column of eight", read, len(b))
		}
	})

	t.Run("a filter the statistics prune reads only the footers", func(t *testing.T) {
		var n tally
		df, err := ursus.ScanParquetFrom([]ursus.ParquetFile{parquetFile("w.parquet", b, &n, 0, 0)}).
			Filter(c("c0").Gt(int64(2_000_000_000))).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 0 {
			t.Fatalf("%d rows past every maximum", df.Height())
		}
		if read, most := n.read.Load(), n.opens.Load()*footer; read > most {
			t.Errorf("read %d bytes over %d opens; the footers are %d", read, n.opens.Load(), most)
		}
	})

	t.Run("Open gets the query's context", func(t *testing.T) {
		type key struct{}
		var seen, opens atomic.Int64
		f := ursus.ParquetFile{Name: "w.parquet", Open: func(ctx context.Context) (io.ReaderAt, int64, error) {
			opens.Add(1)
			if ctx.Value(key{}) == "the query" {
				seen.Add(1)
			}
			return bytes.NewReader(b), int64(len(b)), nil
		}}
		ctx := context.WithValue(t.Context(), key{}, "the query")
		if _, err := ursus.ScanParquetFrom([]ursus.ParquetFile{f}).Collect(ctx); err != nil {
			t.Fatal(err)
		}
		if seen.Load() != opens.Load() || opens.Load() == 0 {
			t.Errorf("%d of %d opens saw the query's context", seen.Load(), opens.Load())
		}
	})

	t.Run("cancelling the query reaches a reader that kept the context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var n tally
		f := parquetFile("w.parquet", b, &n, 0, 0)
		open := f.Open
		f.Open = func(ctx context.Context) (io.ReaderAt, int64, error) {
			if n.opens.Load() == 1 {
				cancel() // planned; cancelled as it executes
			}
			return open(ctx)
		}
		_, err := ursus.ScanParquetFrom([]ursus.ParquetFile{f}).Collect(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
		if n.opens.Load() != n.closed.Load() {
			t.Errorf("%d opened, %d closed", n.opens.Load(), n.closed.Load())
		}
	})

	t.Run("every reader is closed", func(t *testing.T) {
		var n tally
		lf := ursus.ScanParquetFrom([]ursus.ParquetFile{parquetFile("w.parquet", b, &n, 0, 0)})
		if _, err := lf.Collect(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, err := range lf.CollectBatches(t.Context(), ursus.WithBatchSize(100)) {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if n.opens.Load() < 3 || n.opens.Load() != n.closed.Load() {
			t.Errorf("%d opened, %d closed", n.opens.Load(), n.closed.Load())
		}
	})

	t.Run("a read that fails mid-scan is an I/O error naming the file, and closes", func(t *testing.T) {
		var n tally
		_, err := ursus.ScanParquetFrom([]ursus.ParquetFile{
			parquetFile("s3://bucket/w.parquet", b, &n, int64(len(b))/2, 0)}).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrIO, "s3://bucket/w.parquet"); d != "" {
			t.Error(d)
		}
		if n.opens.Load() != n.closed.Load() {
			t.Errorf("%d opened, %d closed", n.opens.Load(), n.closed.Load())
		}
	})

	t.Run("an Open that fails is an I/O error naming the file and the cause", func(t *testing.T) {
		f := ursus.ParquetFile{Name: "s3://bucket/gone.parquet", Open: func(context.Context) (io.ReaderAt, int64, error) {
			return nil, 0, errors.New("NoSuchKey")
		}}
		_, err := ursus.ScanParquetFrom([]ursus.ParquetFile{f}).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrIO, "s3://bucket/gone.parquet", "NoSuchKey"); d != "" {
			t.Error(d)
		}
	})

	t.Run("a size past the end is an I/O error", func(t *testing.T) {
		var n tally
		_, err := ursus.ScanParquetFrom([]ursus.ParquetFile{
			parquetFile("w.parquet", b, &n, 0, int64(len(b))+100)}).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrIO, "w.parquet"); d != "" {
			t.Error(d)
		}
	})

	t.Run("several files answer as ScanParquetFiles", func(t *testing.T) {
		dir := t.TempDir()
		var files []ursus.ParquetFile
		var paths []string
		var n tally
		for i := range 3 {
			var buf bytes.Buffer
			part := ursus.Frame(ursus.Values("a", []int64{int64(i), int64(10 * i)}),
				ursus.Values("s", []string{fmt.Sprint("x", i), ""}))
			if err := part.WriteParquet(t.Context(), &buf); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, fmt.Sprintf("part-%d.parquet", i))
			if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, p)
			files = append(files, parquetFile(p, buf.Bytes(), &n, 0, 0))
		}
		got, err := ursus.ScanParquetFrom(files).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want, err := ursus.ScanParquetFiles(paths).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if d := framesDiffer(t, got, want); d != "" {
			t.Error(d)
		}
		if n.opens.Load() != n.closed.Load() {
			t.Errorf("%d opened, %d closed", n.opens.Load(), n.closed.Load())
		}
	})

	t.Run("files that differ are refused naming both", func(t *testing.T) {
		var one, two bytes.Buffer
		if err := ursus.Frame(ursus.Values("a", []int64{1})).WriteParquet(t.Context(), &one); err != nil {
			t.Fatal(err)
		}
		if err := ursus.Frame(ursus.Values("b", []int64{1})).WriteParquet(t.Context(), &two); err != nil {
			t.Fatal(err)
		}
		var n tally
		_, err := ursus.ScanParquetFrom([]ursus.ParquetFile{
			parquetFile("first.parquet", one.Bytes(), &n, 0, 0),
			parquetFile("second.parquet", two.Bytes(), &n, 0, 0)}).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrSchema, "first.parquet", "second.parquet"); d != "" {
			t.Error(d)
		}
	})

	t.Run("no files, or a file with no Open, is refused", func(t *testing.T) {
		_, err := ursus.ScanParquetFrom(nil).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrValue, "no files"); d != "" {
			t.Error(d)
		}
		_, err = ursus.ScanParquetFrom([]ursus.ParquetFile{{Name: "x.parquet"}}).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrValue, "x.parquet", "Open"); d != "" {
			t.Error(d)
		}
	})

	t.Run("Explain names the files", func(t *testing.T) {
		var n tally
		plan, err := ursus.ScanParquetFrom([]ursus.ParquetFile{parquetFile("s3://bucket/w.parquet", b, &n, 0, 0),
			parquetFile("s3://bucket/v.parquet", b, &n, 0, 0)}).Explain(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, "s3://bucket/w.parquet and 1 more") {
			t.Errorf("Explain does not name the files:\n%s", plan)
		}
	})
}

// csvStream counts opens and closes, and fails after failAfter bytes when non-zero.
type csvStream struct {
	io.Reader
	closed *atomic.Int64
}

func (s *csvStream) Close() error {
	s.closed.Add(1)
	return nil
}

// failingReader returns the first n bytes of b, then an error.
type failingReader struct {
	b []byte
	n int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("the connection was reset")
	}
	k := copy(p, f.b[:min(len(f.b), f.n)])
	f.b, f.n = f.b[k:], f.n-k
	return k, nil
}

func csvFile(name, text string, t *tally, failAfter int) ursus.CSVFile {
	return ursus.CSVFile{Name: name, Open: func(context.Context) (io.ReadCloser, error) {
		t.opens.Add(1)
		var r io.Reader = strings.NewReader(text)
		if failAfter > 0 {
			r = &failingReader{b: []byte(text), n: failAfter}
		}
		return &csvStream{Reader: r, closed: &t.closed}, nil
	}}
}

func TestScanCSVFrom(t *testing.T) {
	var text strings.Builder
	text.WriteString("a,s,f\n")
	for i := range 5000 {
		fmt.Fprintf(&text, "%d,x%d,%d.5\n", i, i%7, i)
	}
	body := text.String()

	t.Run("answers as ScanCSVReader, and closes every stream", func(t *testing.T) {
		var n tally
		got, err := ursus.ScanCSVFrom([]ursus.CSVFile{csvFile("d.csv", body, &n, 0)}).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want, err := ursus.ScanCSVReader([]byte(body), "d.csv").Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if d := framesDiffer(t, got, want); d != "" {
			t.Error(d)
		}
		if n.opens.Load() < 2 || n.opens.Load() != n.closed.Load() {
			t.Errorf("%d opened, %d closed", n.opens.Load(), n.closed.Load())
		}
	})

	t.Run("several files answer as ScanCSVFiles", func(t *testing.T) {
		dir := t.TempDir()
		var files []ursus.CSVFile
		var paths []string
		var n tally
		for i := range 3 {
			part := fmt.Sprintf("s,a\nx%d,%d\n,%d\n", i, i, 10*i)
			p := filepath.Join(dir, fmt.Sprintf("part-%d.csv", i))
			if err := os.WriteFile(p, []byte(part), 0o644); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, p)
			files = append(files, csvFile(p, part, &n, 0))
		}
		got, err := ursus.ScanCSVFrom(files).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want, err := ursus.ScanCSVFiles(paths).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if d := framesDiffer(t, got, want); d != "" {
			t.Error(d)
		}
		if n.opens.Load() != n.closed.Load() {
			t.Errorf("%d opened, %d closed", n.opens.Load(), n.closed.Load())
		}
	})

	t.Run("Open gets the query's context", func(t *testing.T) {
		type key struct{}
		var seen, opens atomic.Int64
		f := ursus.CSVFile{Name: "d.csv", Open: func(ctx context.Context) (io.ReadCloser, error) {
			opens.Add(1)
			if ctx.Value(key{}) == "the query" {
				seen.Add(1)
			}
			return io.NopCloser(strings.NewReader(body)), nil
		}}
		ctx := context.WithValue(t.Context(), key{}, "the query")
		if _, err := ursus.ScanCSVFrom([]ursus.CSVFile{f}).Collect(ctx); err != nil {
			t.Fatal(err)
		}
		if seen.Load() != opens.Load() || opens.Load() == 0 {
			t.Errorf("%d of %d opens saw the query's context", seen.Load(), opens.Load())
		}
	})

	t.Run("a later file that fails to open is named", func(t *testing.T) {
		var n tally
		gone := ursus.CSVFile{Name: "s3://bucket/2.csv", Open: func(context.Context) (io.ReadCloser, error) {
			return nil, errors.New("NoSuchKey")
		}}
		_, err := ursus.ScanCSVFrom([]ursus.CSVFile{csvFile("s3://bucket/1.csv", body, &n, 0), gone}).
			Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrIO, "s3://bucket/2.csv", "NoSuchKey"); d != "" {
			t.Error(d)
		}
		if n.opens.Load() != n.closed.Load() {
			t.Errorf("%d opened, %d closed", n.opens.Load(), n.closed.Load())
		}
	})

	t.Run("a read that fails mid-stream is an I/O error naming the file, and closes", func(t *testing.T) {
		var n tally
		f := csvFile("s3://bucket/d.csv", body, &n, 0)
		open := f.Open
		f.Open = func(ctx context.Context) (io.ReadCloser, error) {
			if n.opens.Load() == 0 {
				return open(ctx) // inference reads it whole
			}
			n.opens.Add(1)
			return &csvStream{Reader: &failingReader{b: []byte(body), n: len(body) / 2}, closed: &n.closed}, nil
		}
		_, err := ursus.ScanCSVFrom([]ursus.CSVFile{f}).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrIO, "s3://bucket/d.csv", "connection was reset"); d != "" {
			t.Error(d)
		}
		if n.opens.Load() != n.closed.Load() {
			t.Errorf("%d opened, %d closed", n.opens.Load(), n.closed.Load())
		}
	})

	t.Run("a later file that fails mid-stream is named", func(t *testing.T) {
		var n tally
		_, err := ursus.ScanCSVFrom([]ursus.CSVFile{csvFile("s3://bucket/1.csv", body, &n, 0),
			csvFile("s3://bucket/2.csv", body, &n, len(body)/2)}).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrIO, "s3://bucket/2.csv"); d != "" {
			t.Error(d)
		}
	})

	t.Run("no files, or a file with no Open, is refused", func(t *testing.T) {
		_, err := ursus.ScanCSVFrom(nil).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrValue, "no files"); d != "" {
			t.Error(d)
		}
		_, err = ursus.ScanCSVFrom([]ursus.CSVFile{{Name: "x.csv"}}).Collect(t.Context())
		if d := refusal(nil, err, ursus.ErrValue, "x.csv", "Open"); d != "" {
			t.Error(d)
		}
	})

	t.Run("Explain names the files", func(t *testing.T) {
		var n tally
		plan, err := ursus.ScanCSVFrom([]ursus.CSVFile{csvFile("s3://bucket/1.csv", body, &n, 0),
			csvFile("s3://bucket/2.csv", body, &n, 0)}).Explain(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, "s3://bucket/1.csv and 1 more") {
			t.Errorf("Explain does not name the files:\n%s", plan)
		}
	})
}
