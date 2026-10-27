package ursus

import (
	"context"
	"iter"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestAQueryTakesItsShareOfItsEngine (step 397, B2 of v0.8-scope.md): a query given
// no WithThreads runs on its engine's threads divided by its queries on more than
// one, this one counted, at least one; one on a single thread takes none; one given
// WithThreads its own, counted; and each gives them back as it ends.
func TestAQueryTakesItsShareOfItsEngine(t *testing.T) {
	if NewEngine().threads != runtime.GOMAXPROCS(0) {
		t.Errorf("an engine's threads %d, want GOMAXPROCS %d", NewEngine().threads, runtime.GOMAXPROCS(0))
	}
	e := NewEngine(WithEngineThreads(8), WithEngineMemoryLimit(0))
	var got []int
	for range 5 {
		got = append(got, e.share(8))
	}
	if want := []int{8, 4, 2, 2, 1}; !equalInts(got, want) {
		t.Errorf("five queries wanting eight of eight threads given %v, want %v", got, want)
	}
	if e.share(1) != 1 || e.share(3) != 1 || e.Stats().Parallel != 4 {
		t.Errorf("a query of one thread, or of none to spare, counted: %+v", e.Stats())
	}
	for _, n := range got {
		e.release(n)
	}
	if p := e.Stats().Parallel; p != 0 {
		t.Errorf("%d queries on more than one thread after all ended", p)
	}

	// Queries: a sort of 100,000 rows paused halfway holds the engine's eight.
	ctx := t.Context()
	big := Frame(Values("k", make([]int64, 100_000))).Sort(Asc(Col("k")))
	planned := func(lf *LazyFrame, opts ...CollectOption) int {
		t.Helper()
		cfg := newCollectCfg(append([]CollectOption{WithEngine(e)}, opts...))
		defer cfg.done()
		root, err := lf.compile(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		return cfg.planned()
	}
	next, stop := iter.Pull2(big.CollectBatches(ctx, WithEngine(e)))
	defer stop()
	if _, err, ok := next(); !ok || err != nil {
		t.Fatalf("the first batch: %v", err)
	}
	if p := e.Stats().Parallel; p != 1 {
		t.Fatalf("a paused sort of 100,000 rows: %d on more than one thread, want 1", p)
	}
	if n := planned(big); n != 4 {
		t.Errorf("a second sort beside it planned on %d threads, want 4", n)
	}
	if n := planned(Frame(Values("k", []int64{1}))); n != 1 {
		t.Errorf("a query of a row planned on %d threads", n)
	}
	if n := planned(big, WithThreads(6)); n != 6 {
		t.Errorf("a query given WithThreads(6) planned on %d", n)
	}
	for _, threads := range []int{2, runtime.NumCPU() + 4} {
		alone := NewEngine(WithEngineThreads(threads), WithEngineMemoryLimit(0))
		if n := planned(big, WithEngine(alone)); n != threads {
			t.Errorf("a sort alone on an engine of %d threads planned on %d", threads, n)
		}
	}
	if p := e.Stats().Parallel; p != 1 {
		t.Errorf("after the planned queries ended: %d on more than one thread, want 1", p)
	}
	stop()
	if p := e.Stats().Parallel; p != 0 {
		t.Errorf("after the paused sort stopped: %d", p)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAFrameOperationTakesItsShare: an eager frame operation's goroutines are its
// share of DefaultEngine's threads, where they were GOMAXPROCS each, and given back.
func TestAFrameOperationTakesItsShare(t *testing.T) {
	e := NewEngine(WithEngineThreads(2), WithEngineMemoryLimit(0))
	defer SetDefaultEngine(e)()
	var active, most atomic.Int64
	err := inParallel(context.Background(), 64, func(int) error {
		n := active.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(200 * time.Microsecond)
		active.Add(-1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if m := most.Load(); m > 2 {
		t.Errorf("%d pieces at once on an engine of two threads", m)
	}
	if p := e.Stats().Parallel; p != 0 {
		t.Errorf("%d on more than one thread after it", p)
	}
	df, err := Frame(Values("k", make([]int64, 200_000))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := df.PartitionBy(t.Context(), PartitionOptions{By: []string{"k"}}); err != nil {
		t.Fatal(err)
	}
	if p := e.Stats().Parallel; p != 0 {
		t.Errorf("%d on more than one thread after PartitionBy", p)
	}
}
