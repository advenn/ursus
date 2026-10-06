package kernel

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/expr"
)

// quantileBySort is the quantile as it was computed before step 105: sort the whole
// group under the total order, then read the two ranks.
func quantileBySort(v []float64, q float64, interp expr.Interpolation) float64 {
	s := slices.Clone(v)
	slices.SortFunc(s, compareTotalF64)
	n := len(s)
	if n == 1 {
		return s[0]
	}
	pos := q * float64(n-1)
	lo := max(0, min(int(math.Floor(pos)), n-1))
	hi := max(0, min(int(math.Ceil(pos)), n-1))
	frac := pos - float64(lo)
	switch interp {
	case expr.InterpLower:
		return s[lo]
	case expr.InterpHigher:
		return s[hi]
	case expr.InterpNearest:
		if frac < 0.5 {
			return s[lo]
		}
		return s[hi]
	case expr.InterpMidpoint:
		return midpoint(s[lo], s[hi])
	default:
		return lerp(s[lo], s[hi], frac)
	}
}

// sameFloat is equality under the total order: NaN is NaN, and -0 is +0.
func sameFloat(a, b float64) bool { return compareTotalF64(a, b) == 0 }

// TestQuantileBySelectionIsTheSortedAnswer: every interpolation, at every q that
// lands on a rank and between ranks, over groups with NaNs, infinities, signed
// zeros and long runs of equal values, against sorting the group.
func TestQuantileBySelectionIsTheSortedAnswer(t *testing.T) {
	r := rand.New(rand.NewPCG(105, 7))
	special := []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, math.Copysign(0, -1), 1, -1}
	value := func(kind int) float64 {
		switch kind {
		case 0: // special values and duplicates
			return special[r.IntN(len(special))]
		case 1: // a few distinct values, many repeats
			return float64(r.IntN(4))
		default:
			return r.NormFloat64() * 1e3
		}
	}
	interps := []expr.Interpolation{expr.InterpLinear, expr.InterpLower, expr.InterpHigher, expr.InterpNearest, expr.InterpMidpoint}
	qs := []float64{0, 0.001, 0.1, 0.25, 0.5, 0.75, 0.9, 0.999, 1}
	for trial := range 3000 {
		n := 1 + r.IntN(60)
		if trial%100 == 0 {
			n = 1000 + r.IntN(5000)
		}
		kind := r.IntN(3)
		v := make([]float64, n)
		for i := range v {
			v[i] = value(kind)
		}
		// Sorted and reverse-sorted inputs, the classic quickselect worst cases.
		switch trial % 7 {
		case 1:
			slices.SortFunc(v, compareTotalF64)
		case 2:
			slices.SortFunc(v, func(a, b float64) int { return compareTotalF64(b, a) })
		}
		for _, q := range qs {
			for _, in := range interps {
				want := quantileBySort(v, q, in)
				got := quantileOf(slices.Clone(v), q, in)
				if !sameFloat(got, want) {
					t.Fatalf("trial %d, n=%d, q=%v, interpolation %d: %v, want %v", trial, n, q, in, got, want)
				}
			}
		}
	}
}

// TestSelectNthPlacesTheRank: v[k] is the sorted v[k], nothing after it is smaller
// and nothing before it larger, for every k.
func TestSelectNthPlacesTheRank(t *testing.T) {
	r := rand.New(rand.NewPCG(105, 8))
	for range 300 {
		n := 1 + r.IntN(50)
		v := make([]float64, n)
		for i := range v {
			v[i] = float64(r.IntN(10))
		}
		sorted := slices.Clone(v)
		slices.SortFunc(sorted, compareTotalF64)
		for k := range n {
			w := slices.Clone(v)
			selectNth(w, k)
			if !sameFloat(w[k], sorted[k]) {
				t.Fatalf("n=%d k=%d: v[k] = %v, want %v", n, k, w[k], sorted[k])
			}
			for i := range k {
				if compareTotalF64(w[i], w[k]) > 0 {
					t.Fatalf("n=%d k=%d: v[%d] = %v is after-sized, before the rank", n, k, i, w[i])
				}
			}
			for i := k + 1; i < n; i++ {
				if compareTotalF64(w[i], w[k]) < 0 {
					t.Fatalf("n=%d k=%d: v[%d] = %v is smaller, after the rank", n, k, i, w[i])
				}
			}
		}
	}
}
