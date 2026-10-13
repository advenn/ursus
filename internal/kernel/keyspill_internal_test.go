package kernel

import (
	"bytes"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// fakeKeySpill is a KeySpill in memory. It checks each run is in order as it ends,
// and gives the keys back by sorting them all.
type fakeKeySpill struct {
	t    *testing.T
	runs [][]spilledKey
	open []spilledKey
}

type spilledKey struct {
	g int32
	k string
}

func compareSpilledKey(a, b spilledKey) int {
	if a.g != b.g {
		return int(a.g) - int(b.g)
	}
	return bytes.Compare([]byte(a.k), []byte(b.k))
}

func (f *fakeKeySpill) Put(gids []int32, keys [][]byte) error {
	for i, g := range gids {
		f.open = append(f.open, spilledKey{g, string(keys[i])})
	}
	return nil
}

func (f *fakeKeySpill) EndRun() error {
	if len(f.open) == 0 {
		f.t.Error("a run ended with nothing in it")
	}
	if !slices.IsSortedFunc(f.open, compareSpilledKey) {
		f.t.Errorf("run %d is not in order of group and key", len(f.runs))
	}
	f.runs = append(f.runs, f.open)
	f.open = nil
	return nil
}

func (f *fakeKeySpill) Ordered(fn func(gid int32, key []byte) error) error {
	all := slices.Concat(f.runs...)
	slices.SortFunc(all, compareSpilledKey)
	for _, x := range all {
		if err := fn(x.g, []byte(x.k)); err != nil {
			return err
		}
	}
	return nil
}

func newNUnique(t *testing.T, in dtype.DataType) *nuniqueAcc {
	t.Helper()
	bind, err := expr.ResolveAggBinding(expr.AggNUnique, in)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAccumulator(expr.AggNUnique, in, bind, expr.AggParams{})
	if err != nil {
		t.Fatal(err)
	}
	return a.(*nuniqueAcc)
}

// TestAnNUniquesSpilledKeysGiveItsCount: an n_unique whose keys went to a spill at
// random points between batches counts as one that held them (step 241), over floats
// with NaN, ±0 and repeats that recur across spills, with nulls, one group with half
// the rows, and groups that saw no value. Every run is in order, and a spill lets
// every key go. A spilled n_unique refuses a merge either way.
func TestAnNUniquesSpilledKeysGiveItsCount(t *testing.T) {
	rng := rand.New(rand.NewPCG(241, 1))
	pool := []float64{math.NaN(), 0, math.Copysign(0, -1), 1, 2, 3}
	held, spilled := newNUnique(t, dtype.Float64), newNUnique(t, dtype.Float64)
	sp := &fakeKeySpill{t: t}
	for range 80 {
		n := 1 + rng.IntN(60)
		groups, vals := make([]int32, n), make([]float64, n)
		ok := bitmap.NewBuilder(n)
		for i := range n {
			groups[i] = int32(rng.IntN(30))
			if rng.IntN(2) == 0 {
				groups[i] = 4
			}
			vals[i] = pool[rng.IntN(len(pool))]
			if rng.IntN(3) == 0 {
				vals[i] = float64(rng.IntN(200))
			}
			ok.Append(rng.IntN(8) != 0)
		}
		valid := ok.Finish()
		for _, a := range []*nuniqueAcc{held, spilled} {
			a.Reserve(30)
			if err := a.AddBatch(groups, data.NewFixed("v", dtype.Float64, vals, valid)); err != nil {
				t.Fatal(err)
			}
		}
		if rng.IntN(3) != 0 {
			continue
		}
		had := spilled.tab.Len() > 0
		got, err := spilled.SpillKeys(sp)
		if err != nil {
			t.Fatal(err)
		}
		if got != had || spilled.tab.Len() != 0 {
			t.Fatalf("SpillKeys said %v holding keys %v, and kept %d", got, had, spilled.tab.Len())
		}
	}
	if len(sp.runs) < 2 {
		t.Fatal("fewer than two runs, so the merge of runs is not tested")
	}
	const nGroups = 35 // 30 to 34 saw no value
	want, err := held.Finish("n", nGroups)
	if err != nil {
		t.Fatal(err)
	}
	got, err := spilled.Finish("n", nGroups)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(data.MustValues[uint64](got), data.MustValues[uint64](want)) {
		t.Fatalf("spilled counts %v, held %v", data.MustValues[uint64](got), data.MustValues[uint64](want))
	}

	other := newNUnique(t, dtype.Float64)
	if err := spilled.Merge(other, nil); err == nil {
		t.Error("a spilled n_unique took a merge")
	}
	if err := other.Merge(spilled, nil); err == nil {
		t.Error("an n_unique merged one that spilled")
	}
}
