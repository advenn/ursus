package ursus_test

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// parFrame builds a frame big enough to span many batches, so thread counts
// actually distribute work rather than all landing on worker 0.
func parFrame(rows int) *ursus.LazyFrame {
	ids := make([]int64, rows)
	qty := make([]int64, rows)
	tag := make([]string, rows)
	for i := range rows {
		ids[i] = int64(i)
		qty[i] = int64(i % 97)
		tag[i] = "t" + strconv.Itoa(i%7)
	}
	return ursus.Frame(
		ursus.Values("id", ids),
		ursus.Values("qty", qty),
		ursus.Values("tag", tag),
	)
}

// TestParallelIsOrderPreserving is the whole of the step's central promise, stated
// as a test: the same query at any thread count returns byte-identical output.
//
// It matters more here than a batch-size invariance test does, because five things
// in ursus depend on batch order and none of them said so — sortSink's stability,
// First and Last, distinct's first-row rule, limit, and first-appearance group ids.
// An unordered scheduler would break all five at once and silently.
func TestParallelIsOrderPreserving(t *testing.T) {
	queries := []struct {
		name string
		lf   func() *ursus.LazyFrame
	}{
		{"pipeline only", func() *ursus.LazyFrame {
			return parFrame(20_000).
				Filter(ursus.Col("qty").Gt(50)).
				WithColumns(ursus.Col("qty").Mul(2).Alias("d")).
				Select(ursus.Col("id"), ursus.Col("d"))
		}},
		{"head — which rows survive depends on order", func() *ursus.LazyFrame {
			return parFrame(20_000).Filter(ursus.Col("qty").Gt(50)).Head(17)
		}},
		{"distinct — keeps the FIRST row per key", func() *ursus.LazyFrame {
			return parFrame(20_000).Unique("tag")
		}},
		{"group by — first-appearance group ids", func() *ursus.LazyFrame {
			return parFrame(20_000).
				GroupBy(ursus.Col("tag")).
				Agg(ursus.Col("qty").Sum().Alias("s"), ursus.Len().Alias("n"))
		}},
		{"first and last are positional", func() *ursus.LazyFrame {
			return parFrame(20_000).
				GroupBy(ursus.Col("tag")).
				Agg(ursus.Col("id").First().Alias("f"), ursus.Col("id").Last().Alias("l"))
		}},
		{"sort is stable, so ties keep input order", func() *ursus.LazyFrame {
			return parFrame(5_000).Sort(ursus.Asc(ursus.Col("tag"))).Head(50)
		}},
		{"join — left-input order is a guarantee", func() *ursus.LazyFrame {
			return parFrame(5_000).
				Join(parFrame(200), ursus.JoinOn(ursus.Col("id"))).
				Select(ursus.Col("id"), ursus.Col("qty"))
		}},
		{"filtered to nothing", func() *ursus.LazyFrame {
			return parFrame(20_000).Filter(ursus.Col("qty").Gt(1000))
		}},
		{"empty input", func() *ursus.LazyFrame {
			return parFrame(0).Filter(ursus.Col("qty").Gt(1))
		}},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			// AssertFrameEqual, NOT df.String(). String stops at ten rows, so the
			// join case below — which joins 5000 rows against 200 — was checking
			// about five percent of its output and calling the order a guarantee.
			var want *ursus.DataFrame
			for _, n := range []int{1, 2, 3, 4, 8, 16} {
				df, err := q.lf().Collect(t.Context(),
					ursus.WithThreads(n), ursus.WithBatchSize(512), ursus.WithVerify())
				if err != nil {
					t.Fatalf("threads=%d: %v", n, err)
				}
				if want == nil {
					want = df
					continue
				}
				t.Run("threads "+itoa64(int64(n)), func(t *testing.T) {
					ursustest.AssertFrameEqual(t, df, want)
				})
			}
		})
	}
}

