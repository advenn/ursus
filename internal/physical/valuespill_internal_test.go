package physical

import (
	"cmp"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/kernel"
)

type gv struct {
	g int32
	v float64
}

func compareGV(a, b gv) int {
	return cmp.Or(cmp.Compare(a.g, b.g),
		cmp.Compare(kernel.OrderKeyF64(a.v), kernel.OrderKeyF64(b.v)))
}

// spillRuns puts n runs of up to 40 values each, each in order, and returns them all.
func spillRuns(t *testing.T, s *valueSpill, rng *rand.Rand, n int) []gv {
	t.Helper()
	pool := []float64{math.NaN(), 0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1), 1, 2}
	var all []gv
	for range n {
		run := make([]gv, 1+rng.IntN(40))
		for i := range run {
			run[i] = gv{int32(rng.IntN(20)), rng.NormFloat64()}
			if rng.IntN(4) == 0 {
				run[i].v = pool[rng.IntN(len(pool))]
			}
		}
		slices.SortFunc(run, compareGV)
		// Two puts, as the kernel puts a run a chunk at a time.
		for _, part := range [][]gv{run[:len(run)/2], run[len(run)/2:]} {
			gids, vals := make([]int32, len(part)), make([]float64, len(part))
			for i, x := range part {
				gids[i], vals[i] = x.g, x.v
			}
			if err := s.Put(gids, vals); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.EndRun(); err != nil {
			t.Fatal(err)
		}
		all = append(all, run...)
	}
	return all
}

func filesIn(t *testing.T, dir string) int {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

// TestAValueSpillMergesItsRunsAsTheyGather: past valueFanIn² runs, a value spill
// holds fewer than valueFanIn runs a level, and as many files as runs; it gives every
// value back in order of (group, value), the order a quantile reads, and leaves no
// file behind (step 220).
func TestAValueSpillMergesItsRunsAsTheyGather(t *testing.T) {
	dir := t.TempDir()
	s := &valueSpill{ctx: t.Context(), budget: execopt.NewBudget(0, dir), batch: 7,
		prefix: filepath.Join(dir, "v0-")}
	rng := rand.New(rand.NewPCG(220, 2))
	all := spillRuns(t, s, rng, valueFanIn*valueFanIn+5)

	if len(s.levels) < 3 {
		t.Fatalf("%d levels after %d runs: the runs were not merged", len(s.levels),
			valueFanIn*valueFanIn+5)
	}
	held := 0
	for l, runs := range s.levels {
		if len(runs) >= valueFanIn {
			t.Errorf("level %d holds %d runs", l, len(runs))
		}
		held += len(runs)
	}
	if got := filesIn(t, dir); got != held {
		t.Errorf("%d files for %d runs: a merged run was left behind", got, held)
	}

	slices.SortFunc(all, compareGV)
	var got []gv
	if err := s.Ordered(func(g int32, v float64) error {
		got = append(got, gv{g, v})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all) {
		t.Fatalf("%d values back, %d put", len(got), len(all))
	}
	for i := range all {
		if compareGV(got[i], all[i]) != 0 {
			t.Fatalf("value %d is %v, want %v", i, got[i], all[i])
		}
	}
	if n := filesIn(t, dir); n != 0 {
		t.Errorf("%d files left after the values were read back", n)
	}
}

// TestAClosedValueSpillLeavesNoFile: closed before its values are read back, with a
// run still open, a value spill removes every run.
func TestAClosedValueSpillLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	s := &valueSpill{ctx: t.Context(), budget: execopt.NewBudget(0, dir),
		prefix: filepath.Join(dir, "v0-")}
	spillRuns(t, s, rand.New(rand.NewPCG(220, 3)), valueFanIn+3)
	if err := s.Put([]int32{1}, []float64{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if n := filesIn(t, dir); n != 0 {
		t.Errorf("%d files left after close", n)
	}
}
