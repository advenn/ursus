package kernel

// Step 400: a column and a constant are added, subtracted, multiplied and divided
// with the constant read once (byConst), where broadcastVals copied it into a column
// of the batch's length first (B3 of v0.8-scope.md).

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

// sides is a column of vs and a constant c on either side, as one value and as the
// column broadcastVals would make of it.
func sides[T data.Primitive](dt dtype.DataType, vs []T, c T, left bool) (one, many [2]*data.Column) {
	n := len(vs)
	col := data.NewFixed("x", dt, vs, bitmap.AllSet(n))
	single := data.NewFixed("c", dt, []T{c}, bitmap.AllSet(1))
	copied := data.NewFixed("c", dt, slices.Repeat([]T{c}, n), bitmap.AllSet(n))
	if left {
		return [2]*data.Column{single, col}, [2]*data.Column{copied, col}
	}
	return [2]*data.Column{col, single}, [2]*data.Column{col, copied}
}

// TestAConstantIsReadOnce: a Float64 column and a constant, on either side, added,
// subtracted, multiplied and divided, NaN, the infinities and both zeros among
// them, answer bit for bit what the constant copied to a column answers; an Int64's
// and an Int8's, which wrap, added, subtracted and multiplied, too. None allocates
// more than the two columns' answer does.
func TestAConstantIsReadOnce(t *testing.T) {
	rng := rand.New(rand.NewPCG(400, 1))
	const n = 1_000
	all := bitmap.AllSet(n)
	fs := randFloats(rng, n)
	is, i8s := make([]int64, n), make([]int8, n)
	for i := range n {
		is[i] = int64(rng.Uint64())
		i8s[i] = int8(rng.Uint32())
	}
	for _, left := range []bool{false, true} {
		for _, op := range []expr.BinaryOp{expr.OpAdd, expr.OpSub, expr.OpMul, expr.OpDiv} {
			for _, c := range []float64{2, -0.5, 0, math.Copysign(0, -1), math.NaN(), math.Inf(1), math.Inf(-1), 1e-310} {
				one, many := sides(dtype.Float64, fs, c, left)
				got, err := arithFloat[float64](op, "y", dtype.Float64, one[0], one[1], n, all)
				if err != nil {
					t.Fatal(err)
				}
				want, err := arithFloat[float64](op, "y", dtype.Float64, many[0], many[1], n, all)
				if err != nil {
					t.Fatal(err)
				}
				g, w := data.MustValues[float64](got), data.MustValues[float64](want)
				for i := range n {
					if math.Float64bits(g[i]) != math.Float64bits(w[i]) {
						t.Fatalf("%v %s %v, left %v: row %d is %v, broadcast %v", fs[i], op, c, left, i, g[i], w[i])
					}
				}
				allocs(t, op, left, func() { arithFloat[float64](op, "y", dtype.Float64, one[0], one[1], n, all) },
					func() { arithFloat[float64](op, "y", dtype.Float64, many[0], many[1], n, all) })
			}
		}
		for _, op := range []expr.BinaryOp{expr.OpAdd, expr.OpSub, expr.OpMul} {
			for _, c := range []int64{3, -1, math.MaxInt64, math.MinInt64} {
				one, many := sides(dtype.Int64, is, c, left)
				got, _ := arithNum[int64](op, "y", dtype.Int64, one[0], one[1], n, all)
				want, _ := arithNum[int64](op, "y", dtype.Int64, many[0], many[1], n, all)
				if !slices.Equal(data.MustValues[int64](got), data.MustValues[int64](want)) {
					t.Fatalf("Int64 %s %d, left %v: not the broadcast's answer", op, c, left)
				}
				allocs(t, op, left, func() { arithNum[int64](op, "y", dtype.Int64, one[0], one[1], n, all) },
					func() { arithNum[int64](op, "y", dtype.Int64, many[0], many[1], n, all) })
			}
			for _, c := range []int8{7, -128, 127} {
				one, many := sides(dtype.Int8, i8s, c, left)
				got, _ := arithNum[int8](op, "y", dtype.Int8, one[0], one[1], n, all)
				want, _ := arithNum[int8](op, "y", dtype.Int8, many[0], many[1], n, all)
				if !slices.Equal(data.MustValues[int8](got), data.MustValues[int8](want)) {
					t.Fatalf("Int8 %s %d, left %v: not the broadcast's answer", op, c, left)
				}
			}
		}
	}
}

// allocs fails when a column and a constant allocate more than two columns do: the
// constant copied to a column is one allocation more.
func allocs(t *testing.T, op expr.BinaryOp, left bool, constant, columns func()) {
	t.Helper()
	if c, w := testing.AllocsPerRun(20, constant), testing.AllocsPerRun(20, columns); c > w {
		t.Fatalf("%s, the constant on the left %v: %v allocations, two columns %v", op, left, c, w)
	}
}
