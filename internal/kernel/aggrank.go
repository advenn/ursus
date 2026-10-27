package kernel

import (
	"cmp"
	"math"
	"runtime"
	"slices"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Entropy and Spearman's correlation (step 277, M1 of v0.6.6-scope.md).

// --- entropy -----------------------------------------------------------------------

// entropyAcc is Polars' entropy of each group's non-null values: −Σ p·ln p over the
// values as a distribution, each over their sum, or with normalize false −Σ x·ln x,
// either over ln(base). It keeps the sum S and Σ x·ln x, so it merges: the first is
// ln S − (Σ x·ln x)/S. Polars divides each value by S first, so the two agree to the
// last few bits. A zero or negative value makes it NaN, as Polars' 0·ln 0 is. A group
// of no value is null, as every aggregate's that reads its values is here; Polars'
// is −0, its empty sum negated, as its Sum of none is 0.
type entropyAcc struct {
	base      float64
	normalize bool
	out       dtype.DataType
	n         []uint64
	s, t      []float64
}

func (a *entropyAcc) Reserve(n int) {
	a.n = Extend(a.n, n, 0)
	a.s = Extend(a.s, n, 0)
	a.t = Extend(a.t, n, 0)
}

func (a *entropyAcc) AddBatch(groups []int32, col *data.Column) error {
	vals, err := widenFloat(col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		x := vals[i]
		a.n[g]++
		a.s[g] += x
		a.t[g] += x * math.Log(x)
	}
	return nil
}

func (a *entropyAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*entropyAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into entropyAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.n)))
	mergeEach(remap, len(o.n), func(dst, src int) {
		a.n[dst] += o.n[src]
		a.s[dst] += o.s[src]
		a.t[dst] += o.t[src]
	})
	return nil
}

func (a *entropyAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]float64, nGroups)
	seen := make([]bool, nGroups)
	lnBase := math.Log(a.base)
	for g := range nGroups {
		var h float64
		seen[g] = a.n[g] > 0
		switch {
		case !seen[g]:
		case a.normalize:
			h = (math.Log(a.s[g]) - a.t[g]/a.s[g]) / lnBase
		default:
			h = -a.t[g] / lnBase
		}
		out[g] = h
	}
	valid := seenBitmap(seen, nGroups)
	if a.out == dtype.Float32 {
		narrow := make([]float32, nGroups)
		for i, v := range out {
			narrow[i] = float32(v)
		}
		return data.NewFixed(name, dtype.Float32, narrow, valid), nil
	}
	return data.NewFixed(name, dtype.Float64, out, valid), nil
}

func (a *entropyAcc) NBytes() int64 { return int64(cap(a.n))*8 + int64(cap(a.s))*8 + int64(cap(a.t))*8 }

// --- Spearman's correlation ----------------------------------------------------------

// spearmanAcc is Spearman's correlation of each group's pairs where both are
// non-null: Pearson's of their ranks, a tie the mean of its ranks and NaN above every
// number, as Polars ranks them. It keeps every such pair, as a median keeps its
// values, and past its sink's budget hands them to disk (pairspill.go). Fewer than
// two pairs, or values all alike, are NaN, as Polars answers.
type spearmanAcc struct {
	xs, ys [][]float64

	// Past its sink's budget its pairs go to sp, cnt of them a group, and every
	// answer is read back from there at Finish (pairspill.go, step 339).
	cnt []int
	sp  PairSpill
}

func (a *spearmanAcc) Reserve(n int) {
	a.xs = Extend(a.xs, n, nil)
	a.ys = Extend(a.ys, n, nil)
}

func (a *spearmanAcc) AddBatch(groups []int32, col *data.Column) error {
	fx, fy, err := pairFields(col)
	if err != nil {
		return err
	}
	xs, err := widenFloat(fx)
	if err != nil {
		return err
	}
	ys, err := widenFloat(fy)
	if err != nil {
		return err
	}
	sv, xv, yv := col.Validity(), fx.Validity(), fy.Validity()
	for i, g := range groups {
		if sv.Get(i) && xv.Get(i) && yv.Get(i) {
			a.xs[g] = append(a.xs[g], xs[i])
			a.ys[g] = append(a.ys[g], ys[i])
		}
	}
	return nil
}

