package kernel

import (
	"cmp"
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// fakePairSpill is a PairSpill in memory. It checks each run is in order as it ends,
// unless it came from Sorting, and gives the pairs back sorted, each tie in an order
// of its own, so nothing reads one.
type fakePairSpill struct {
	t       *testing.T
	rng     *rand.Rand
	sorting bool
	runs    [][]spilledPair
	open    []spilledPair
	next    *fakePairSpill
}

type spilledPair struct {
	g    int32
	a, b float64
}

func compareSpilledPair(x, y spilledPair) int {
	if c := cmp.Compare(x.g, y.g); c != 0 {
		return c
	}
	return cmp.Compare(OrderKeyF64(x.a), OrderKeyF64(y.a))
}

func (f *fakePairSpill) Put(gids []int32, a, b []float64) error {
	for i, g := range gids {
		f.open = append(f.open, spilledPair{g, a[i], b[i]})
	}
	return nil
}

func (f *fakePairSpill) EndRun() error {
	if len(f.open) == 0 {
		if !f.sorting {
			f.t.Error("a run ended with nothing in it")
		}
		return nil
	}
	if !f.sorting && !slices.IsSortedFunc(f.open, compareSpilledPair) {
		f.t.Errorf("run %d is not in order of group and its first", len(f.runs))
	}
	f.runs = append(f.runs, f.open)
	f.open = nil
	return nil
}

func (f *fakePairSpill) Ordered(fn func(gid int32, a, b float64) error) error {
	if len(f.open) > 0 {
		if err := f.EndRun(); err != nil {
			return err
		}
	}
	all := slices.Concat(f.runs...)
	f.rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	slices.SortStableFunc(all, compareSpilledPair)
	for _, p := range all {
		if err := fn(p.g, p.a, p.b); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakePairSpill) Sorting() (PairSpill, error) {
	f.next = &fakePairSpill{t: f.t, rng: f.rng, sorting: true}
	return f.next, nil
}

func newSpearman(t *testing.T) *spearmanAcc {
	t.Helper()
	in := dtype.Struct(dtype.Field{Name: expr.PairFirst, Type: dtype.Float64, Nullable: true},
		dtype.Field{Name: expr.PairSecond, Type: dtype.Float64, Nullable: true})
	bind, err := expr.ResolveAggBinding(expr.AggSpearman, in)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAccumulator(expr.AggSpearman, in, bind, expr.AggParams{})
	if err != nil {
		t.Fatal(err)
	}
	return a.(*spearmanAcc)
}

// TestASpearmansSpilledPairsGiveItsCorrelation: a Spearman whose pairs went to a
// spill at random points between batches answers as one that held them, to the bit
// (step 339): over values with NaN, ±0 and many ties, with nulls on either side, one
// group of most rows whose y has two values, so its ties pass the buffer and are
// numbered, groups of one pair and of none. Every run is in order, a spill lets every
// pair go, and a spilled Spearman refuses a merge either way.
func TestASpearmansSpilledPairsGiveItsCorrelation(t *testing.T) {
	rng := rand.New(rand.NewPCG(339, 1))
	pool := []float64{math.NaN(), math.Float64frombits(0x7ff8000000000001), 0, math.Copysign(0, -1), 1, 2, 3}
	held, spilled := newSpearman(t), newSpearman(t)
	sp := &fakePairSpill{t: t, rng: rng}
	const nGroups = 12 // 9 has one pair; 10 and 11 none
	var long [2]int    // group 4's pairs whose y is 0, and 1
	for range 170 {
		n := 1 + rng.IntN(400)
		groups, xs, ys := make([]int32, n), make([]float64, n), make([]float64, n)
		xok, yok := bitmap.NewBuilder(n), bitmap.NewBuilder(n)
		for i := range n {
			groups[i] = int32(rng.IntN(9))
			xs[i] = pool[rng.IntN(len(pool))]
			if rng.IntN(2) == 0 {
				xs[i] = float64(rng.IntN(300))
			}
			ys[i] = float64(rng.IntN(40)) + xs[i]/7
			if rng.IntN(4) != 0 {
				groups[i], ys[i] = 4, float64(rng.IntN(2))
			}
			xo, yo := rng.IntN(12) != 0, rng.IntN(12) != 0
			xok.Append(xo)
			yok.Append(yo)
			if groups[i] == 4 && xo && yo && (ys[i] == 0 || ys[i] == 1) {
				long[int(ys[i])]++
			}
		}
		pair := data.NewStruct("p", []*data.Column{
			data.NewFixed(expr.PairFirst, dtype.Float64, xs, xok.Finish()),
			data.NewFixed(expr.PairSecond, dtype.Float64, ys, yok.Finish())}, bitmap.AllSet(n))
		for _, a := range []*spearmanAcc{held, spilled} {
			a.Reserve(nGroups)
			if err := a.AddBatch(groups, pair); err != nil {
				t.Fatal(err)
			}
		}
		if rng.IntN(3) != 0 {
			continue
		}
		had := spilled.NBytes() > int64(cap(spilled.cnt))*8+48*int64(len(spilled.xs))
		got, err := spilled.SpillPairs(sp)
		if err != nil {
			t.Fatal(err)
		}
		if got != had {
			t.Fatalf("SpillPairs said %v holding pairs %v", got, had)
		}
		for g := range spilled.xs {
			if len(spilled.xs[g])+len(spilled.ys[g]) != 0 {
				t.Fatalf("group %d kept pairs past a spill", g)
			}
		}
	}
	for _, g := range []int32{9} {
		for _, a := range []*spearmanAcc{held, spilled} {
			pair := data.NewStruct("p", []*data.Column{
				data.NewFixed(expr.PairFirst, dtype.Float64, []float64{1}, bitmap.AllSet(1)),
				data.NewFixed(expr.PairSecond, dtype.Float64, []float64{2}, bitmap.AllSet(1))}, bitmap.AllSet(1))
			if err := a.AddBatch([]int32{g}, pair); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(sp.runs) < 2 {
		t.Fatal("fewer than two runs, so the merge of runs is not tested")
	}
	want, err := held.Finish("r", nGroups)
	if err != nil {
		t.Fatal(err)
	}
	got, err := spilled.Finish("r", nGroups)
	if err != nil {
		t.Fatal(err)
	}
	if min(long[0], long[1]) <= spillChunk {
		t.Fatalf("group 4's ties of y are %v, not both past the buffer", long)
	}
	if sp.next == nil || len(sp.next.runs) == 0 {
		t.Fatal("nothing went on to the spill by x")
	}
	w, g := data.MustValues[float64](want), data.MustValues[float64](got)
	for i := range w {
		if math.Float64bits(w[i]) != math.Float64bits(g[i]) && !(math.IsNaN(w[i]) && math.IsNaN(g[i])) {
			t.Errorf("group %d: spilled %v, held %v", i, g[i], w[i])
		}
	}
	for i := 9; i < nGroups; i++ {
		if !math.IsNaN(g[i]) {
			t.Errorf("group %d of fewer than two pairs: %v", i, g[i])
		}
	}
	if math.IsNaN(g[4]) || g[4] == 0 {
		t.Errorf("group 4's correlation %v tests nothing", g[4])
	}

	other := newSpearman(t)
	if err := spilled.Merge(other, nil); err == nil {
		t.Error("a spilled spearman took a merge")
	}
	if err := other.Merge(spilled, nil); err == nil {
		t.Error("a spearman merged one that spilled")
	}
}

// TestSpearmanIsPearsonsOfTheRanks: the exact sums give Pearson's correlation of the
// average ranks, as a float computation of it does to a few ulps.
func TestSpearmanIsPearsonsOfTheRanks(t *testing.T) {
	rng := rand.New(rand.NewPCG(339, 2))
	var rk, ranks ranker // one kept across the groups, as Finish keeps it
	for range 200 {
		n := 2 + rng.IntN(300)
		x, y := make([]float64, n), make([]float64, n)
		for i := range n {
			x[i] = float64(rng.IntN(1 + rng.IntN(50)))
			y[i] = float64(rng.IntN(1+rng.IntN(50))) - x[i]/3
		}
		if rng.IntN(2) == 0 {
			// Past radixRanks, ranked by radix.
			x = append(x, make([]float64, radixRanks)...)
			y = append(y, make([]float64, radixRanks)...)
			for i := n; i < len(x); i++ {
				x[i], y[i] = float64(rng.IntN(70))-0.5, float64(rng.IntN(9))
			}
			n = len(x)
		}
		rx, ry := ranks.doubled(x, nil), ranks.doubled(y, nil)
		var mx, my float64
		for i := range n {
			mx += float64(rx[i]) / 2
			my += float64(ry[i]) / 2
		}
		mx, my = mx/float64(n), my/float64(n)
		var sxy, sxx, syy float64
		for i := range n {
			dx, dy := float64(rx[i])/2-mx, float64(ry[i])/2-my
			sxy += dx * dy
			sxx += dx * dx
			syy += dy * dy
		}
		want, got := sxy/math.Sqrt(sxx*syy), rk.spearman(x, y)
		if math.IsNaN(want) != math.IsNaN(got) || math.Abs(got-want) > 1e-12 {
			t.Fatalf("%v and %v: %v, want %v", x, y, got, want)
		}
	}
	// A group of parallelRanks pairs or more ranks its y beside its x: the same sums.
	n := parallelRanks + 1000
	x, y := make([]float64, n), make([]float64, n)
	for i := range n {
		x[i], y[i] = float64(rng.IntN(5000)), float64(rng.IntN(300))+float64(i%7)/2
	}
	rx, ry := ranks.doubled(x, nil), ranks.doubled(y, nil)
	var sxy, sxx, syy wide
	for i := range n {
		dx, dy := rx[i]-int64(n)-1, ry[i]-int64(n)-1
		sxy.addMul(dx, dy)
		sxx.addMul(dx, dx)
		syy.addMul(dy, dy)
	}
	if got, want := rk.spearman(x, y), correlation(sxy, sxx, syy); math.Float64bits(got) != math.Float64bits(want) {
		t.Errorf("a large group: %v, ranked one after the other %v", got, want)
	}
	if rk.other == nil {
		t.Error("a large group's y was not ranked beside its x")
	}
	// A tie spans its ranks' mean: [1 1 2] ranks 1.5 1.5 3.
	if got := ranks.doubled([]float64{1, 1, 2}, nil); !slices.Equal(got, []int64{3, 3, 6}) {
		t.Errorf("doubled ranks %v", got)
	}
	if got := ranks.doubled([]float64{math.NaN(), 0, math.Copysign(0, -1), -1}, nil); !slices.Equal(got, []int64{8, 5, 5, 2}) {
		t.Errorf("doubled ranks of NaN and ±0: %v", got)
	}
}

// TestWideAddMulIsExact: a sum of products at 128 bits, against big.Int, at the ends
// of int64.
func TestWideAddMulIsExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(339, 3))
	edges := []int64{0, 1, -1, 2, -2, math.MaxInt64, math.MinInt64, math.MaxInt64 - 1, math.MinInt64 + 1, 1 << 32, -(1 << 32)}
	var w wide
	want := new(big.Int)
	for i := range 4000 {
		var a, b int64
		if i%3 == 0 {
			a, b = edges[rng.IntN(len(edges))], edges[rng.IntN(len(edges))]
		} else {
			a, b = int64(rng.Uint64()), int64(rng.Uint64())>>rng.IntN(64)
		}
		if i%5 == 0 {
			// Keep the sum inside 128 bits.
			a >>= 1
		}
		w.addMul(a, b)
		want.Add(want, new(big.Int).Mul(big.NewInt(a), big.NewInt(b)))
		got := i128.Int128(w)
		if got.String() != wrap128(want).String() {
			t.Fatalf("after %d products: %s, want %s", i+1, got, want)
		}
	}
}

// wrap128 is v modulo 2^128, signed.
func wrap128(v *big.Int) *big.Int {
	m := new(big.Int).Lsh(big.NewInt(1), 128)
	r := new(big.Int).Mod(v, m)
	if r.Cmp(new(big.Int).Lsh(big.NewInt(1), 127)) >= 0 {
		r.Sub(r, m)
	}
	return r
}
