package ursus_test

// A rolling or dynamic group-by's expansion is bounded, and what it must hold is
// charged to the budget.
//
// Rolling and GroupByDynamic aggregate overlapping windows by expanding them: one
// (window, row) pair per membership, then a copy of every input column gathered by
// those pairs, then each aggregate's input evaluated over the copy. A 3,000-row
// rolling window over 3,000 rows is 4.5 million pairs, and the whole expansion was
// built at once and never charged: the budget saw the input and the window grid,
// and the process held a hundred times more. Under a container's limit that is a
// kill rather than a refusal, the exposure step 90 removed everywhere else.

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// minuteSeries is n rows a minute apart, with a value column and a two-way key.
func minuteSeries(n int) *ursus.LazyFrame {
	ts := make([]time.Time, n)
	v := make([]float64, n)
	k := make([]string, n)
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		ts[i] = t0.Add(time.Duration(i) * time.Minute)
		v[i] = float64(i%97) + 0.5
		k[i] = []string{"a", "b"}[i%2]
	}
	return ursus.Frame(ursus.Values("ts", ts), ursus.Values("v", v), ursus.Values("k", k))
}

// peakHeapGrowth is the most the live heap grew, above where it started, while f
// ran: the heap each collection found held, collecting often while measuring. The
// heap's objects, which count garbage not yet swept, grew past it on a loaded
// machine, whose collector lagged.
func peakHeapGrowth(t *testing.T, f func()) int64 {
	t.Helper()
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	read := func() int64 { metrics.Read(sample); return int64(sample[0].Value.Uint64()) }
	base := read()
	stop, done := make(chan struct{}), make(chan int64)
	go func() {
		peak := base
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				done <- peak
				return
			case <-tick.C:
				peak = max(peak, read())
			}
		}
	}()
	f()
	close(stop)
	return <-done - base
}

// TestRollingExpansionIsBounded: a rolling sum whose windows overlap almost
// entirely holds a bounded working set, not one proportional to the overlap.
func TestRollingExpansionIsBounded(t *testing.T) {
	const n = 4000 // 8 million (window, row) pairs
	var rows int
	grown := peakHeapGrowth(t, func() {
		df, err := minuteSeries(n).
			Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
			Agg(ursus.Col("v").Sum().Alias("s")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		rows = df.Height()
	})
	if rows != n {
		t.Fatalf("%d rows, want %d", rows, n)
	}
	// The pairs alone are 64 MB, and the gathered copy twice that again.
	if grown > 48<<20 {
		t.Errorf("the live heap grew by %d MB for a %d-row rolling sum", grown>>20, n)
	}
}

// TestRollingStateIsCharged: an aggregate that keeps every value holds one per
// (window, row) pair, so overlapping windows under a limit grow many times their
// input. A list is its answer, so it is refused, naming the operator, instead of
// growing past the limit. A median's, a quantile's and n_unique's values spill since
// step 244: TestTemporalMediansSpillTheirValues.
func TestRollingStateIsCharged(t *testing.T) {
	const n = 4000
	for _, tc := range []struct {
		name string
		lf   func() *ursus.LazyFrame
		op   string
	}{
		{"rolling implode", func() *ursus.LazyFrame {
			return minuteSeries(n).
				Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
				Agg(ursus.Col("v").Implode().Alias("vs"))
		}, "rolling"},
		{"dynamic implode, period far past every", func() *ursus.LazyFrame {
			return minuteSeries(n).
				GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
					Every: ursus.Every("1m"), Period: ursus.Every("7d")}).
				Agg(ursus.Col("v").Implode().Alias("vs"))
		}, "group_by_dynamic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			grown := peakHeapGrowth(t, func() {
				_, err = tc.lf().Collect(t.Context(), ursus.WithMemoryLimit(16<<20))
			})
			if err == nil {
				t.Fatal("8 million values held under a 16 MB limit, with no error")
			}
			if !errors.Is(err, ursus.ErrResource) || !strings.Contains(err.Error(), tc.op) {
				t.Errorf("want a memory refusal naming %s, got: %v", tc.op, err)
			}
			// Refused as the state grows, not once it has all been built: the whole
			// of it is 64 MB of values before the slices' spare capacity.
			if grown > 48<<20 {
				t.Errorf("the live heap grew by %d MB before a 16 MB limit refused", grown>>20)
			}
		})
	}

	t.Run("control: a sum's state is per window, and runs under the same limit", func(t *testing.T) {
		df, err := minuteSeries(n).
			Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
			Agg(ursus.Col("v").Sum().Alias("s")).
			Collect(t.Context(), ursus.WithMemoryLimit(16<<20))
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != n {
			t.Fatalf("%d rows, want %d", df.Height(), n)
		}
	})
}

