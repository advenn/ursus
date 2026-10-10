package kernel

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// The exponentially weighted functions and Interpolate (step 153), ordered window
// functions over the window's Segments.
//
// # The recurrences are pandas'
//
// ewm_mean is pandas' ewma, and ewm_var pandas' ewmcov of a column with itself,
// which Polars' follow: with a decay alpha, each row's weight is (1-alpha) to the
// power of how far behind the current row it is, and adjust=False weighs the
// newest row by alpha and everything before by 1-alpha instead. So their answers
// are the ones a user of either computes:
//
//   - A null is no observation. Without IgnoreNulls it still ages the weights
//     before it, as a row does; with it, the weights count values only.
//   - A row's answer is the running value, a null row's too, once MinSamples values
//     have been seen; MinSamples is one unless set.
//   - ewm_var's unbiased estimate needs more weight than one value's, so a first
//     value's is null where pandas answers NaN.
//
// A NaN is a value, not a gap: it makes the running value NaN from there on, as
// IEEE arithmetic would. pandas reads NaN as missing.
func winEwm(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments) (*data.Column, error) {

	alpha := params.Alpha
	if !(alpha > 0 && alpha <= 1) {
		return nil, uerr.Internalf("kernel: %s with alpha %v", fn, alpha)
	}
	minN := max(int(params.MinSamples), 1)
	col, err := readable(col)
	if err != nil {
		return nil, err
	}
	vals, err := toFloat64(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]float64, col.Len())
	ok := make([]bool, col.Len())
	keep := 1 - alpha
	newWt := 1.0
	if params.NoAdjust {
		newWt = alpha
	}
	forEachOrdered(seg, false, func(rows []int32) {
		var (
			seen                 bool
			nobs                 int
			mean, cov            float64
			oldWt, sumWt, sumWt2 = 1.0, 1.0, 1.0
		)
		for _, row := range rows {
			x, obs := vals[row], valid.Get(int(row))
			switch {
			case !seen && obs:
				seen, mean, cov = true, x, 0
				oldWt, sumWt, sumWt2 = 1, 1, 1
			case seen && (obs || !params.IgnoreNulls):
				sumWt *= keep
				sumWt2 *= keep * keep
				oldWt *= keep
				if obs {
					prev := mean
					// pandas' guard: a constant run keeps its exact mean.
					if mean != x {
						mean = (oldWt*prev + newWt*x) / (oldWt + newWt)
					}
					cov = (oldWt*(cov+(prev-mean)*(prev-mean)) + newWt*(x-mean)*(x-mean)) /
						(oldWt + newWt)
					sumWt += newWt
					sumWt2 += newWt * newWt
					oldWt += newWt
					if params.NoAdjust {
						sumWt /= oldWt
						sumWt2 /= oldWt * oldWt
						oldWt = 1
					}
				}
			}
			if obs {
				nobs++
			}
			if !seen || nobs < minN {
				continue
			}
			switch fn {
			case expr.WinEwmMean:
				res[row], ok[row] = mean, true
			default:
				v := cov
				if !params.Bias {
					num := sumWt * sumWt
					den := num - sumWt2
					if !(den > 0) {
						continue
					}
					v = num / den * cov
				}
				if fn == expr.WinEwmStd {
					v = math.Sqrt(v)
				}
				res[row], ok[row] = v, true
			}
		}
	})
	return floatsAs(name, out, res, seenBitmap(ok, len(ok))), nil
}

// winInterpolate fills each run of nulls between two values with the points on the
// straight line between them, by position in the window's order, as Polars'
// interpolate does. A run before the first value or after the last stays null,
// having no line to lie on.
func winInterpolate(name string, out dtype.DataType, col *data.Column, seg Segments) (*data.Column, error) {
	col, err := readable(col)
	if err != nil {
		return nil, err
	}
	vals, err := toFloat64(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]float64, col.Len())
	ok := make([]bool, col.Len())
	forEachOrdered(seg, false, func(rows []int32) {
		last := -1 // the position of the last value seen
		for p, row := range rows {
			if !valid.Get(int(row)) {
				continue
			}
			res[row], ok[row] = vals[row], true
			if last >= 0 && p-last > 1 {
				a, b := vals[rows[last]], vals[row]
				span := float64(p - last)
				for q := last + 1; q < p; q++ {
					t := float64(q-last) / span
					res[rows[q]], ok[rows[q]] = a+(b-a)*t, true
				}
			}
			last = p
		}
	})
	return floatsAs(name, out, res, seenBitmap(ok, len(ok))), nil
}

// winInterpolateNearest fills each run of nulls between two values with the nearer
// of the two by position, the later on a tie, as Polars' interpolate(method=
// "nearest") does (step 172). A run before the first value or after the last stays
// null. It answers rows of the column, so one Take keeps its type.
func winInterpolateNearest(name string, col *data.Column, seg Segments) (*data.Column, error) {
	col, err := readable(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	sel := make([]int32, col.Len())
	for i := range sel {
		sel[i] = NullIndex
	}
	forEachOrdered(seg, false, func(rows []int32) {
		last := -1 // the position of the last value seen
		for p, row := range rows {
			if !valid.Get(int(row)) {
				continue
			}
			sel[row] = row
			if last >= 0 {
				for q := last + 1; q < p; q++ {
					// The later value wins a tie: nearer to it, or as near.
					if p-q <= q-last {
						sel[rows[q]] = row
					} else {
						sel[rows[q]] = rows[last]
					}
				}
			}
			last = p
		}
	})
	out, err := Take(col, sel)
	if err != nil {
		return nil, err
	}
	return out.Rename(name), nil
}

// floatsAs stores float64 answers as out, a Float32 or a Float64.
func floatsAs(name string, out dtype.DataType, res []float64, valid bitmap.View) *data.Column {
	if out == dtype.Float32 {
		f32 := make([]float32, len(res))
		for i, v := range res {
			f32[i] = float32(v)
		}
		return data.NewFixed(name, out, f32, valid)
	}
	return data.NewFixed(name, out, res, valid)
}
