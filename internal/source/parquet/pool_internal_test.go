package parquet

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

// TestPagePoolsSizes: a buffer of the pool is its size's power of two from 4 KiB, a
// smaller or a larger one exactly its size; a grown one keeps what it held; one the
// pool did not make is let go.
func TestPagePoolsSizes(t *testing.T) {
	var p pagePool
	for _, c := range []struct{ size, cap int }{
		{1, 4096}, {4096, 4096}, {4097, 8192}, {1 << 20, 1 << 20}, {1<<20 + 1, 1 << 21}, {maxPooled, maxPooled},
	} {
		b := p.Allocate(c.size)
		if len(b) != c.size || cap(b) != c.cap {
			t.Errorf("Allocate(%d): len %d cap %d, want %d and %d", c.size, len(b), cap(b), c.size, c.cap)
		}
		p.Free(b)
	}
	if b := p.Allocate(maxPooled + 1); len(b) != maxPooled+1 || cap(b) != maxPooled+1 {
		t.Errorf("Allocate past the pool: len %d cap %d", len(b), cap(b))
	}
	b := p.Allocate(5000)
	for i := range b {
		b[i] = byte(i)
	}
	g := p.Reallocate(20000, b)
	if len(g) != 20000 || cap(g) != 32768 {
		t.Fatalf("Reallocate: len %d cap %d", len(g), cap(g))
	}
	for i := range 5000 {
		if g[i] != byte(i) {
			t.Fatalf("Reallocate lost byte %d", i)
		}
	}
	if s := p.Reallocate(100, g); &s[0] != &g[0] || len(s) != 100 {
		t.Error("Reallocate to fewer bytes moved the buffer")
	}
	p.Free(make([]byte, 5000)) // not a power of two: let go
	p.Free(nil)
}