// TestParallelBatchSizeAndThreadsAreIndependent varies both knobs together. Either
// one alone can hide a bug in the other: at one batch there is nothing to
// distribute, and at one thread there is nothing to reorder.
func TestParallelBatchSizeAndThreadsAreIndependent(t *testing.T) {
	build := func() *ursus.LazyFrame {
		return parFrame(3_000).
			Filter(ursus.Col("qty").Gt(10)).
			WithColumns(ursus.Col("id").Add(1).Alias("next"))
	}

	var want *ursus.DataFrame
	for _, bs := range []int{1, 7, 64, 512, 8192} {
		for _, th := range []int{1, 2, 5, 8} {
			df, err := build().Collect(t.Context(),
				ursus.WithBatchSize(bs), ursus.WithThreads(th), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch=%d threads=%d: %v", bs, th, err)
			}
			if want == nil {
				want = df
				continue
			}
			t.Run("batch "+itoa64(int64(bs))+" threads "+itoa64(int64(th)),
				func(t *testing.T) { ursustest.AssertFrameEqual(t, df, want) })
		}
	}
}

// TestParallelPropagatesErrors: an error raised inside a worker must reach the
// caller, and must not be overtaken by results already in flight.
func TestParallelPropagatesErrors(t *testing.T) {
	// A strict cast that fails partway through the data.
	lf := parFrame(10_000).
		Filter(ursus.Col("qty").Gt(0)).
		Select(ursus.Col("tag").Cast(ursus.Int64).Alias("bad"))

	for _, n := range []int{1, 4, 8} {
		_, err := lf.Collect(t.Context(), ursus.WithThreads(n), ursus.WithBatchSize(256))
		if err == nil {
			t.Errorf("threads=%d: expected the cast to fail", n)
		}
	}
}

// TestParallelRespectsCancellation: a cancelled context must stop the workers and
// return, not hang on a full queue.
func TestParallelRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := parFrame(50_000).Filter(ursus.Col("qty").Gt(1)).
		Collect(ctx, ursus.WithThreads(8), ursus.WithBatchSize(64))
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestParallelEarlyBreakDoesNotLeak: CollectBatches' consumer may stop early, which
// tears the tree down mid-flight. Workers blocked on a full queue must wake on the
// cancellation rather than leaking.
//
// The goroutine count is the assertion. Without the cancel-then-wait in
// parallelOp.Close, this test leaves workers parked forever.
func TestParallelEarlyBreakDoesNotLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	for range 20 {
		for df, err := range parFrame(50_000).Filter(ursus.Col("qty").Gt(1)).
			CollectBatches(t.Context(), ursus.WithThreads(8), ursus.WithBatchSize(64)) {
			if err != nil {
				t.Fatal(err)
			}
			_ = df
			break // stop after the first batch, every time
		}
	}

	// Give any straggler a chance to be scheduled before counting.
	for range 100 {
		if runtime.NumGoroutine() <= before+2 {
			break
		}
		runtime.Gosched()
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines leaked: %d before, %d after 20 early breaks", before, after)
	}
}

// TestParallelConcurrentCollects runs whole queries from several goroutines at
// once. Under -race this is what finally gives the race detector something to
// detect: before step 5 the library started no goroutines at all, so `make race`
// was a gate with nothing behind it.
func TestParallelConcurrentCollects(t *testing.T) {
	src := parFrame(5_000)

	var wg sync.WaitGroup
	results := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			df, err := src.
				Filter(ursus.Col("qty").Gt(20)).
				GroupBy(ursus.Col("tag")).
				Agg(ursus.Col("qty").Sum().Alias("s")).
				Collect(t.Context(), ursus.WithThreads(4), ursus.WithBatchSize(128))
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = df.String()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	// Every concurrent run must agree, which also re-checks order preservation
	// under real contention rather than under a scheduler that happens to be tidy.
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Errorf("goroutine %d disagreed:\n%s\nvs\n%s", i, results[i], results[0])
		}
	}
}

// TestParallelDoesNotTouchBreakers documents the scope boundary as a test: a query
// that is nothing but a pipeline breaker still works, and still gives the same
// answer, at every thread count.
func TestParallelDoesNotTouchBreakers(t *testing.T) {
	var want *ursus.DataFrame
	for _, n := range []int{1, 4, 8} {
		df, err := parFrame(2_000).
			Sort(ursus.Desc(ursus.Col("qty"))).
			Unique("tag").
			Collect(t.Context(), ursus.WithThreads(n), ursus.WithBatchSize(97), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = df
			continue
		}
		t.Run("threads "+itoa64(int64(n)),
			func(t *testing.T) { ursustest.AssertFrameEqual(t, df, want) })
	}
}
