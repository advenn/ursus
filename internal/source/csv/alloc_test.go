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
