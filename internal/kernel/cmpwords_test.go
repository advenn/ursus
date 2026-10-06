package kernel_test

// Comparisons read a scalar side in place and pack their results a word at a time
// (step 107). These hold every operator, at every word boundary, against Go's own
// operators — IEEE's order for floats, so NaN compares false — for a scalar on
// either side, both, and neither.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

var cmpOps = []expr.BinaryOp{expr.OpEq, expr.OpNe, expr.OpLt, expr.OpLe, expr.OpGt, expr.OpGe}

// checkCompare runs every operator over every shape of l and r and checks each
// row against want(op, i).
func checkCompare[T data.Fixed](t *testing.T, dt dtype.DataType, a, b []T,
	want func(op expr.BinaryOp, x, y T) bool) {
	t.Helper()
	col := func(v []T) *data.Column { return data.NewFixed("c", dt, v, bitmap.View{}) }
	shapes := []struct {
		name string
		l, r []T
	}{
		{"column op column", a, b},
		{"column op scalar", a, b[:1]},
		{"scalar op column", a[:1], b},
		{"scalar op scalar", a[:1], b[:1]},
	}
	for _, sh := range shapes {
		for _, op := range cmpOps {
			// Two one-element operands make a one-row answer.
			n := max(len(sh.l), len(sh.r))
			got, err := kernel.Binary(op, "x", dtype.Bool, col(sh.l), col(sh.r))
			if err != nil {
				t.Fatalf("%s, %s %s: %v", dt, sh.name, op, err)
			}
			if got.Len() != n {
				t.Fatalf("%s, %s %s: %d rows, want %d", dt, sh.name, op, got.Len(), n)
			}
			bits := got.Bools()
			for i := range n {
				x, y := sh.l[min(i, len(sh.l)-1)], sh.r[min(i, len(sh.r)-1)]
				if w := want(op, x, y); bits.Get(i) != w {
					t.Fatalf("%s, %s, n=%d, row %d: %v %s %v = %v, want %v",
						dt, sh.name, n, i, x, op, y, bits.Get(i), w)
				}
			}
		}
	}
}

func ordered[T int8 | int64 | uint16 | float32 | float64](op expr.BinaryOp, x, y T) bool {
	// Go's operators, which are IEEE's for floats: every comparison with NaN is
	// false except !=.
	switch op {
	case expr.OpEq:
		return x == y
	case expr.OpNe:
		return x != y
	case expr.OpLt:
		return x < y
	case expr.OpLe:
		return x <= y
	case expr.OpGt:
		return x > y
	default:
		return x >= y
	}
}

func TestCompareWordsAtEveryBoundary(t *testing.T) {
	r := rand.New(rand.NewPCG(107, 1))
	for _, n := range []int{1, 2, 63, 64, 65, 127, 128, 129, 1000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			i8a, i8b := make([]int8, n), make([]int8, n)
			i64a, i64b := make([]int64, n), make([]int64, n)
			u16a, u16b := make([]uint16, n), make([]uint16, n)
			f32a, f32b := make([]float32, n), make([]float32, n)
			f64a, f64b := make([]float64, n), make([]float64, n)
			for i := range n {
				// Few distinct values, so equality is common, and the extremes.
				i8a[i], i8b[i] = int8(r.IntN(5)-2), int8(r.IntN(5)-2)
				if i%17 == 0 {
					i8a[i] = math.MinInt8
				}
				i64a[i], i64b[i] = r.Int64N(7)-3, r.Int64N(7)-3
				if i%19 == 0 {
					i64b[i] = math.MaxInt64
				}
				u16a[i], u16b[i] = uint16(r.IntN(4)), uint16(r.IntN(4))
				f32a[i], f32b[i] = float32(r.IntN(5)-2), float32(r.IntN(5)-2)
				f64a[i], f64b[i] = float64(r.IntN(5)-2)/2, float64(r.IntN(5)-2)/2
				if i%7 == 3 {
					f32a[i], f64b[i] = float32(math.NaN()), math.NaN()
				}
			}
			checkCompare(t, dtype.Int8, i8a, i8b, ordered[int8])
			checkCompare(t, dtype.Int64, i64a, i64b, ordered[int64])
			checkCompare(t, dtype.Uint16, u16a, u16b, ordered[uint16])
			checkCompare(t, dtype.Float32, f32a, f32b, ordered[float32])
			checkCompare(t, dtype.Float64, f64a, f64b, ordered[float64])
		})
	}
}
