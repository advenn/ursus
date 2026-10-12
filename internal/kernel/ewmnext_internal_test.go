package kernel

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// TestAnEwmCarriedAcrossSplitsIsOnePass: an ewm function run a piece at a time with
// EwmNext, its state carried from piece to piece, answers as one pass over the whole
// partition does, bit for bit (step 221). Each function, adjusted or not, biased or
// not, ignoring nulls or not, wanting one value or four; over runs of nulls longer
// than a piece, a NaN late on, and pieces of one row to sixty.
func TestAnEwmCarriedAcrossSplitsIsOnePass(t *testing.T) {
	const n = 2000
	rng := rand.New(rand.NewPCG(221, 1))
	vals := make([]float64, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		vals[i] = rng.NormFloat64()*10 + float64(i%17)
		ok.Append(i%400 >= 90 && rng.IntN(6) != 0) // runs of 90 nulls, and some others
	}
	vals[1700] = math.NaN()
	col := data.NewFixed("v", dtype.Float64, vals, ok.Finish())
	perm := make([]int32, n)
	for i := range perm {
		perm[i] = int32(i)
	}
	whole := Segments{Perm: perm, Bounds: []int32{0, n}}

	for _, fn := range []expr.WinFnOp{expr.WinEwmMean, expr.WinEwmVar, expr.WinEwmStd} {
		for flags := range 16 {
			params := expr.WinParams{Alpha: 0.15, NoAdjust: flags&1 != 0, Bias: flags&2 != 0,
				IgnoreNulls: flags&4 != 0, MinSamples: 1}
			if flags&8 != 0 {
				params.MinSamples = 4
			}
			want, err := WinCall(fn, params, "w", dtype.Float64, col, nil, whole)
			if err != nil {
				t.Fatal(err)
			}
			var st EwmState
			wv := data.MustValues[float64](want)
			for lo := 0; lo < n; {
				hi := min(n, lo+1+rng.IntN(60))
				got, err := EwmNext(fn, params, "w", dtype.Float64, col.Slice(lo, hi-lo), &st)
				if err != nil {
					t.Fatal(err)
				}
				gv := data.MustValues[float64](got)
				for i := range hi - lo {
					wok, gok := want.IsValid(lo+i), got.IsValid(i)
					if wok != gok || (wok && math.Float64bits(wv[lo+i]) != math.Float64bits(gv[i])) {
						t.Fatalf("%v %+v: row %d is %v (valid %v), one pass %v (valid %v)",
							fn, params, lo+i, gv[i], gok, wv[lo+i], wok)
					}
				}
				lo = hi
			}
		}
	}
}
