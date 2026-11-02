package kernel

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Step 433: guards behind the planner, each recorded silent because no public query
// reaches it, held by calling the kernel directly.

// TestARankOrInterpolationTheKernelDoesNotKnowIsInternal: ResolveWinFn and Agg.Field
// refuse an undeclared rank method and interpolation before a kernel sees one. Past
// them, the rank kernel's default wrote the average arm into a buffer that did not
// exist, and the quantile's answered linearly; each is an internal error (step 72).
func TestARankOrInterpolationTheKernelDoesNotKnowIsInternal(t *testing.T) {
	col := data.NewFixed("v", dtype.Int64, []int64{3, 1, 2, 1}, bitmap.AllSet(4))
	seg := Segments{Perm: []int32{0, 1, 2, 3}, Bounds: []int32{0, 4}}
	_, err := winRank(expr.WinParams{Method: expr.RankMethod(99)}, "r", dtype.Uint32, col, seg)
	if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "rank method 99") {
		t.Errorf("rank method 99: %v, want an internal error naming it", err)
	}

	func() {
		defer func() {
			v := recover()
			e, ok := v.(*uerr.Error)
			if !ok || e.Kind != uerr.KindInternal || !strings.Contains(e.Error(), "interpolation 99") {
				t.Errorf("interpolation 99: recovered %v, want an internal error naming it", v)
			}
		}()
		got := interpolate(1, 3, 0.5, expr.Interpolation(99))
		t.Errorf("interpolation 99 answered %v", got)
	}()
}

// TestAPayloadFreeOperandIsRead: a column of nulls with no payload, as an aggregate
// over nothing is, is given one to read by cut and by a bit count, as every operand is
// (take.go's NullColumn); each answers nulls. No public query hands them one, an
// aggregate's and a typed null literal's coming with a payload (step 147).
func TestAPayloadFreeOperandIsRead(t *testing.T) {
	const n = 5
	got, err := bitCount(expr.FnMathCountOnes, "b", data.NewNull("x", dtype.Int32, n))
	if err != nil || got.Len() != n || got.NullCount() != n {
		t.Errorf("a bit count of %d payload-free nulls: %v, %v", n, got, err)
	}
	one := data.NewFixed("b1", dtype.Float64, []float64{1, 1, 1, 1, 1}, bitmap.AllSet(n))
	two := data.NewFixed("b2", dtype.Float64, []float64{2, 2, 2, 2, 2}, bitmap.AllSet(n))
	got, err = cut("c", false, []*data.Column{data.NewNull("x", dtype.Float64, n), one, two}, n)
	if err != nil || got.Len() != n || got.NullCount() != n {
		t.Errorf("cut of %d payload-free nulls: %v, %v", n, got, err)
	}
}

// TestACumulativeProductNeverAdds: winCumInt128 ignores its function and adds, so the
// gate sending an exact output there is cum_sum's alone. No exact product binds
// today, so the half `fn == WinCumSum` of the gate is a trap closed rather than a
// defect (step 62): a cum_prod to an Int128, a Decimal or a Duration is never the
// running sum, whatever else it is.
func TestACumulativeProductNeverAdds(t *testing.T) {
	vals := []int64{2, 3, 4, 5}
	col := data.NewFixed("v", dtype.Int64, vals, bitmap.AllSet(len(vals)))
	seg := Segments{Perm: []int32{0, 1, 2, 3}, Bounds: []int32{0, 4}}
	for _, out := range []dtype.DataType{dtype.Int128, dtype.Decimal(20, 0), dtype.Duration(dtype.Micro)} {
		res, err := func() (c *data.Column, err error) {
			defer func() {
				if v := recover(); v != nil {
					err = uerr.FromPanic(v, "test")
				}
			}()
			return winCumulative(expr.WinCumProd, expr.WinParams{}, "p", out, col, seg)
		}()
		if err != nil || res == nil || res.Len() != len(vals) {
			continue // refused, or not built: not a sum
		}
		sums := []int64{2, 5, 9, 14}
		added := true
		for i, want := range sums {
			switch res.DType().Physical().ID() {
			case dtype.TypeInt128:
				if data.MustValues[i128.Int128](res)[i] != i128.FromInt64(want) {
					added = false
				}
			case dtype.TypeInt64:
				if data.MustValues[int64](res)[i] != want {
					added = false
				}
			default:
				added = false
			}
		}
		if added {
			t.Errorf("cum_prod to %s is the running sum", out)
		}
	}
}