func (a *spearmanAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*spearmanAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into spearmanAcc", other)
	}
	if a.sp != nil || o.sp != nil {
		// Only a serial sink spills its pairs: parallel workers go serial first.
		return uerr.Internalf("kernel: cannot merge a spearman whose pairs spilled")
	}
	a.Reserve(mergeCap(remap, len(o.xs)))
	mergeEach(remap, len(o.xs), func(dst, src int) {
		a.xs[dst] = append(a.xs[dst], o.xs[src]...)
		a.ys[dst] = append(a.ys[dst], o.ys[src]...)
	})
	return nil
}

func (a *spearmanAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	out := make([]float64, nGroups)
	if a.sp != nil {
		if err := a.finishSpilled(out); err != nil {
			return nil, err
		}
		return data.NewFixed(name, dtype.Float64, out, bitmap.AllSet(nGroups)), nil
	}
	var rk ranker
	for g := range nGroups {
		out[g] = rk.spearman(a.xs[g], a.ys[g])
	}
	return data.NewFixed(name, dtype.Float64, out, bitmap.AllSet(nGroups)), nil
}

func (a *spearmanAcc) NBytes() int64 {
	b := int64(cap(a.cnt)) * 8
	for g := range a.xs {
		b += int64(cap(a.xs[g])+cap(a.ys[g]))*8 + 48
	}
	return b
}

// ranker ranks a group's pairs, its buffers kept from one group to the next.
type ranker struct {
	sorter radixSorter
	keys   []uint64
	idx    []int32
	rx, ry []int64
	other  *ranker // a large group's y, ranked beside its x
}

// parallelRanks is the fewest pairs whose two variables are ranked side by side.
const parallelRanks = 1 << 16

// spearman is Spearman's correlation of the pairs (x[i], y[i]), NaN below two, its
// sums exact as a spilled group's are (pairspill.go).
func (k *ranker) spearman(x, y []float64) float64 {
	n := len(x)
	if n < 2 {
		return math.NaN()
	}
	if n >= parallelRanks && runtime.GOMAXPROCS(0) > 1 {
		if k.other == nil {
			k.other = &ranker{}
		}
		var (
			wg    sync.WaitGroup
			relay uerr.Relay
		)
		relay.Go(&wg, func() { k.other.ry = k.other.doubled(y, k.other.ry) })
		k.rx = k.doubled(x, k.rx)
		wg.Wait()
		relay.Raise()
		k.ry, k.other.ry = k.other.ry, k.ry
	} else {
		k.rx = k.doubled(x, k.rx)
		k.ry = k.doubled(y, k.ry)
	}
	mean := int64(n) + 1 // doubled, as the ranks are
	var sxy, sxx, syy wide
	for i := range n {
		dx, dy := k.rx[i]-mean, k.ry[i]-mean
		sxy.addMul(dx, dy)
		sxx.addMul(dx, dx)
		syy.addMul(dy, dy)
	}
	return correlation(sxy, sxx, syy)
}

// doubled are twice v's ranks from 1, into ranks: a tie the mean of the ranks it
// spans, in the total order, NaN above every number; integers, 2·lo + t + 1 for a tie
// of t from lo. The order is OrderKeyF64's, sorted by radix where there are enough
// values to repay its passes.
func (k *ranker) doubled(v []float64, ranks []int64) []int64 {
	n := len(v)
	keys := Extend(k.keys[:0], n, 0)
	for i, x := range v {
		keys[i] = OrderKeyF64(x)
	}
	idx := Extend(k.idx[:0], n, 0)
	for i := range idx {
		idx[i] = int32(i)
	}
	k.keys, k.idx = keys, idx
	if n >= radixRanks {
		k.sorter.scratch = Extend(k.sorter.scratch[:0], n, 0)
		idx, keys = k.sorter.radixSorted(idx, k.sorter.scratch, keys)
	} else {
		slices.SortFunc(idx, func(a, b int32) int { return cmp.Compare(keys[a], keys[b]) })
		sorted := k.sorter.keyBuf(n)
		for i, j := range idx {
			sorted[i] = keys[j]
		}
		keys = sorted
	}
	ranks = Extend(ranks[:0], n, 0)
	for lo := 0; lo < n; {
		hi := lo + 1
		for hi < n && keys[hi] == keys[lo] {
			hi++
		}
		r := int64(lo + 1 + hi)
		for _, i := range idx[lo:hi] {
			ranks[i] = r
		}
		lo = hi
	}
	return ranks
}

// radixRanks is the fewest values doubled sorts by radix.
const radixRanks = 256
