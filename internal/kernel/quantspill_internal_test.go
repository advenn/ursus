package kernel

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// fakeSpill is a ValueSpill in memory. It checks each run is in order as it ends,
// and gives the values back by sorting them all.
type fakeSpill struct {
	t    *testing.T
	runs [][]spilledValue
	open []spilledValue
}

type spilledValue struct {
	g int32
	v float64
}

func compareSpilled(a, b spilledValue) int {
	if a.g != b.g {
		return int(a.g) - int(b.g)
	}
	return compareTotalF64(a.v, b.v)
}

func (f *fakeSpill) Put(gids []int32, vals []float64) error {
	for i, g := range gids {
		f.open = append(f.open, spilledValue{g, vals[i]})
	}
	return nil
}

func (f *fakeSpill) EndRun() error {
	if len(f.open) == 0 {
		f.t.Error("a run ended with nothing in it")
	}
	if !slices.IsSortedFunc(f.open, compareSpilled) {
		f.t.Errorf("run %d is not in order of group and value", len(f.runs))
	}
	f.runs = append(f.runs, f.open)
	f.open = nil
	return nil
}

func (f *fakeSpill) Ordered(fn func(gid int32, v float64) error) error {
	if len(f.open) > 0 {
		f.t.Error("read back with a run still open")
	}
	all := slices.Concat(f.runs...)
	slices.SortFunc(all, compareSpilled)
	for _, x := range all {
		if err := fn(x.g, x.v); err != nil {
			return err
		}
	}
	return nil
}

func newQuantile(t *testing.T, q float64, interp expr.Interpolation) *quantileAcc {
	t.Helper()
	bind, err := expr.ResolveAggBinding(expr.AggQuantile, dtype.Float64)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAccumulator(expr.AggQuantile, dtype.Float64, bind,
		expr.AggParams{Q: q, Interp: interp})
	if err != nil {
		t.Fatal(err)
	}
	return a.(*quantileAcc)
}

// TestAQuantilesSpilledValuesGiveItsAnswer: a quantile whose values went to a spill,
// at random points between batches, answers as one that held them (step 220). Over
// every interpolation and six quantiles; NaN, ±0, ±Inf and repeats among the values;
// nulls; one group with half the rows; and groups that saw no value. Every run is in
// order, and a spill lets every value go.
func TestAQuantilesSpilledValuesGiveItsAnswer(t *testing.T) {
	pool := []float64{math.NaN(), 0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1), 1, 1, 2, -3}
	const nGroups = 45 // groups 40 to 44 see no value
	for interp := expr.InterpLinear; interp.String() != "?"; interp++ {
		for _, q := range []float64{0, 0.1, 0.37, 0.5, 0.9, 1} {
			rng := rand.New(rand.NewPCG(220, uint64(interp)*10+uint64(q*100)))
			held, spilled := newQuantile(t, q, interp), newQuantile(t, q, interp)
			sp := &fakeSpill{t: t}
			for range 60 {
				n := 1 + rng.IntN(50)
				groups := make([]int32, n)
				vals := make([]float64, n)
				ok := bitmap.NewBuilder(n)
				for i := range n {
					groups[i] = int32(rng.IntN(40))
					if rng.IntN(2) == 0 {
						groups[i] = 7 // the hot group
					}
					if rng.IntN(3) == 0 {
						vals[i] = pool[rng.IntN(len(pool))]
					} else {
						vals[i] = rng.NormFloat64() * 100
					}
					ok.Append(rng.IntN(10) != 0)
				}
				valid := ok.Finish()
				for _, a := range []*quantileAcc{held, spilled} {
					a.Reserve(40)
					if err := a.AddBatch(groups, data.NewFixed("v", dtype.Float64,
						vals, valid)); err != nil {
						t.Fatal(err)
					}
				}
				if rng.IntN(3) != 0 {
					continue
				}
				had := false
				for _, v := range spilled.vals {
					had = had || len(v) > 0
				}
				got, err := spilled.SpillValues(sp)
				if err != nil {
					t.Fatal(err)
				}
				if got != had {
					t.Fatalf("SpillValues said %v of a quantile that held values: %v", got, had)
				}
				for g, v := range spilled.vals {
					if v != nil {
						t.Fatalf("group %d kept %d values past a spill", g, len(v))
					}
				}
			}
			if len(sp.runs) == 0 {
				t.Fatal("nothing spilled, so nothing was tested")
			}
			want, err := held.Finish("q", nGroups)
			if err != nil {
				t.Fatal(err)
			}
			got, err := spilled.Finish("q", nGroups)
			if err != nil {
				t.Fatal(err)
			}
			wv, gv := data.MustValues[float64](want), data.MustValues[float64](got)
			for g := range nGroups {
				wok, gok := want.Validity().Get(g), got.Validity().Get(g)
				if wok != gok || (wok && !sameFloat(wv[g], gv[g])) {
					t.Fatalf("%v q=%v: group %d is %v (valid %v), want %v (valid %v)",
						interp, q, g, gv[g], gok, wv[g], wok)
				}
			}
		}
	}
}

// TestASpilledQuantileIsNotMerged: a quantile whose values spilled refuses a merge,
// either way, and a second spill: its runs are in one place.
func TestASpilledQuantileIsNotMerged(t *testing.T) {
	a, b := newQuantile(t, 0.5, expr.InterpLinear), newQuantile(t, 0.5, expr.InterpLinear)
	for _, x := range []*quantileAcc{a, b} {
		x.Reserve(1)
		if err := x.AddBatch([]int32{0}, data.NewFixed("v", dtype.Float64,
			[]float64{1}, bitmap.View{})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.SpillValues(&fakeSpill{t: t}); err != nil {
		t.Fatal(err)
	}
	if err := a.Merge(b, []int32{0}); err == nil {
		t.Error("a spilled quantile took a merge")
	}
	if err := b.Merge(a, []int32{0}); err == nil {
		t.Error("a quantile merged one that spilled")
	}
	if _, err := a.SpillValues(&fakeSpill{t: t}); err == nil {
		t.Error("a quantile spilled to a second place")
	}
}
