package parquet

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

// writeIntGroups writes groups row groups of rows rows each, of one REQUIRED Int64
// column "a" holding each row's place in the file.
func writeIntGroups(t *testing.T, path string, groups, rows int) {
	t.Helper()
	node, err := schema.NewPrimitiveNodeLogical("a", parquet.Repetitions.Required,
		schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required, schema.FieldList{node}, -1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	for g := range groups {
		vals := make([]int64, rows)
		for i := range vals {
			vals[i] = int64(g*rows + i)
		}
		rg := w.AppendRowGroup()
		cw, err := rg.NextColumn()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(vals, nil, nil); err != nil {
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
}

// openParallel opens path on threads workers, batches of batch rows, under a
// read-ahead of ahead bytes.
func openParallel(t *testing.T, path string, threads, batch int, ahead int64) *parallelReader {
	t.Helper()
	bs, err := openSource(t, path).Open(t.Context(),
		source.ScanSpec{Threads: threads, BatchSize: batch, ReadAhead: ahead})
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := bs.(*parallelReader)
	if !ok {
		t.Fatalf("opened a %T, not the parallel reader", bs)
	}
	return pr
}

// TestARowGroupDecodesAheadOfTheReader: a row group not at the head decodes to its
// end while the reader holds still (step 214). Before, it stopped three batches in,
// and decoded the rest only once it was the head: traced, four or five of eight
// workers sat at their send (step 213).
func TestARowGroupDecodesAheadOfTheReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.parquet")
	const groups, rows, batch = 4, 16_384, 1024
	writeIntGroups(t, path, groups, rows)
	pr := openParallel(t, path, groups, batch, 0)
	defer pr.Close()
	if _, err := pr.Next(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := groups*rows/batch - 1 // every batch of the four, but the one taken
	deadline := time.Now().Add(10 * time.Second)
	for {
		pr.mu.Lock()
		pr.gate.mu.Lock()
		pending := 0
		for _, task := range pr.inflight {
			pending += task.pending
		}
		pr.gate.mu.Unlock()
		pr.mu.Unlock()
		if pending == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d batches decoded ahead of the reader, want %d", pending, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestReadAheadKeepsTheOrderAndItsBound: under any read-ahead, a byte, 32 KiB or
// the default, eight workers and a reader that stops now and then give every row in
// its order, hold at most the read-ahead and the floor's batches, and never stop.
func TestReadAheadKeepsTheOrderAndItsBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.parquet")
	const groups, rows, batch, threads = 12, 5_000, 512, 8
	writeIntGroups(t, path, groups, rows)
	for _, ahead := range []int64{1, 32 << 10, 0} {
		pr := openParallel(t, path, threads, batch, ahead)
		done := make(chan error, 1)
		go func() {
			next := int64(0)
			for i := 0; ; i++ {
				b, err := pr.Next(t.Context())
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					done <- err
					return
				}
				if i%7 == 3 {
					time.Sleep(200 * time.Microsecond) // a reader that stops now and then
				}
				for _, v := range data.MustValues[int64](b.Column(0)) {
					if v != next {
						done <- errors.New("rows out of order")
						return
					}
					next++
				}
			}
			if next != groups*rows {
				done <- errors.New("rows missing")
				return
			}
			done <- nil
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("read-ahead %d: %v", ahead, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("read-ahead %d: the reader stopped", ahead)
		}
		limit := pr.gate.limit
		maxBatch := int64(batch * 8) // a batch of 512 Int64s
		if bound := limit + threads*aheadFloor*maxBatch; pr.gate.peak > bound {
			t.Errorf("read-ahead %d: held %d bytes, over %d", ahead, pr.gate.peak, bound)
		}
		if err := pr.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestClosingWakesWorkersAtTheGate: a worker waiting at the gate cannot see stop,
// so Close wakes the gate before it waits for the workers; it returns, and leaves no
// goroutine behind.
func TestClosingWakesWorkersAtTheGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.parquet")
	writeIntGroups(t, path, 8, 8_192)
	before := runtime.NumGoroutine()
	pr := openParallel(t, path, 8, 256, 1)
	if _, err := pr.Next(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Let the workers reach the gate.
	time.Sleep(20 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- pr.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return: a worker waits at the gate")
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines, %d before", runtime.NumGoroutine(), before)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestATinyReadAheadHoldsTheFloor: under a read-ahead of a byte, the workers hold
// the floor's batches each and no more, the depth before step 214, however long the
// reader holds still.
func TestATinyReadAheadHoldsTheFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.parquet")
	const groups, rows, batch = 4, 16_384, 1024
	writeIntGroups(t, path, groups, rows)
	pr := openParallel(t, path, groups, batch, 1)
	defer pr.Close()
	if _, err := pr.Next(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	pr.mu.Lock()
	pr.gate.mu.Lock()
	pending := 0
	for _, task := range pr.inflight {
		pending += task.pending
	}
	pr.gate.mu.Unlock()
	pr.mu.Unlock()
	if pending > groups*aheadFloor {
		t.Fatalf("%d batches held ahead under a read-ahead of a byte, over %d", pending, groups*aheadFloor)
	}
	if pending < groups {
		t.Fatalf("%d batches held ahead: the workers did not read ahead at all", pending)
	}
}
