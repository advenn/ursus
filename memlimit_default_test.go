package ursus_test

// A query with no WithMemoryLimit is budgeted by default (step 90): spilling is
// decided by the budget, and with none a query under a cgroup's limit grew until the
// kernel killed it, as the v0.3.0 benchmark's h2o gb10 over CSV was.

import (
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/execopt"
)

func TestTheMemoryBudgetHasADefault(t *testing.T) {
	lf := ursus.Frame(ursus.Values("k", []int64{3, 1, 2})).Sort(ursus.Asc(ursus.Col("k")))
	limitOf := func(opts ...ursus.CollectOption) int64 {
		var stats ursus.MemoryStats
		if _, err := lf.Collect(t.Context(), append(opts, ursus.WithMemoryStats(&stats))...); err != nil {
			t.Fatal(err)
		}
		return stats.Limit
	}
	if got, want := limitOf(), execopt.DefaultLimit(); got != want {
		t.Errorf("no WithMemoryLimit: limit %d, want the default %d", got, want)
	}
	if got := limitOf(ursus.WithMemoryLimit(0)); got != 0 {
		t.Errorf("WithMemoryLimit(0): limit %d, want 0, unlimited", got)
	}
	if got := limitOf(ursus.WithMemoryLimit(64 << 20)); got != 64<<20 {
		t.Errorf("WithMemoryLimit(64 MiB): limit %d", got)
	}
	// On Linux the default comes from the machine, and is never zero there.
	if execopt.DefaultLimit() <= 0 {
		t.Logf("no default detected on this machine")
	}
}
