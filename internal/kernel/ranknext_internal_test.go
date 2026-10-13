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

// TestARankStreamedInPiecesIsWinRank: rank streamed with RankNext over a column in
// rank's order, cut into pieces of one to fifty rows, is winRank over the whole of it
// (step 237): ordinal, dense and min forward; max as min over the column reversed,
// counted from the end; average as the two halved. Over floats with nulls, NaN, ±0
// and long ties, and strings, ascending and descending.
func TestARankStreamedInPiecesIsWinRank(t *testing.T) {
	rng := rand.New(rand.NewPCG(237, 1))
	const n = 1500
	pool := []float64{math.NaN(), 0, math.Copysign(0, -1), 1, 2, 2, 2, -1}
	fv := make([]float64, n)
	sv := make([]string, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		fv[i] = pool[rng.IntN(len(pool))]
		sv[i] = []string{"a", "b", "b", "c"}[rng.IntN(4)]
		ok.Append(rng.IntN(7) != 0)
	}
	valid := ok.Finish()
	for _, raw := range []*data.Column{
		data.NewFixed("f", dtype.Float64, fv, valid),
		data.NewString("s", sv, valid),
	} {
		for _, desc := range []bool{false, true} {
			// The column in rank's order: nulls first, then by value, as segments sorts.
			perm, err := ArgSortColumns([]*data.Column{raw}, []SortSpec{{Descending: desc}}, n)
			if err != nil {
				t.Fatal(err)
			}
			sorted, err := Take(raw, perm)
			if err != nil {
				t.Fatal(err)
			}
			ident := make([]int32, n)
			for i := range ident {
				ident[i] = int32(i)
			}
			seg := Segments{Perm: ident, Bounds: []int32{0, n}}
			rev := make([]int32, n)
			for i := range rev {
				rev[i] = int32(n - 1 - i)
			}
			reversed, err := Take(sorted, rev)
			if err != nil {
				t.Fatal(err)
			}
			stream := func(c *data.Column, m expr.RankMethod) []uint32 {
				var st RankState
				var out []uint32
				for lo := 0; lo < n; {
					hi := min(n, lo+1+rng.IntN(50))
					r, err := RankNext(m, "r", c.Slice(lo, hi-lo), &st)
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, data.MustValues[uint32](r)...)
					lo = hi
				}
				return out
			}
			first, minRev := stream(sorted, expr.RankMin), stream(reversed, expr.RankMin)
			for _, m := range []expr.RankMethod{expr.RankOrdinal, expr.RankDense, expr.RankMin,
				expr.RankMax, expr.RankAverage} {
				out := dtype.Uint32
				if m == expr.RankAverage {
					out = dtype.Float64
				}
				want, err := winRank(expr.WinParams{Method: m, Descending: desc}, "r", out, sorted, seg)
				if err != nil {
					t.Fatal(err)
				}
				var got []uint32
				if m != expr.RankMax && m != expr.RankAverage {
					got = stream(sorted, m)
				}
				for i := range n {
					last := uint32(n) + 1 - minRev[n-1-i] // the group's last, from the end
					switch m {
					case expr.RankAverage:
						if g, w := float64(first[i]+last)/2, data.MustValues[float64](want)[i]; g != w {
							t.Fatalf("%s desc=%v average: row %d is %v, want %v", raw.Name(), desc, i, g, w)
						}
					case expr.RankMax:
						if w := data.MustValues[uint32](want)[i]; last != w {
							t.Fatalf("%s desc=%v max: row %d is %d, want %d", raw.Name(), desc, i, last, w)
						}
					default:
						if w := data.MustValues[uint32](want)[i]; got[i] != w {
							t.Fatalf("%s desc=%v %v: row %d is %d, want %d", raw.Name(), desc, m, i, got[i], w)
						}
					}
				}
			}
		}
	}
}
