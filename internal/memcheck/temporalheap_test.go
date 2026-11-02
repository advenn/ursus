package memcheck_test

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/physical"
)

// TestTemporalMediansHoldTheirValuesOnDisk (step 431): a rolling median's windows
// keep every value, one a (window, row) pair, and past the limit hand them to disk
// (step 244). Read where each segment of windows is about to finish, after two full
// collections, the live heap holds the values without a limit, 7.7 MB at step 431,
// and under half of that under one, 1.7 MB. The root package's test sampled the heap
// instead, whose floating garbage swelled under load: its bound of 10 MB passed both
// readings, and flaked.
func TestTemporalMediansHoldTheirValuesOnDisk(t *testing.T) {
	const n = 2000
	ts := make([]time.Time, n)
	v := make([]float64, n)
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		ts[i] = t0.Add(time.Duration(i) * time.Minute)
		v[i] = float64(i%97) + 0.5
	}
	df, err := ursus.Frame(ursus.Values("ts", ts), ursus.Values("v", v)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	q := df.Lazy().Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
		Agg(ursus.Col("v").Median().Alias("m"))
	held := func(opts ...ursus.CollectOption) int64 {
		runtime.GC()
		runtime.GC()
		base := liveNow()
		var most uint64
		physical.TemporalSegmentFinishing = func() {
			runtime.GC()
			runtime.GC()
			most = max(most, liveNow())
		}
		defer func() { physical.TemporalSegmentFinishing = nil }()
		opts = append(opts, ursus.WithBatchSize(256), ursus.WithThreads(1))
		for _, err := range q.CollectBatches(t.Context(), opts...) {
			if err != nil {
				t.Fatal(err)
			}
		}
		runtime.KeepAlive(df)
		return max(0, int64(most)-int64(base))
	}
	in := held(ursus.WithMemoryLimit(0))
	var st ursus.MemoryStats
	dir := t.TempDir()
	out := held(ursus.WithMemoryLimit(2<<20), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&st))
	if os.Getenv("URSUS_MEMCHECK_VERBOSE") != "" || testing.Verbose() {
		t.Logf("held at a segment's finish: %.1f MB in memory, %.1f MB spilling, %d spills",
			float64(in)/mb, float64(out)/mb, st.Spills)
	}
	if st.Spills == 0 {
		t.Fatal("nothing spilled under 2 MB")
	}
	// Two million values are 16 MB; held, at least a quarter are reachable at a
	// segment's finish, 7.7 MB at step 431, or the reading missed them.
	if in < 4*mb {
		t.Fatalf("held %.1f MB at a segment's finish without a limit: the reading is not where the values are", float64(in)/mb)
	}
	if out*2 > in {
		t.Errorf("held %.1f MB at a segment's finish spilling, against %.1f MB in memory", float64(out)/mb, float64(in)/mb)
	}
}
