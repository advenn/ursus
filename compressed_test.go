package ursus_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zlibbed(t *testing.T, b []byte, level int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, level)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zstdded(t *testing.T, b []byte) []byte {
	t.Helper()
	w, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	return w.EncodeAll(b, nil)
}

// TestCompressedInput is step 346: a CSV, an NDJSON or a JSON file compressed by gzip,
// zlib or Zstandard is read as itself, whatever its name, as Polars reads it, and
// streamed as it is read. Every answer is Polars 1.44.1's but where noted.
func TestCompressedInput(t *testing.T) {
	ctx := t.Context()
	csvText := []byte("a,b\n1,x\n2,y\n")
	want := dfOf(t, i64s("a", 1, 2), strs("b", "x", "y"))
	read := func(t *testing.T, lf *ursus.LazyFrame) *ursus.DataFrame {
		t.Helper()
		got, err := lf.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	dir := t.TempDir()
	compressed := map[string][]byte{
		"gzip":                 gzipped(t, csvText),
		"two gzip members":     append(gzipped(t, csvText[:8]), gzipped(t, csvText[8:])...),
		"zlib, 78 01":          zlibbed(t, csvText, zlib.BestSpeed),
		"zlib, 78 5e":          zlibbed(t, csvText, 3),
		"zlib, 78 9c":          zlibbed(t, csvText, zlib.DefaultCompression),
		"zlib, 78 da":          zlibbed(t, csvText, zlib.BestCompression),
		"Zstandard":            zstdded(t, csvText),
		"plain, for reference": csvText,
	}
	for name, data := range compressed {
		if name == "zlib, 78 5e" && fmt.Sprintf("%x", data[:2]) != "785e" {
			t.Fatalf("zlib level 3 begins %x", data[:2])
		}
		t.Run(name, func(t *testing.T) {
			ursustest.AssertFrameEqual(t, read(t, ursus.ScanCSVReader(data, "t")), want)
			// A file's name says nothing: "t.csv" holds the compressed bytes.
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".csv")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, read(t, ursus.ScanCSV(path)), want)
			// The run's stream, past a schema given, as the inference's.
			ursustest.AssertFrameEqual(t, read(t, ursus.ScanCSV(path, ursus.WithSchema(want.Schema()))), want)
		})
	}

	t.Run("NDJSON and JSON", func(t *testing.T) {
		nd := []byte("{\"a\":1,\"b\":\"x\"}\n{\"a\":2,\"b\":\"y\"}\n")
		doc := []byte(`[{"a":1,"b":"x"},{"a":2,"b":"y"}]`)
		for name, c := range map[string]func([]byte) []byte{
			"gzip":      func(b []byte) []byte { return gzipped(t, b) },
			"zlib":      func(b []byte) []byte { return zlibbed(t, b, 6) },
			"Zstandard": func(b []byte) []byte { return zstdded(t, b) },
		} {
			ursustest.AssertFrameEqual(t, read(t, ursus.ScanNDJSONBytes(c(nd), name)), want)
			ursustest.AssertFrameEqual(t, read(t, ursus.ScanJSONBytes(c(doc), name)), want)
		}
	})

	t.Run("a text a zlib header begins", func(t *testing.T) {
		// "x^" is 78 5e, zlib's header at its third level: a CSV whose header it
		// begins is read as text. Polars reads it as zlib, and finds no rows
		// (feature-gaps §9).
		got := read(t, ursus.ScanCSVReader([]byte("x^2,b\n1,2\n"), "t"))
		if s := got.Schema().String(); s != "{x^2: Int64, b: Int64}" || got.Height() != 1 {
			t.Errorf("%s, %d rows", s, got.Height())
		}
		big := []byte("x^2,b\n" + strings.Repeat("1,2\n", 40000))
		if got := read(t, ursus.ScanCSVReader(big, "t")); got.Height() != 40000 {
			t.Errorf("past the 64 KiB tried: %d rows", got.Height())
		}
	})

	t.Run("refused", func(t *testing.T) {
		gz := gzipped(t, csvText)
		var rows strings.Builder
		rows.WriteString("a,b\n")
		for i := range 2000 {
			fmt.Fprintf(&rows, "%d,x%d\n", i*7919%10007, i)
		}
		zs := zstdded(t, []byte(rows.String()))
		for name, data := range map[string][]byte{
			// Polars: "unexpected end of file", both.
			"a gzip stream cut short":    gz[:len(gz)-6],
			"text after a gzip stream":   append(append([]byte(nil), gz...), "junk"...),
			"text after a zlib stream":   append(zlibbed(t, csvText, 6), "junk"...),
			"a Zstandard stream cut off": zs[:len(zs)/2],
		} {
			if _, err := ursus.ScanCSVReader(data, "t").Collect(ctx); err == nil {
				t.Errorf("%s: read", name)
			}
		}
	})

	t.Run("in parts", func(t *testing.T) {
		// A file of many blocks, decompressed as it is read and parsed a part a
		// thread, as its text is.
		rows := 150000 // three blocks of the reader's
		if raceDetector {
			rows = 80000
		}
		var text bytes.Buffer
		text.WriteString("i,s,f\n")
		for i := range rows {
			fmt.Fprintf(&text, "%d,s%d,%d.5\n", i, i%977, i%31)
		}
		plain, err := ursus.ScanCSVReader(text.Bytes(), "t").Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"gzip": gzipped(t, text.Bytes()), "Zstandard": zstdded(t, text.Bytes()),
			"zlib, past the 64 KiB tried": zlibbed(t, text.Bytes(), 6)} {
			for _, threads := range []int{1, 8} {
				got, err := ursus.ScanCSVReader(data, name).Collect(ctx, ursus.WithThreads(threads))
				if err != nil {
					t.Fatal(err)
				}
				if same, err := got.Equals(ctx, plain, true); err != nil || !same || got.Schema().String() != plain.Schema().String() {
					t.Errorf("%s on %d threads: not the plain file's frame (%v)", name, threads, err)
				}
			}
			head, err := ursus.ScanCSVReader(data, name).Head(3).Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			first, err := plain.Head(ctx, 3)
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, head, first)
		}
	})
}
