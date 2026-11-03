package kernel

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// TestARollingWindowStreamedInPiecesIsTheWindow (step 429): a rolling sum, mean,
// var, std, skew, kurtosis, cov and corr over a window of rows, walked with
// RollingNext over a partition cut into pieces of one row to a few hundred, is the
// in-memory kernel over the whole of it, to the bit. Of sin(i)·1000 with nulls, NaN,
// ±Inf and ±0, in Float64 and Float32; windows of 1, 2, 7, 64 and 100 rows, wanting
// every row's value or fewer.
func TestARollingWindowStreamedInPiecesIsTheWindow(t *testing.T) {
	rng := rand.New(rand.NewPCG(429, 1))
	const n = 3000
	pool := []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, math.Copysign(0, -1)}
	xv, yv := make([]float64, n), make([]float64, n)
	xok, yok := bitmap.NewBuilder(n), bitmap.NewBuilder(n)
	for i := range n {
		xv[i], yv[i] = math.Sin(float64(i))*1000, math.Cos(float64(i)*0.7)*500+float64(i%17)
		if rng.IntN(60) == 0 {
			xv[i] = pool[rng.IntN(len(pool))]
		}
		xok.Append(rng.IntN(11) != 0)
		yok.Append(rng.IntN(13) != 0)
	}
	x32 := make([]float32, n)
	for i, v := range xv {
		x32[i] = float32(v)
	}
	xValid, yValid := xok.Finish(), yok.Finish()
	values := map[string]*data.Column{
		"float64": data.NewFixed("0", dtype.Float64, xv, xValid),
		"float32": data.NewFixed("0", dtype.Float32, x32, xValid),
	}
	y := data.NewFixed("1", dtype.Float64, yv, yValid)
	seg := Segments{Perm: make([]int32, n), Bounds: []int32{0, n}}
	for i := range seg.Perm {
		seg.Perm[i] = int32(i)
	}
	fns := []struct {
		fn     expr.WinFnOp
		params expr.WinParams
	}{
		{expr.WinRollingSum, expr.WinParams{}},
		{expr.WinRollingMean, expr.WinParams{}},
		{expr.WinRollingVar, expr.WinParams{Ddof: 1}},
		{expr.WinRollingStd, expr.WinParams{Ddof: 0}},
		{expr.WinRollingSkew, expr.WinParams{Bias: false}},
		{expr.WinRollingKurtosis, expr.WinParams{Fisher: true, Bias: true}},
		{expr.WinRollingCov, expr.WinParams{Ddof: 1}},
		{expr.WinRollingCorr, expr.WinParams{}},
	}
	for vname, value := range values {
		for _, fc := range fns {
			for _, size := range []int64{1, 2, 7, 64, 100} {
				for _, minN := range []int64{0, 1, (size + 1) / 2} {
					params := fc.params
					params.N, params.MinSamples = size, minN
					out := dtype.Float64
					if vname == "float32" && fc.fn != expr.WinRollingCov && fc.fn != expr.WinRollingCorr {
						out = dtype.Float32
					}
					col := value
					if fc.fn == expr.WinRollingCov || fc.fn == expr.WinRollingCorr {
						col = data.NewStruct("p", []*data.Column{value, y}, bitmap.AllSet(n))
					}
					name := fmt.Sprintf("%s of %s over %d rows, min %d", fc.fn, vname, size, minN)
					if !RollingSteps(fc.fn, params, out) {
						t.Fatalf("%s: not RollingNext's", name)
					}
					want, err := winRolling(t.Context(), fc.fn, params, "r", out, col, seg)
					if err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					var st RollingState
					var got []*data.Column
					for lo := 0; lo < n; {
						hi := min(n, lo+1+rng.IntN(150))
						c, err := RollingNext(fc.fn, params, "r", out, col.Slice(lo, hi-lo), &st)
						if err != nil {
							t.Fatalf("%s, rows %d to %d: %v", name, lo, hi, err)
						}
						got = append(got, c)
						lo = hi
					}
					all, err := ConcatColumns("r", got)
					if err != nil {
						t.Fatal(err)
					}
					assertSameColumn(t, name, all, want)
				}
			}
		}
	}
}
