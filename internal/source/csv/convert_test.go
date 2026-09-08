package csv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/source"
)

// barrierBuilder blocks inside appendField until the test has seen every column
// arrive. It is how this file proves conversion is concurrent rather than merely
// capable of being: a serial convertAll can only ever get one builder in.
type barrierBuilder struct {
	colBuilder // the real one, so the batch it builds is still well formed
	id         int
	arrived    chan int
	release    chan struct{}
}

func (b *barrierBuilder) appendField(f []byte) error {
	// A NON-BLOCKING send, and the reason is worth keeping. The first version
	// blocked here, so once a failing test stopped draining the channel the
	// reader deadlocked inside Next while holding its mutex — and the tooth that
	// should have failed in three seconds hung the package for ten minutes
	// instead. A tooth that hangs is nearly as bad as one that does not bite.
	select {
	case b.arrived <- b.id:
	default:
	}
	<-b.release
	return b.colBuilder.appendField(f)
}

// barrier wraps the builder already in place, whatever its type.
func barrier(inner colBuilder, id int, arrived chan int, release chan struct{}) colBuilder {
	return &barrierBuilder{colBuilder: inner, id: id, arrived: arrived, release: release}
}

func stringBarrier(t *testing.T, id int, arrived chan int, release chan struct{}) colBuilder {
	t.Helper()
	inner, err := newBuilder(dtype.String)
	if err != nil {
		t.Fatal(err)
	}
	return barrier(inner, id, arrived, release)
}

// TestConvertAllRunsColumnsConcurrently is step 42's claim, tested directly.
//
// Step 36 shipped a fleet of join workers where only one ever ran, and every
// output test passed because the results were correct either way — the same is
// true here, since a serial conversion produces exactly the frame a parallel one
// does. So the distribution has to be observed rather than inferred: each column
// blocks on entry, and the test can only collect all four if all four are in
// flight at once.
func TestConvertAllRunsColumnsConcurrently(t *testing.T) {
	const cols = 4
	arrived := make(chan int, 256)
	release := make(chan struct{})

	r := &reader{
		threads:  cols,
		wanted:   []int{0, 1, 2, 3},
		stage:    make([]colStage, cols),
		builders: make([]colBuilder, cols),
	}
	for i := range cols {
		r.stage[i].reset()
		r.stage[i].add([]byte("x"), false)
		r.builders[i] = stringBarrier(t, i, arrived, release)
	}

	done := make(chan error, 1)
	go func() { done <- r.convertAll() }()

	seen := make(map[int]bool, cols)
	deadline := time.After(3 * time.Second)
	for len(seen) < cols {
		select {
		case id := <-arrived:
			seen[id] = true
		case <-deadline:
			close(release)
			t.Fatalf("only %d of %d columns entered conversion before the deadline; "+
				"they are being converted one after another, not concurrently", len(seen), cols)
		}
	}
	close(release)

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for i, b := range r.builders {
		if got := b.len(); got != 1 {
			t.Errorf("column %d appended %d values, want 1", i, got)
		}
	}
}

// TestConvertAllStaysSerialForOneThread pins the other half of the split. The
// serial path is not an optimisation of the parallel one, it is a different
// path — WithThreads(1) is documented as reproducing the serial operator tree
// exactly, and a reader that spawned goroutines anyway would break that promise
// silently.
func TestConvertAllStaysSerialForOneThread(t *testing.T) {
	for _, threads := range []int{1, 0, -1} {
		arrived := make(chan int, 256)
		release := make(chan struct{})
		close(release) // never block: a serial run must complete on its own

		r := &reader{
			threads:  threads,
			wanted:   []int{0, 1},
			stage:    make([]colStage, 2),
			builders: make([]colBuilder, 2),
		}
		for i := range 2 {
			r.stage[i].reset()
			r.stage[i].add([]byte("x"), false)
			r.builders[i] = stringBarrier(t, i, arrived, release)
		}
		if err := r.convertAll(); err != nil {
			t.Fatalf("threads=%d: %v", threads, err)
		}
		// Both columns still converted; only the goroutines are absent.
		if len(arrived) != 2 {
			t.Errorf("threads=%d: %d columns converted, want 2", threads, len(arrived))
		}
	}
}

// TestThreadsReachTheConverter closes the loop the unit tests above leave open.
// They build a reader by hand, so they would keep passing if ScanSpec.Threads
// never reached it — which is exactly the failure step 36 shipped. This one goes
// through Open, so every link is under test: the spec's Threads, the reader's
// field, the staged/serial choice in Next, and the goroutines in convertAll.
func TestThreadsReachTheConverter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.csv")
	if err := os.WriteFile(path, []byte("a,b,c,d\n1,2,3,4\n5,6,7,8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := FromFile(path, DefaultOptions())
	bs, err := src.Open(t.Context(), source.ScanSpec{Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	r, ok := bs.(*reader)
	if !ok {
		t.Fatalf("Open returned %T, not *reader", bs)
	}

	// Swap the real builders for barriers AFTER Open, so nothing about the
	// reader's construction changes.
	arrived := make(chan int, 256)
	release := make(chan struct{})
	for i := range r.builders {
		r.builders[i] = barrier(r.builders[i], i, arrived, release)
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.Next(context.Background())
		done <- err
	}()

	seen := make(map[int]bool, 4)
	deadline := time.After(3 * time.Second)
	for len(seen) < 4 {
		select {
		case id := <-arrived:
			seen[id] = true
		case <-deadline:
			close(release)
			t.Fatalf("ScanSpec.Threads=4 but only %d of 4 columns were converting at once; "+
				"the thread count is not reaching the converter", len(seen))
		}
	}
	close(release)
	<-done
}

// TestStagedFieldsAreCopiedNotAliased is the hazard this design has in place of
// the byte-splitting one it avoids. The scanner hands out slices that alias its
// own buffer and are only valid until the next record — the doc on
// colBuilder.appendField says so. Staging defers conversion to the end of the
// batch, so a stage that KEPT the slice instead of copying it would convert
// whatever the buffer holds by then: right shape, wrong data, and only on files
// large enough for the buffer to move.
func TestStagedFieldsAreCopiedNotAliased(t *testing.T) {
	var b strings.Builder
	b.WriteString("a,b\n")
	for i := range 20000 {
		// Long, distinct values, so the scanner's buffer has to refill mid-batch
		// and any retained slice sees a different record's bytes.
		b.WriteString(strings.Repeat("x", 200) + string(rune('A'+i%26)) + ",")
		b.WriteString(strings.Repeat("y", 200) + string(rune('a'+i%26)) + "\n")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "long.csv")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	src := FromFile(path, DefaultOptions())
	bs, err := src.Open(t.Context(), source.ScanSpec{Threads: 4, BatchSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()

	row := 0
	for {
		batch, err := bs.Next(t.Context())
		if err != nil {
			break
		}
		colA := batch.Column(0).Strings()
		colB := batch.Column(1).Strings()
		for i := range batch.Rows() {
			wantA := strings.Repeat("x", 200) + string(rune('A'+row%26))
			wantB := strings.Repeat("y", 200) + string(rune('a'+row%26))
			if colA.Get(i) != wantA || colB.Get(i) != wantB {
				t.Fatalf("row %d: got %q/%q, want ...%q/...%q",
					row, last8(colA.Get(i)), last8(colB.Get(i)),
					last8(wantA), last8(wantB))
			}
			row++
		}
	}
	if row != 20000 {
		t.Errorf("read %d rows, want 20000", row)
	}
}

func last8(s string) string {
	if len(s) <= 8 {
		return s
	}
	return "…" + s[len(s)-8:]
}
