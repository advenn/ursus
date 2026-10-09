package parquet

import (
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

func allocatedBytes() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// TestReadingAllocatesAboutWhatItProduces: 400,000 rows of three Int64 columns, read
// serially and on four threads, against the bytes of the columns produced.
//
// A fixed-width column dropped its values slice every batch, its comment saying
// data.NewFixed wraps it; NewFixed copies, so each batch's values were allocated
// twice. In h2o j5's profile over Parquet that was 0.74 GB (step 137), the CSV
// reader's mistake of step 136 again. The column now keeps its slice.
//
// From the second batch on, as the CSV reader's test reads: the first grows what a
// reader grows once.
//
// Not parallel, so no other test allocates while it reads.
func TestReadingAllocatesAboutWhatItProduces(t *testing.T) {
	const rows = 400_000
	cols := make([][]int64, 3)
	for c := range cols {
		cols[c] = make([]int64, rows)
		for i := range rows {
			cols[c][i] = int64(i*(c+1)) % 100_003
		}
	}
	path := filepath.Join(t.TempDir(), "t.parquet")
	writeInts(t, path, []string{"a", "b", "c"}, cols...)

	for _, threads := range []int{1, 4} {
		bs, err := openSource(t, path).Open(t.Context(), source.ScanSpec{Threads: threads, BatchSize: 8192})
		if err != nil {
			t.Fatal(err)
		}
		first, err := bs.Next(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		n := first.Rows()
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
		if allocated > produced*13/10 {
			t.Errorf("%d threads: reading allocated %d bytes to produce %d", threads, allocated, produced)
		}
		// The column keeps its slice, which is safe only because NewFixed copies out
		// of it: the first batch must read as it did after every later one was read.
		for c := range cols {
			vals, err := data.TypedColumn[int64](first.Column(c))
			if err != nil {
				t.Fatal(err)
			}
			for i := range first.Rows() {
				if v, _ := vals.Get(i); v != cols[c][i] {
					t.Fatalf("%d threads: the first batch's row %d of column %d reads %d, want %d, "+
						"after the rest was read", threads, i, c, v, cols[c][i])
				}
			}
		}
	}
}

// TestAListColumnsFirstBatchSurvivesTheRest: a List column's elements are kept in a
// slice the column reuses from batch to batch, as a flat column's values are, which
// is safe only because data.NewFixed copies out of it. Nothing else read a batch
// after the next one was built: readLists decodes each as it arrives.
func TestAListColumnsFirstBatchSurvivesTheRest(t *testing.T) {
	rows := make([]row, 5000)
	for i := range rows {
		switch i % 7 {
		case 0:
			rows[i] = row{ok: false}
		case 1:
			rows[i] = row{ok: true}
		default:
			rows[i] = row{vals: []int64{int64(i), int64(i * 3), int64(i % 11)}, ok: true}
		}
	}
	bs, err := openSource(t, writeLists(t, rows)).Open(t.Context(), source.ScanSpec{BatchSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	first, err := bs.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := first.Rows()
	for {
		b, err := bs.Next(t.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n += b.Rows()
	}
	if n != len(rows) {
		t.Fatalf("read %d rows, want %d", n, len(rows))
	}
	got := decodeLists(t, first.Columns()[0])
	for i, r := range got {
		want := rows[i]
		if r.ok != want.ok || !slices.Equal(r.vals, want.vals) {
			t.Fatalf("the first batch's row %d reads %v, want %v, after the rest was read", i, r, want)
		}
	}
}
