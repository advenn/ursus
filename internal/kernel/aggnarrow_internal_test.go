package kernel

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// newSumMean is a sum and a mean of in.
func newSumMean(t *testing.T, in dtype.DataType) (*sumAcc, *meanAcc) {
	t.Helper()
	var out []Accumulator
	for _, op := range []expr.AggOp{expr.AggSum, expr.AggMean} {
		bind, err := expr.ResolveAggBinding(op, in)
		if err != nil {
			t.Fatal(err)
		}
		acc, err := NewAccumulator(op, in, bind, expr.AggParams{})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, acc)
	}
	return out[0].(*sumAcc), out[1].(*meanAcc)
}

// TestANarrowIntegersSumsAreExact: a sum and a mean of an integer of 32 bits or
// fewer add in int64 partials that fold into their exact sums (step 215). Lowered to
// fold every 37 rows, over the extremes of each type, with nulls, a group each of
// 50: every sum is the exact one, every partial is under 38 values' worth, and a
// merge leaves the other's partials as they were.
func TestANarrowIntegersSumsAreExact(t *testing.T) {
	saved := narrowFold
	narrowFold = 37
	defer func() { narrowFold = saved }()
	rng := rand.New(rand.NewPCG(215, 1))
	types := []struct {
		dt     dtype.DataType
		lo, hi int64
		mk     func(vals []int64, valid bitmap.View) *data.Column
	}{
		{dtype.Int8, math.MinInt8, math.MaxInt8, func(v []int64, ok bitmap.View) *data.Column {
			return data.NewFixed("v", dtype.Int8, narrowOf[int8](v), ok)
		}},
		{dtype.Int16, math.MinInt16, math.MaxInt16, func(v []int64, ok bitmap.View) *data.Column {
			return data.NewFixed("v", dtype.Int16, narrowOf[int16](v), ok)
		}},
		{dtype.Int32, math.MinInt32, math.MaxInt32, func(v []int64, ok bitmap.View) *data.Column {
			return data.NewFixed("v", dtype.Int32, narrowOf[int32](v), ok)
		}},
		{dtype.Uint8, 0, math.MaxUint8, func(v []int64, ok bitmap.View) *data.Column {
			return data.NewFixed("v", dtype.Uint8, narrowOf[uint8](v), ok)
		}},
		{dtype.Uint16, 0, math.MaxUint16, func(v []int64, ok bitmap.View) *data.Column {
			return data.NewFixed("v", dtype.Uint16, narrowOf[uint16](v), ok)
		}},
		{dtype.Uint32, 0, math.MaxUint32, func(v []int64, ok bitmap.View) *data.Column {
			return data.NewFixed("v", dtype.Uint32, narrowOf[uint32](v), ok)
		}},
	}
	const groups = 50
	for _, ty := range types {
		t.Run(ty.dt.String(), func(t *testing.T) {
			sums := [2][]*sumAcc{}
			means := [2][]*meanAcc{}
			want := make([]i128.Int128, groups)
			count := make([]uint64, groups)
			for half := range 2 {
				s, m := newSumMean(t, ty.dt)
				if !s.narrow || !m.narrow {
					t.Fatalf("a %s is not narrow", ty.dt)
				}
				sums[half], means[half] = []*sumAcc{s}, []*meanAcc{m}
				for range 20 {
					n := 1 + rng.IntN(60)
					vals, gids := make([]int64, n), make([]int32, n)
					valid := bitmap.NewBuilder(n)
					for i := range n {
						switch rng.IntN(4) {
						case 0:
							vals[i] = ty.hi
						case 1:
							vals[i] = ty.lo
						default:
							vals[i] = ty.lo + rng.Int64N(ty.hi-ty.lo+1)
						}
						gids[i] = int32(rng.IntN(groups))
						ok := rng.IntN(9) != 0
						valid.Append(ok)
						if ok {
							want[gids[i]] = want[gids[i]].Add(i128.FromInt64(vals[i]))
							count[gids[i]]++
						}
					}
					col := ty.mk(vals, valid.Finish())
					for _, acc := range []Accumulator{s, m} {
						acc.Reserve(groups)
						if err := acc.AddBatch(gids, col); err != nil {
							t.Fatal(err)
						}
					}
					// Never more than narrowFold rows since the last fold.
					if s.ni.pend > narrowFold || m.ni.pend > narrowFold {
						t.Fatalf("%d and %d rows unfolded, over %d", s.ni.pend, m.ni.pend, narrowFold)
					}
				}
			}
			// Merge the second half into the first; the second's partials stay.
			other := append([]int64(nil), sums[1][0].ni.s...)
			if err := sums[0][0].Merge(sums[1][0], nil); err != nil {
				t.Fatal(err)
			}
			if err := means[0][0].Merge(means[1][0], nil); err != nil {
				t.Fatal(err)
			}
			for g, v := range other {
				if sums[1][0].ni.s[g] != v {
					t.Fatalf("the merge wrote the other's partial of group %d", g)
				}
			}
			s, m := sums[0][0], means[0][0]
			sc, err := s.Finish("s", groups)
			if err != nil {
				t.Fatal(err)
			}
			got := data.MustValues[i128.Int128](sc)
			for g := range groups {
				if count[g] > 0 && got[g] != want[g] {
					t.Fatalf("group %d: sum %v, want %v", g, got[g], want[g])
				}
			}
			mc, err := m.Finish("m", groups)
			if err != nil {
				t.Fatal(err)
			}
			for g := range groups {
				if count[g] == 0 || !mc.IsValid(g) {
					continue
				}
				f := want[g].Float64()
				if w, got := f/float64(count[g]), data.MustValues[float64](mc)[g]; math.Abs(got-w) > 1e-9*math.Max(1, math.Abs(w)) {
					t.Fatalf("group %d: mean %v, want %v", g, got, w)
				}
			}
		})
	}
}

func narrowOf[T int8 | int16 | int32 | uint8 | uint16 | uint32](v []int64) []T {
	out := make([]T, len(v))
	for i, x := range v {
		out[i] = T(x)
	}
	return out
}