// TestPagesAreDecompressedIntoPooledBuffers: a file of eight row groups of two Int64
// columns and a nullable String, Zstandard, a page a column chunk as DuckDB writes
// lineitem, read serially and on four threads, allocates about what it produces,
// where every page was decompressed into buffers made for it and dropped (step 376);
// and every batch reads as it was read once the whole file has been: a column that
// kept a page buffer the pool hands on would read a later page's bytes.
func TestPagesAreDecompressedIntoPooledBuffers(t *testing.T) {
	const groups, rows = 8, 50_000
	path := filepath.Join(t.TempDir(), "t.parquet")
	want := writePooledFile(t, path, groups, rows)

	for _, threads := range []int{1, 4} {
		// A read-ahead of a byte, as TestReadingAllocatesAboutWhatItProduces reads.
		bs, err := openSource(t, path).Open(t.Context(),
			source.ScanSpec{Threads: threads, BatchSize: 8192, ReadAhead: 1})
		if err != nil {
			t.Fatal(err)
		}
		var batches []*data.Batch
		// Two row groups first: the pool has a buffer of each class once the first
		// one's readers have closed.
		n := 0
		for n < 2*rows {
			b, err := bs.Next(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			batches = append(batches, b)
			n += b.Rows()
		}
		runtime.GC()
		before := allocatedBytes()
		var produced int64
		for {
			b, err := bs.Next(t.Context())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			batches = append(batches, b)
			n += b.Rows()
			for _, c := range b.Columns() {
				produced += c.NBytes()
			}
		}
		allocated := int64(allocatedBytes() - before)
		bs.Close()
		if n != groups*rows {
			t.Fatalf("%d threads: read %d rows, want %d", threads, n, groups*rows)
		}
		t.Logf("%d threads: allocated %.1f MB to produce %.1f MB: %.2f", threads,
			float64(allocated)/(1<<20), float64(produced)/(1<<20), float64(allocated)/float64(produced))
		// One thread read 1.36 with the pool and 2.30 without, the compressed chunks
		// GetStream reads the 0.36. Four threads, whose workers decode some pages before
		// the count starts, read 1.16 and 1.38, so only a gross regression shows there.
		bound := produced * 16 / 10
		if threads > 1 {
			bound = produced * 15 / 10
		}
		if allocated > bound && !raceDetector {
			t.Errorf("%d threads: reading allocated %d bytes to produce %d", threads, allocated, produced)
		}
		at := 0
		for _, b := range batches {
			a, _ := data.TypedColumn[int64](b.Column(0))
			c, _ := data.TypedColumn[int64](b.Column(1))
			s, _ := data.TypedColumn[string](b.Column(2))
			for i := range b.Rows() {
				r := at + i
				av, _ := a.Get(i)
				cv, _ := c.Get(i)
				sv, ok := s.Get(i)
				if av != want.a[r] || cv != want.c[r] || ok != (want.s[r] != "") || sv != want.s[r] {
					t.Fatalf("%d threads: row %d reads %d, %d, %q (%v) after the file was read; want %d, %d, %q",
						threads, r, av, cv, sv, ok, want.a[r], want.c[r], want.s[r])
				}
			}
			at += b.Rows()
		}
	}
}

type pooledRows struct {
	a, c []int64
	s    []string
}

// writePooledFile writes groups row groups of rows rows, Zstandard, each column chunk
// one page, and answers what it wrote.
func writePooledFile(t *testing.T, path string, groups, rows int) pooledRows {
	t.Helper()
	i64 := func(name string) schema.Node {
		n, err := schema.NewPrimitiveNodeLogical(name, parquet.Repetitions.Required,
			schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	sn, err := schema.NewPrimitiveNodeLogical("s", parquet.Repetitions.Optional,
		schema.StringLogicalType{}, parquet.Types.ByteArray, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required,
		schema.FieldList{i64("a"), i64("c"), sn}, -1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root, file.WithWriterProps(parquet.NewWriterProperties(
		parquet.WithCompression(compress.Codecs.Zstd), parquet.WithDataPageSize(64<<20))))
	var all pooledRows
	for g := range groups {
		a, c := make([]int64, rows), make([]int64, rows)
		var vals []parquet.ByteArray
		defs := make([]int16, rows)
		for i := range rows {
			r := g*rows + i
			a[i], c[i] = int64(r), int64(r*7919%100_003)
			s := ""
			if r%9 != 0 {
				// 500 values: arrow-go drops a dictionary of thousands of short
				// strings after its first page, as parquet-mr does, for PLAIN pages.
				s = fmt.Sprintf("value-%d", r%500)
				vals, defs[i] = append(vals, parquet.ByteArray(s)), 1
			}
			all.s = append(all.s, s)
		}
		all.a, all.c = append(all.a, a...), append(all.c, c...)
		rg := w.AppendRowGroup()
		for _, col := range [][]int64{a, c} {
			cw, err := rg.NextColumn()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(col, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := cw.Close(); err != nil {
				t.Fatal(err)
			}
		}
		cw, err := rg.NextColumn()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch(vals, defs, nil); err != nil {
			t.Fatal(err)
		}
		if err := cw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := rg.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return all
}

// TestAScanTellsItsPages: a scan tells ScanSpec.Pages what its page readers hold out
// of the pool, the decompressed pages of each column chunk, serially and on four
// threads, and holds none once closed, read to its end or after one batch (step
// 400).
func TestAScanTellsItsPages(t *testing.T) {
	const groups, rows = 4, 50_000
	path := filepath.Join(t.TempDir(), "t.parquet")
	writePooledFile(t, path, groups, rows)
	for _, threads := range []int{1, 4} {
		for _, whole := range []bool{true, false} {
			var mu sync.Mutex
			var held, most int64
			tell := func(d int64) {
				mu.Lock()
				defer mu.Unlock()
				held += d
				most = max(most, held)
			}
			bs, err := openSource(t, path).Open(t.Context(),
				source.ScanSpec{Threads: threads, BatchSize: 8192, Pages: tell})
			if err != nil {
				t.Fatal(err)
			}
			for {
				_, err := bs.Next(t.Context())
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if !whole {
					break
				}
			}
			bs.Close()
			mu.Lock()
			// A column chunk of 50,000 Int64s is a page of 400,000 bytes decompressed.
			if most < 400_000 {
				t.Errorf("%d threads, read whole %v: told at most %d bytes of pages", threads, whole, most)
			}
			if held != 0 {
				t.Errorf("%d threads, read whole %v: holds %d bytes of pages once closed", threads, whole, held)
			}
			mu.Unlock()
		}
	}
}

// TestAScansPagesEndWithIt: what a scan holds is told as it takes and gives back
// buffers, a grown one by its new size; a buffer a page reader keeps past the scan's
// end is untold as it closes, and one given back afterwards is not told again (step
// 400).
func TestAScansPagesEndWithIt(t *testing.T) {
	var held int64
	s := newScanPages(func(d int64) { held += d })
	a := s.Allocate(5000)
	b := s.Allocate(100)
	if held != 8192+4096 {
		t.Fatalf("two buffers of 8 and 4 KiB told as %d bytes", held)
	}
	b = s.Reallocate(20_000, b)
	if held != 8192+32768 {
		t.Fatalf("a buffer grown to 32 KiB told as %d bytes with the other's 8", held)
	}
	s.Free(b)
	if held != 8192 {
		t.Fatalf("one of 8 KiB left told as %d bytes", held)
	}
	s.close()
	if held != 0 {
		t.Fatalf("a closed scan told as holding %d bytes", held)
	}
	s.Free(a)
	if held != 0 {
		t.Fatalf("a buffer given back once the scan closed told: %d bytes", held)
	}
}
