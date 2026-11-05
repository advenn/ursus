package ursus_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// ScanLines and its spellings (step 492), Polars 2.x's scan_lines and read_lines.

// lineCells is q's first column as text, the rows of a frame of lines.
func lineCells(t *testing.T, q *ursus.LazyFrame, opts ...ursus.CollectOption) []string {
	t.Helper()
	df, err := q.Collect(t.Context(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	col, err := df.Column[string](df.Schema().Field(0).Name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, df.Height())
	for i := range out {
		out[i], _ = col.Get(i)
	}
	return out
}

// scannerLines is text's lines as bufio.Scanner cuts them, the oracle.
func scannerLines(text []byte) []string {
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(nil, len(text)+1)
	out := []string{}
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func TestScanLines(t *testing.T) {
	// Polars 2.1.0 gives these, pl.scan_lines(b) each: a final newline ends the last
	// line, one "\r" before a newline or the end is dropped, and a byte-order mark is
	// kept.
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"a\nb\nc\n", []string{"a", "b", "c"}},
		{"a\nb\nc", []string{"a", "b", "c"}},
		{"", []string{}},
		{"\n", []string{""}},
		{"\n\n", []string{"", ""}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"a\r\nb", []string{"a", "b"}},
		{"a\rb\n", []string{"a\rb"}},
		{"a\r", []string{"a"}},
		{"a\r\r\n", []string{"a\r"}},
		{"a\n\nb\n\n", []string{"a", "", "b", ""}},
		{"\xef\xbb\xbfa\nb\n", []string{"\xef\xbb\xbfa", "b"}},
		{"\xef\xbb\xbf", []string{"\xef\xbb\xbf"}},
		{"a\x00b\n", []string{"a\x00b"}},
		{"  a  \n\t\n", []string{"  a  ", "\t"}},
	} {
		if got := lineCells(t, ursus.ScanLinesBytes([]byte(c.in), "b")); !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, Polars %q", c.in, got, c.want)
		}
	}
	// Polars 2.1.0: the column is a String, "line", or name=; text that is not UTF-8
	// is refused, "invalid utf8", and counted, as a count reads no text.
	if s, err := ursus.ScanLinesBytes([]byte("a"), "b").CollectSchema(t.Context()); err != nil ||
		s.Len() != 1 || s.Field(0).Name != "line" || s.Field(0).Type != dtype.String {
		t.Errorf("the schema is %v, %v, Polars {line: String}", s, err)
	}
	if got := lineCells(t, ursus.ScanLinesBytes([]byte("a"), "b", ursus.WithLinesName("x")).Select(ursus.Col("x"))); !slices.Equal(got, []string{"a"}) {
		t.Errorf("WithLinesName: %q", got)
	}
	bad := ursus.ScanLinesBytes([]byte("ok\n\xff\n"), "b")
	if _, err := bad.Collect(t.Context()); !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("a line not UTF-8: %v, want a value error at line 2", err)
	}
	if n, err := bad.Count(t.Context()); err != nil || n != 2 {
		t.Errorf("a count of lines not UTF-8: %d, %v, Polars 2", n, err)
	}

	// Polars 2.1.0 reads a directory's files in order, each file's last line its own,
	// a gzip file decompressed, the path column after the lines.
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	one, two := write("d/one.txt", []byte("a\nb\n")), write("d/two.txt", []byte("c"))
	write("d/three.txt", nil)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte("x\ny\n"))
	zw.Close()
	z := write("z.txt.gz", gz.Bytes())
	fsys := fstest.MapFS{"d/one.txt": {Data: []byte("a\nb\n")}, "d/two.txt": {Data: []byte("c")}}
	from := []ursus.LinesFile{{Name: "two", Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("c")), nil
	}}}
	for _, c := range []struct {
		name string
		q    *ursus.LazyFrame
		want []string
	}{
		{"a directory", ursus.ScanLines(filepath.Join(dir, "d")), []string{"a", "b", "c"}},
		{"its paths", ursus.ScanLines(filepath.Join(dir, "d"), ursus.WithLinesIncludeFilePaths("p")).Select(ursus.Col("p")),
			[]string{one, one, two}},
		{"files, in their order", ursus.ScanLinesFiles([]string{two, two, one}), []string{"c", "c", "a", "b"}},
		{"a glob", ursus.ScanLinesGlob(filepath.Join(dir, "d", "t*.txt")), []string{"c"}},
		{"gzip", ursus.ScanLines(z), []string{"x", "y"}},
		{"an fs.FS", ursus.ScanLinesFS(fsys, "d"), []string{"a", "b", "c"}},
		{"openers", ursus.ScanLinesFrom(append(from, from...)), []string{"c", "c"}},
	} {
		if got := lineCells(t, c.q); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, Polars %q", c.name, got, c.want)
		}
	}
	// Polars 2.1.0 refuses a path column of the lines' name, and a missing file.
	for name, q := range map[string]*ursus.LazyFrame{
		"a path column named line": ursus.ScanLinesBytes([]byte("a"), "b", ursus.WithLinesIncludeFilePaths("line")),
		"a missing file":           ursus.ScanLines(filepath.Join(dir, "nope.txt")),
	} {
		if _, err := q.Collect(t.Context()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	// The oracle: bufio.Scanner, over 3 MB of generated lines, past the first blocks of
	// 1 and 2 MiB, one line longer than a block among them, "\r\n" ends, a lone "\r",
	// empty lines and text of every width; each way the scan can cut it, on one thread
	// and four, in small batches and in its own, its first rows, its count, and a sort
	// that spills.
	rng := rand.New(rand.NewPCG(492, 1))
	pieces := []string{"a", "bc", " ", "é", "日本", "\r", "x\x00y"}
	var text bytes.Buffer
	for text.Len() < 3<<20 {
		if text.Len() > 1<<20 && text.Len() < 1<<20+100 {
			text.WriteString(strings.Repeat("long", 1<<19)) // 2 MiB, one line
		}
		for range rng.IntN(12) {
			text.WriteString(pieces[rng.IntN(len(pieces))])
		}
		if rng.IntN(3) == 0 {
			text.WriteString("\r\n")
		} else {
			text.WriteString("\n")
		}
	}
	text.WriteString("the last line, no newline\r")
	want := scannerLines(text.Bytes())
	path := write("big.txt", text.Bytes())
	for _, opts := range [][]ursus.CollectOption{
		{ursus.WithThreads(1)},
		{ursus.WithThreads(4), ursus.WithBatchSize(1000)},
		{ursus.WithThreads(4)},
	} {
		if got := lineCells(t, ursus.ScanLines(path), opts...); !slices.Equal(got, want) {
			t.Fatalf("%d lines, bufio.Scanner %d", len(got), len(want))
		}
	}
	if got := lineCells(t, ursus.ScanLines(path).Head(5)); !slices.Equal(got, want[:5]) {
		t.Errorf("the first five: %q, want %q", got, want[:5])
	}
	if n, err := ursus.ScanLines(path).Count(t.Context()); err != nil || n != int64(len(want)) {
		t.Errorf("the count: %d, %v, want %d", n, err, len(want))
	}
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	if got := lineCells(t, ursus.ScanLines(path).Sort(ursus.Asc(ursus.Col("line"))),
		ursus.WithMemoryLimit(256<<10), ursus.WithThreads(4)); !slices.Equal(got, sorted) {
		t.Errorf("sorted, spilled: %d lines, want %d", len(got), len(sorted))
	}
}
