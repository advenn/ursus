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
// ran. It collects often while measuring, so what it sees is held rather than
// merely not yet collected.
func peakHeapGrowth(t *testing.T, f func()) int64 {
	t.Helper()
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
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

// TestRollingStateIsCharged: an aggregate that keeps every value — a median, a
// list — holds one per (window, row) pair, so overlapping windows under a limit are
// refused, naming the operator, instead of growing past it.
func TestRollingStateIsCharged(t *testing.T) {
	const n = 4000
	for _, tc := range []struct {
		name string
		lf   func() *ursus.LazyFrame
		op   string
	}{
		{"rolling median", func() *ursus.LazyFrame {
			return minuteSeries(n).
				Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
				Agg(ursus.Col("v").Median().Alias("m"))
		}, "rolling"},
		{"rolling implode", func() *ursus.LazyFrame {
			return minuteSeries(n).
				Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
				Agg(ursus.Col("v").Implode().Alias("vs"))
		}, "rolling"},
		{"dynamic median, period far past every", func() *ursus.LazyFrame {
			return minuteSeries(n).
				GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
					Every: ursus.Every("1m"), Period: ursus.Every("7d")}).
				Agg(ursus.Col("v").Median().Alias("m"))
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
func TestDynamicWindowStateIsCharged(t *testing.T) {
	lo := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var stats ursus.MemoryStats
	df, err := ursus.Frame(ursus.Values("ts", []time.Time{lo, lo.Add(4*time.Hour - time.Second)})).
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1s")}).
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
