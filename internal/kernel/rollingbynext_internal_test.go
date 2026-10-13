package kernel

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// TestARollingWindowByTimeStreamedInPiecesIsRollingBy: a rolling function by time
// walked with RollingByNext over a partition in its by order, cut between runs of
// equal by values into pieces of one run to a few hundred rows, is rollingBy over the
// whole of it, to the bit (step 246).
//
// Sum, mean, var, std, min, max, median and a quantile; of floats with nulls, NaN,
// ±Inf, ±0 and values whose sums round, of integers summed as Int128, and of strings
// for min and max. By a Datetime with long runs of ties and nulls, over periods of
// about two hundred rows and twelve hundred, and a calendar month over day-apart
// rows, closed each way, wanting one value and five.
func TestARollingWindowByTimeStreamedInPiecesIsRollingBy(t *testing.T) {
	rng := rand.New(rand.NewPCG(246, 1))
	const n = 2500
	step := map[string]int64{"dense": 300_000, "days": 86_400_000_000}
	for _, spacing := range []string{"dense", "days"} {
		ticks := make([]int64, n)
		byOK := bitmap.NewBuilder(n)
		tick := int64(1_700_000_000_000_000)
		for i := range n {
			if rng.IntN(3) != 0 {
				tick += 1 + rng.Int64N(step[spacing])
			}
			ticks[i] = tick
			byOK.Append(rng.IntN(25) != 0)
		}
		by := data.NewFixed("1", dtype.Datetime(dtype.Micro, ""), ticks, byOK.Finish())

		pool := []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, math.Copysign(0, -1)}
		fv, iv, sv := make([]float64, n), make([]int64, n), make([]string, n)
		fok, iok := bitmap.NewBuilder(n), bitmap.NewBuilder(n)
		for i := range n {
			fv[i] = rng.NormFloat64() * 1e3 / 3
			if rng.IntN(40) == 0 {
				fv[i] = pool[rng.IntN(len(pool))]
			}
			iv[i] = rng.Int64N(1<<40) - 1<<39
			sv[i] = fmt.Sprintf("s%02d", rng.IntN(60))
			fok.Append(rng.IntN(9) != 0)
			iok.Append(rng.IntN(9) != 0)
		}
		values := map[string]*data.Column{
			"float":  data.NewFixed("0", dtype.Float64, fv, fok.Finish()),
			"int":    data.NewFixed("0", dtype.Int64, iv, iok.Finish()),
			"string": data.NewString("0", sv, bitmap.AllSet(n)),
		}
		// About two hundred rows a window, and twelve hundred, of the stream's 2,500;
		// and a month of the day-apart rows, about ninety.
		periods := map[string]dtype.Interval{"dense": dtype.Every("20s"), "days": dtype.Every("1mo")}
		if spacing == "dense" {
			periods["wide"] = dtype.Every("2m")
		}
		seg := Segments{Perm: make([]int32, n), Bounds: []int32{0, n}}
		for i := range seg.Perm {
			seg.Perm[i] = int32(i)
		}

		// The types the planner gives: a sum of integers is an Int128, min and max
		// their value's type, and every other a Float64.
		type fnCase struct {
			fn     expr.WinFnOp
			values []string
		}
		fns := []fnCase{
			{expr.WinRollingSum, []string{"float", "int"}},
			{expr.WinRollingMean, []string{"float", "int"}},
			{expr.WinRollingVar, []string{"float"}},
			{expr.WinRollingStd, []string{"float"}},
			{expr.WinRollingMin, []string{"float", "string"}},
			{expr.WinRollingMax, []string{"float", "int", "string"}},
			{expr.WinRollingMedian, []string{"float"}},
			{expr.WinRollingQuantile, []string{"int"}},
		}
		for pname, period := range periods {
			for _, closed := range []expr.Closed{expr.ClosedRight, expr.ClosedLeft, expr.ClosedBoth, expr.ClosedNone} {
				for _, minN := range []int64{1, 5} {
					for _, fc := range fns {
						for _, vname := range fc.values {
							value := values[vname]
							out := dtype.Float64
							switch {
							case fc.fn == expr.WinRollingMin || fc.fn == expr.WinRollingMax:
								out = value.DType()
							case fc.fn == expr.WinRollingSum && vname == "int":
								out = dtype.Int128
							}
							params := expr.WinParams{By: true, Period: period, Closed: closed, MinSamples: minN,
								Ddof: 1, Q: 0.3, Interp: expr.InterpLinear}
							pair := data.NewStruct("p", []*data.Column{value, by}, bitmap.AllSet(n))
							name := fmt.Sprintf("%s %s, %s %s, closed %d, min %d", fc.fn, vname, spacing, pname, closed, minN)
							want, err := rollingBy(fc.fn, params, "r", out, pair, seg)
							if err != nil {
								t.Fatalf("%s: %v", name, err)
							}
							var st RollingByState
							var got []*data.Column
							for lo := 0; lo < n; {
								hi := min(n, lo+1+rng.IntN(300))
								// Cut between runs of equal by values: the run at the
								// cut goes on into this piece.
								for hi < n && ticks[hi] == ticks[hi-1] {
									hi++
								}
								c, err := RollingByNext(fc.fn, params, "r", out, pair.Slice(lo, hi-lo), &st)
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
	}
}

// TestARollingWalkByTimeRefusesASplitRun: a run of equal by values split between
// two batches would answer its first rows without the rest, so it is refused.
func TestARollingWalkByTimeRefusesASplitRun(t *testing.T) {
	by := data.NewFixed("1", dtype.Datetime(dtype.Micro, ""), []int64{1, 2, 2, 3}, bitmap.AllSet(4))
	v := data.NewFixed("0", dtype.Float64, []float64{1, 2, 3, 4}, bitmap.AllSet(4))
	pair := data.NewStruct("p", []*data.Column{v, by}, bitmap.AllSet(4))
	params := expr.WinParams{By: true, Period: dtype.Every("1s"), MinSamples: 1}
	var st RollingByState
	if _, err := RollingByNext(expr.WinRollingSum, params, "r", dtype.Float64, pair.Slice(0, 2), &st); err != nil {
		t.Fatal(err)
	}
	if _, err := RollingByNext(expr.WinRollingSum, params, "r", dtype.Float64, pair.Slice(2, 2), &st); err == nil {
		t.Error("a run of equal by values split between batches was walked")
	}
}

// assertSameColumn fails unless got and want hold the same values, bit for bit, and
// the same nulls.
func assertSameColumn(t *testing.T, name string, got, want *data.Column) {
	t.Helper()
	if got.Len() != want.Len() || got.DType() != want.DType() {
		t.Fatalf("%s: %d rows of %s, want %d of %s", name, got.Len(), got.DType(), want.Len(), want.DType())
	}
	gv, wv := got.Validity(), want.Validity()
	for i := range got.Len() {
		if gv.Get(i) != wv.Get(i) {
			t.Fatalf("%s: row %d is valid %v, want %v", name, i, gv.Get(i), wv.Get(i))
		}
		if !gv.Get(i) {
			continue
		}
		g, w := cellBits(got, i), cellBits(want, i)
		if g != w {
			t.Fatalf("%s: row %d is %s, want %s", name, i, g, w)
		}
	}
}

// cellBits is row i of c, floats by their bits.
func cellBits(c *data.Column, i int) string {
	switch c.DType().Physical().ID() {
	case dtype.TypeFloat64:
		return fmt.Sprintf("%#x", math.Float64bits(data.MustValues[float64](c)[i]))
	case dtype.TypeString:
		return string(c.Strings().Bytes(i))
	case dtype.TypeInt64:
		return fmt.Sprint(data.MustValues[int64](c)[i])
	case dtype.TypeInt128:
		return data.MustValues[i128.Int128](c)[i].String()
	}
	return fmt.Sprintf("a %s", c.DType())
}
