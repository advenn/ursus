package kernel

import (
	"math"
	"slices"

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
// values, so its state grows with its input. Fewer than two pairs, or values all
// alike, are NaN, as Polars answers.
type spearmanAcc struct {
	xs, ys [][]float64
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
	for g := range nGroups {
		out[g] = pearson(averageRanks(a.xs[g]), averageRanks(a.ys[g]))
	}
	return data.NewFixed(name, dtype.Float64, out, bitmap.AllSet(nGroups)), nil
}

func (a *spearmanAcc) NBytes() int64 {
	var b int64
	for g := range a.xs {
		b += int64(cap(a.xs[g])+cap(a.ys[g]))*8 + 48
	}
	return b
}

// averageRanks are v's ranks from 1, a tie the mean of the ranks it spans, in the
// total order, NaN above every number.
func averageRanks(v []float64) []float64 {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(i, j int) int {
		ki, kj := OrderKeyF64(v[i]), OrderKeyF64(v[j])
		switch {
		case ki < kj:
			return -1
		case ki > kj:
			return 1
		}
		return 0
	})
	ranks := make([]float64, len(v))
	for lo := 0; lo < len(idx); {
		hi := lo + 1
		for hi < len(idx) && OrderKeyF64(v[idx[hi]]) == OrderKeyF64(v[idx[lo]]) {
			hi++
		}
		r := float64(lo+1+hi) / 2 // the mean of lo+1 .. hi
		for _, i := range idx[lo:hi] {
			ranks[i] = r
		}
		lo = hi
	}
	return ranks
}

// pearson is the correlation of x and y, NaN below two values.
func pearson(x, y []float64) float64 {
	n := len(x)
	if n < 2 {
		return math.NaN()
	}
	var mx, my float64
	for i := range n {
		mx += x[i]
		my += y[i]
	}
	mx /= float64(n)
	my /= float64(n)
	var sxy, sxx, syy float64
	for i := range n {
		dx, dy := x[i]-mx, y[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	return sxy / math.Sqrt(sxx*syy)
}
