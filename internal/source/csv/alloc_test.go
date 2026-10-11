package csv

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

func allocatedBytes() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// TestReadingAllocatesAboutWhatItProduces: 200,000 rows of two Int64 columns, a
// Float64 and a String, read serially and on four threads, against the bytes of
// the columns produced.
//
// A fixed-width column's builder dropped its values slice every batch, believing
// the column wrapped it, and grew a new one by append, past 256 values a quarter at
// a time; the column then copied it. Each batch's values were allocated about six
// times over. In h2o gb10's profile over CSV the builders were 2.4 GB, 10% of
// everything the query allocated (step 132). The builder now keeps its slice, as
// the String builder always kept its buffers (step 136).
//
// From the second batch on: the first grows what every reader grows once, its
// staging and its builders, which 200,000 rows do not amortize and ten million do.
//
// On four threads each worker grows its own, so the reading starts after every
// worker's first batch, and its bound is looser: the reader parses ahead, and how
// much of the reading's allocation happened before it began is the scheduler's.
// From the second batch it read 1.14 to 1.47 on one machine, and a gate failed on it
// (step 185); from each worker's second, 0.21 to 1.11. The whole read is no measure
// either: each worker's staging, about 8 MB, is a fixed cost these rows do not
// amortize. The fault this test is for allocated each value about six times.
//
// Not parallel, so no other test allocates while it reads.
func TestReadingAllocatesAboutWhatItProduces(t *testing.T) {
	const rows = 200_000
	var sb strings.Builder
	sb.WriteString("id,v,w,s\n")
	for i := range rows {
		fmt.Fprintf(&sb, "%d,%d.5,%d,k%d\n", i, i%977, i*31, i%1000)
	}
	path := filepath.Join(t.TempDir(), "t.csv")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, threads := range []int{1, 4} {
		bs, err := FromFile(path, DefaultOptions()).Open(t.Context(),
			source.ScanSpec{Threads: threads, BatchSize: 8192})
		if err != nil {
			t.Fatal(err)
		}
		first, err := bs.Next(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		n := first.Rows()
		// Every worker's first batch precedes the reading; the batches come in input
		// order, a worker's chunks in turn.
		for range 2*threads - 2 {
			b, err := bs.Next(t.Context())
			if err != nil {
				t.Fatal(err)
			}
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
			n += b.Rows()
			for _, c := range b.Columns() {
				produced += c.NBytes()
			}
		}
		allocated := int64(allocatedBytes() - before)
		bs.Close()
		if n != rows {
			t.Fatalf("%d threads: read %d rows, want %d", threads, n, rows)
		}
		t.Logf("%d threads: allocated %.1f MB to produce %.1f MB: %.2f", threads,
			float64(allocated)/(1<<20), float64(produced)/(1<<20), float64(allocated)/float64(produced))
		bound := produced * 5 / 4
		if threads > 1 {
			bound = produced * 3 / 2
		}
		if allocated > bound {
			t.Errorf("%d threads: reading allocated %d bytes to produce %d", threads, allocated, produced)
		}
		// The builders keep their buffers from batch to batch, which is safe only
		// because every column copies out of them: the first batch must read as it
		// did after every later one was built.
		ids, err := data.TypedColumn[int64](first.Column(0))
		if err != nil {
			t.Fatal(err)
		}
		vs, err := data.TypedColumn[float64](first.Column(1))
		if err != nil {
			t.Fatal(err)
		}
		ss, err := data.TypedColumn[string](first.Column(3))
		if err != nil {
			t.Fatal(err)
		}
		for i := range first.Rows() {
			id, _ := ids.Get(i)
			v, _ := vs.Get(i)
			s, _ := ss.Get(i)
			if id != int64(i) || v != float64(i%977)+0.5 || s != fmt.Sprintf("k%d", i%1000) {
				t.Fatalf("%d threads: the first batch's row %d reads %d, %v, %q after the rest was read",
					threads, i, id, v, s)
			}
		}
	}
}

// TestABlockAllocatesWhatItHolds: the parallel path's blocks, parsed one after
// another, against the bytes of the columns they produce. Before step 188 a block's
// builders were fresh, grew by append, about a quarter at a time once large, and
// were copied out by NewFixed and NewStringParts: reading h2o's 2M-row CSV on four
// threads allocated 1,041 MB to produce 251 MB. A block's rows are counted before
// it is parsed, so its builders are now sized once and its columns take them.
//
// TestReadingAllocatesAboutWhatItProduces reads past the first window of blocks
// before it measures, and a small file is parsed by then, so it never saw this.
//
// Two String columns change width halfway, one from 20 bytes to 1 and one the
// other way, so the blocks there are sized from a block of the other width: the
// one too large is copied to its size, and the one too small grows.
func TestABlockAllocatesWhatItHolds(t *testing.T) {
	const rows = 100_000
	var sb strings.Builder
	sb.WriteString("id,v,b,s,w,u\n")
	wide := strings.Repeat("x", 20)
	for i := range rows {
		w, u := wide, "y"
		if i >= rows/2 {
			w, u = u, w
		}
		fmt.Fprintf(&sb, "%d,%d.5,%t,k%d,%s,%s\n", i, i%977, i%3 == 0, i%1000, w, u)
	}
	path := filepath.Join(t.TempDir(), "t.csv")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func(size int) { splitBlockSize = size }(splitBlockSize)
	splitBlockSize = 256 << 10

	bs, err := FromFile(path, DefaultOptions()).Open(t.Context(), source.ScanSpec{Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	r := bs.(*reader)
	var allocated, held, ideal int64
	n, blocks := 0, 0
	for {
		blk, ok, err := r.split.next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		blk.wanted, blk.part = r.wanted, r.src.partName(r.next-1)
		blk.count() // as parseLater counts a speculative block, before its parse
		runtime.GC()
		before := allocatedBytes()
		b, err := r.parseBlock(blk)
		allocated += int64(allocatedBytes() - before)
		if err != nil {
			t.Fatal(err)
		}
		if b.Rows() != blk.rows {
			t.Fatalf("block %d: %d rows parsed, %d counted", blocks, b.Rows(), blk.rows)
		}
		for i, c := range b.Columns() {
			// What the column needs: its values, a validity bit a row, and for a
			// String its offsets and characters. It may hold an eighth more, and
			// what aligns each of its buffers to 64 bytes.
			bits := int64(b.Rows()+7) / 8
			need := bits
			switch i {
			case 0, 1:
				need += int64(b.Rows()) * 8
			case 2:
				need += bits
			default:
				ss, err := data.TypedColumn[string](c)
				if err != nil {
					t.Fatal(err)
				}
				need += int64(b.Rows()+1) * 4
				for j := range b.Rows() {
					s, _ := ss.Get(j)
					need += int64(len(s))
				}
			}
			if got := c.NBytes(); got > need+need/8+3*64 {
				t.Errorf("block %d: column %s holds %d bytes, and needs %d", blocks, c.Name(), got, need)
			}
			held += c.NBytes()
			ideal += need
		}
		ids, err := data.TypedColumn[int64](b.Column(0))
		if err != nil {
			t.Fatal(err)
		}
		ws, err := data.TypedColumn[string](b.Column(4))
		if err != nil {
			t.Fatal(err)
		}
		for j := range b.Rows() {
			id, _ := ids.Get(j)
			w, _ := ws.Get(j)
			if want := wide; id != int64(n+j) || (id < rows/2) != (w == want) {
				t.Fatalf("block %d row %d: id %d, w %q", blocks, j, id, w)
			}
		}
		n += b.Rows()
		blocks++
	}
	if n != rows || blocks < 8 {
		t.Fatalf("%d rows in %d blocks, want %d rows in at least 8", n, blocks, rows)
	}
	t.Logf("%d blocks: allocated %.2f MB, the columns hold %.2f MB and need %.2f MB",
		blocks, float64(allocated)/(1<<20), float64(held)/(1<<20), float64(ideal)/(1<<20))
	// A block's scanner and builders allocate what its columns hold, a fifth more
	// at most: the columns that grew past their estimate, where the width changed.
	if allocated > held+held/5 {
		t.Errorf("the blocks allocated %d bytes, and their columns hold %d", allocated, held)
	}
}