// TestTemporalMediansSpillTheirValues: step 244. A median's windows keep every
// value, one a (window, row) pair, so windows that overlap hold many times their
// input. Past the limit they hand their values to disk, as a group-by's do, where
// they refused. Over 2,000 rows a minute apart whose windows reach back a week, two
// million values: a rolling median, a dynamic one whose period is far past its
// every, and a rolling quantile and n_unique by key. Under 2 MB, at batches of 256
// rows, so that eight segments of windows, none closed before the end, spill side
// by side, the answer is the one without a limit, it spills, it peaks at most twice
// the limit, the live heap grows by under 10 MB of the 16 MB the values are, and the
// spill directory is empty after. Beside an implode, the median spills and the
// implode refuses, and the directory is empty after the error too.
func TestTemporalMediansSpillTheirValues(t *testing.T) {
	const n = 2000
	c := ursus.Col
	cases := []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"rolling median", minuteSeries(n).
			Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
			Agg(c("v").Median().Alias("m"))},
		{"dynamic median, period far past every", minuteSeries(n).
			GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("1m"), Period: ursus.Every("7d")}).
			Agg(c("v").Median().Alias("m"))},
		{"rolling quantile and n_unique by key", minuteSeries(n).
			Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("7d"), GroupBy: []ursus.Expr{c("k")}}).
			Agg(c("v").Quantile(0.25, ursus.InterpLinear).Alias("q"), c("v").NUnique().Alias("u"))},
	}
	if raceDetector {
		// The root package's race run is near its limit: the keyed case, which
		// spills two aggregates, drives the spill and the segments alone.
		cases = cases[2:]
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := tc.lf.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var got *ursus.DataFrame
			var st ursus.MemoryStats
			dir := t.TempDir()
			grown := peakHeapGrowth(t, func() {
				got, err = tc.lf.Collect(t.Context(), ursus.WithBatchSize(256), ursus.WithMemoryLimit(2<<20),
					ursus.WithSpillDir(dir), ursus.WithMemoryStats(&st))
			})
			if err != nil {
				t.Fatalf("under 2 MB: %v", err)
			}
			if st.Spills == 0 {
				t.Error("nothing spilled")
			}
			if st.Peak > 2*(2<<20) {
				t.Errorf("a peak of %d KB under a 2 MB limit", st.Peak>>10)
			}
			if grown > 10<<20 {
				t.Errorf("the live heap grew by %d MB under a 2 MB limit", grown>>20)
			}
			ursustest.AssertFrameEqual(t, got, want)
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("%d entries left in the spill directory", len(left))
			}
		})
	}

	dir := t.TempDir()
	var st ursus.MemoryStats
	_, err := minuteSeries(n).Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
		Agg(c("v").Median().Alias("m"), c("v").Implode().Alias("vs")).
		Collect(t.Context(), ursus.WithBatchSize(256), ursus.WithMemoryLimit(2<<20), ursus.WithSpillDir(dir),
			ursus.WithMemoryStats(&st))
	if !errors.Is(err, ursus.ErrResource) {
		t.Errorf("a median beside an implode under 2 MB: want a memory refusal, got %v", err)
	}
	if st.Spills == 0 {
		t.Error("a median beside an implode refused before the median spilled")
	}
	if left, _ := os.ReadDir(dir); len(left) > 0 {
		t.Errorf("%d entries left in the spill directory after the refusal", len(left))
	}
}

// TestRollingAnswersDoNotDependOnTheChunk: windows are expanded a chunk at a time,
// one chunk per batch size, so a window can straddle chunks. Every aggregate,
// order-dependent ones included, answers the same whatever the batch size.
func TestRollingAnswersDoNotDependOnTheChunk(t *testing.T) {
	c := ursus.Col
	aggs := []ursus.Expr{
		ursus.Len().Alias("n"),
		c("v").Sum().Alias("sum"), c("v").Mean().Alias("mean"),
		c("v").Min().Alias("min"), c("v").Max().Alias("max"),
		c("v").First().Alias("first"), c("v").Last().Alias("last"),
		c("v").Median().Alias("median"), c("v").Implode().Alias("vs"),
	}
	queries := map[string]func() *ursus.LazyFrame{
		"rolling": func() *ursus.LazyFrame {
			return minuteSeries(300).
				Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("45m")}).Agg(aggs...)
		},
		"rolling by key": func() *ursus.LazyFrame {
			return minuteSeries(300).
				Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("45m"), GroupBy: []ursus.Expr{c("k")}}).
				Agg(aggs...)
		},
		"dynamic, overlapping": func() *ursus.LazyFrame {
			return minuteSeries(300).
				GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("10m"), Period: ursus.Every("1h")}).
				Agg(aggs...)
		},
	}
	for name, q := range queries {
		want, err := q().Collect(t.Context(), ursus.WithBatchSize(1<<20))
		if err != nil {
			t.Fatal(err)
		}
		for _, bs := range []int{1, 7, 64, 1000} {
			t.Run(fmt.Sprintf("%s, batch %d", name, bs), func(t *testing.T) {
				got, err := q().Collect(t.Context(), ursus.WithBatchSize(bs))
				if err != nil {
					t.Fatal(err)
				}
				ursustest.AssertFrameEqual(t, got, want)
			})
		}
	}
}

// TestDynamicWindowStateIsCharged: what Finish keeps for every window — its grid
// slot, its start and bucket, and each aggregate's state — is charged, so the
// budget's peak is at least their sum. Two rows four hours apart at a one-second
// grid are 14,400 windows and almost nothing else.
//
// Under a key, the same in both rows: an unkeyed group hands its windows out as
// they close since step 207, and holds them no longer.
func TestDynamicWindowStateIsCharged(t *testing.T) {
	lo := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var stats ursus.MemoryStats
	df, err := ursus.Frame(ursus.Values("ts", []time.Time{lo, lo.Add(4*time.Hour - time.Second)}),
		ursus.Values("k", []string{"a", "a"})).
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1s"),
			GroupBy: []ursus.Expr{ursus.Col("k")}}).
		Agg(ursus.Len().Alias("n")).
		Collect(t.Context(), ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	const windows = 4 * 3600
	if df.Height() != windows {
		t.Fatalf("%d windows, want %d", df.Height(), windows)
	}
	// 24 bytes of grid, 16 of start and bucket, 8 of count, per window.
	if want := int64(windows * (24 + 16 + 8)); stats.Peak < want {
		t.Errorf("the budget's peak is %d bytes, want at least %d for %d windows",
			stats.Peak, want, windows)
	}
}
