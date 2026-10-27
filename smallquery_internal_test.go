package ursus

import (
	"runtime"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/plan"
)

// TestASmallQueryRunsOnOneThread: a query given no WithThreads whose every input
// fits in a batch, by the sources' own counts, is planned on one thread (step 396,
// B1 of v0.8-scope.md); a larger one, one reading a source of no count, and one given
// WithThreads, on its threads.
func TestASmallQueryRunsOnOneThread(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("one CPU: every query runs on one thread")
	}
	threads := func(lf *LazyFrame, opts ...CollectOption) int {
		t.Helper()
		cfg := newCollectCfg(opts)
		n, err := plan.Resolve(t.Context(), lf.node)
		if err != nil {
			t.Fatal(err)
		}
		// As compile plans it: after the optimizer, which hints a Head's rows to its scan.
		if n, err = optimizer().Run(n, cfg.flags); err != nil {
			t.Fatal(err)
		}
		return cfg.planThreads(n)
	}
	rows := func(n int) *LazyFrame { return Frame(Values("k", make([]int64, n))) }
	all := DefaultEngine().threads
	var csv strings.Builder
	csv.WriteString("k\n1\n2\n")
	for _, c := range []struct {
		name string
		lf   *LazyFrame
		opts []CollectOption
		want int
	}{
		{"a batch", rows(8192), nil, 1},
		{"a batch and a row", rows(8193), nil, all},
		{"a join of two batches' worth", rows(8192).Join(rows(100), JoinOn(Col("k"))), nil, 1},
		{"a join to a larger side", rows(100).Join(rows(10_000), JoinOn(Col("k"))), nil, all},
		{"a head of a larger frame", rows(100_000).Head(10), nil, 1},
		{"a batch given WithThreads", rows(100), []CollectOption{WithThreads(4)}, 4},
		{"a batch of 1,024 rows' worth", rows(2000), []CollectOption{WithBatchSize(1024)}, all},
		{"CSV, of no count", ScanCSVReader([]byte(csv.String()), "k.csv"), nil, all},
	} {
		if got := threads(c.lf, c.opts...); got != c.want {
			t.Errorf("%s: planned on %d threads, want %d", c.name, got, c.want)
		}
	}
}
