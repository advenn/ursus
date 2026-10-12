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

// TestARollingQuantileIsEachWindowsQuantile: a rolling quantile kept in a sorted
// window answers as sorting each window afresh does (step 227), over 3,000 rows of
// repeats, NaN, ±0, infinities and nulls, in three partitions, for windows of 1 to
// 40 rows trailing and centred, each interpolation, four quantiles and a minimum of
// values.
func TestARollingQuantileIsEachWindowsQuantile(t *testing.T) {
	const n = 3000
	rng := rand.New(rand.NewPCG(227, 1))
	pool := []float64{math.NaN(), 0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1), 1, 1, 2, 2, 2}
	vals := make([]float64, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		vals[i] = math.Round(rng.NormFloat64() * 5)
		if rng.IntN(4) == 0 {
			vals[i] = pool[rng.IntN(len(pool))]
		}
		ok.Append(rng.IntN(8) != 0)
	}
	col := data.NewFixed("v", dtype.Float64, vals, ok.Finish())
	// Three partitions of interleaved rows, each in row order.
	perm := make([]int32, 0, n)
	bounds := []int32{0}
	for p := range 3 {
		for i := p; i < n; i += 3 {
			perm = append(perm, int32(i))
		}
		bounds = append(bounds, int32(len(perm)))
	}
	seg := Segments{Perm: perm, Bounds: bounds}

	for _, size := range []int{1, 2, 7, 40} {
		for _, center := range []bool{false, true} {
			for interp := expr.InterpLinear; interp.String() != "?"; interp++ {
				for _, q := range []float64{0, 0.3, 0.5, 1} {
					minN := 1 + rng.IntN(size)
					p := expr.WinParams{N: int64(size), MinSamples: int64(minN), Center: center,
						Q: q, Interp: interp}
					got, err := WinCall(expr.WinRollingQuantile, p, "r", dtype.Float64, col, nil, seg)
					if err != nil {
						t.Fatal(err)
					}
					gv := data.MustValues[float64](got)
					for b := 0; b+1 < len(bounds); b++ {
						rows := perm[bounds[b]:bounds[b+1]]
						for i, row := range rows {
							lo, hi := max(0, i-size+1), i+1
							if center {
								lo, hi = max(0, i-size/2), min(len(rows), i+(size-1)/2+1)
							}
							var w []float64
							for _, r := range rows[lo:hi] {
								if col.IsValid(int(r)) {
									w = append(w, vals[r])
								}
							}
							slices.SortFunc(w, compareTotalF64)
							wantOK := len(w) > 0 && len(w) >= minN
							if got.IsValid(int(row)) != wantOK {
								t.Fatalf("size %d center %v %v q %v: row %d answered %v, want %v",
									size, center, interp, q, row, got.IsValid(int(row)), wantOK)
							}
							if !wantOK {
								continue
							}
							l, h, frac := ranks(len(w), q)
							if want := interpolate(w[l], w[h], frac, interp); !sameFloat(gv[row], want) {
								t.Fatalf("size %d center %v %v q %v: row %d is %v, want %v",
									size, center, interp, q, row, gv[row], want)
							}
						}
					}
				}
			}
		}
	}
}
