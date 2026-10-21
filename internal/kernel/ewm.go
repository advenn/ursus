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
//   - A row's answer is the running value once MinSamples values have been seen;
//     MinSamples is one unless set. A null row's answer is null, as Polars' is
//     (step 184, found by the Polars oracle); it answered the running value, as
//     pandas does, until then.
//   - ewm_var's unbiased estimate needs more weight than one value's, so a first
//     value's is null where pandas answers NaN.
//
// A NaN is a value, not a gap: it makes the running value NaN from there on, as
// IEEE arithmetic would. pandas reads NaN as missing.
func winEwm(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments) (*data.Column, error) {

	if params.By {
		return winEwmBy(fn, params, name, out, col, seg)
	}
	e, err := newEwmRun(fn, params, col)
	if err != nil {
		return nil, err
	}
	forEachOrdered(seg, false, func(rows []int32) {
		var st EwmState
		for _, row := range rows {
			e.step(&st, int(row))
		}
	})
	return floatsAs(name, out, e.res, seenBitmap(e.ok, len(e.ok))), nil
}

// EwmState is what an ewm function keeps between the rows of one partition, so a
// window streaming its one partition carries it between batches (step 221). Its zero
// value is a partition's start.
type EwmState struct {
	seen                 bool
	nobs                 int
	mean, cov            float64
	oldWt, sumWt, sumWt2 float64
	// sum is ewm_sum's running sum; prevTime is the last time of an ewm by time,
	// whose last answer is mean (step 285).
	sum      float64
	prevTime int64
}

// EwmNext answers col, the next rows of a partition in its order, continuing st.
func EwmNext(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, st *EwmState) (*data.Column, error) {

	if params.By {
		// A sorted pass's stream, already in by's order (winsorted.go).
		e, err := newEwmByRun(fn, params, out, col)
		if err != nil {
			return nil, err
		}
		for row := range col.Len() {
			e.step(st, row)
		}
		return floatsAs(name, out, e.res, seenBitmap(e.ok, len(e.ok))), nil
	}
	e, err := newEwmRun(fn, params, col)
	if err != nil {
		return nil, err
	}
	for row := range col.Len() {
		e.step(st, row)
	}
	return floatsAs(name, out, e.res, seenBitmap(e.ok, len(e.ok))), nil
}

// ewmRun is one ewm function over one column: its parameters, its input, and its
// answers so far.
type ewmRun struct {
	fn          expr.WinFnOp
	params      expr.WinParams
	minN        int
	f32         bool // ewm_sum of a Float32 is computed in float32, as Polars'
	keep, newWt float64
	vals        []float64
	valid       bitmap.View
	res         []float64
	ok          []bool
}

func newEwmRun(fn expr.WinFnOp, params expr.WinParams, col *data.Column) (*ewmRun, error) {
	alpha := params.Alpha
	if !(alpha > 0 && alpha <= 1) {
		return nil, uerr.Internalf("kernel: %s with alpha %v", fn, alpha)
	}
	col, err := readable(col)
	if err != nil {
		return nil, err
	}
	vals, err := toFloat64(col)
	if err != nil {
		return nil, err
	}
	e := &ewmRun{fn: fn, params: params, minN: max(int(params.MinSamples), 1),
		f32: col.DType() == dtype.Float32, keep: 1 - alpha, newWt: 1, vals: vals, valid: col.Validity(),
		res: make([]float64, col.Len()), ok: make([]bool, col.Len())}
	if params.NoAdjust {
		e.newWt = alpha
	}
	return e, nil
}

// step takes row into st and answers it.
func (e *ewmRun) step(st *EwmState, row int) {
	x, obs := e.vals[row], e.valid.Get(row)
	if e.fn == expr.WinEwmSum {
		e.sumStep(st, row, x, obs)
		return
	}
	keep, newWt := e.keep, e.newWt
	switch {
	case !st.seen && obs:
		st.seen, st.mean, st.cov = true, x, 0
		st.oldWt, st.sumWt, st.sumWt2 = 1, 1, 1
	case st.seen && (obs || !e.params.IgnoreNulls):
		st.sumWt *= keep
		st.sumWt2 *= keep * keep
		st.oldWt *= keep
		if obs {
			prev := st.mean
			// pandas' guard: a constant run keeps its exact mean.
			if st.mean != x {
				st.mean = (st.oldWt*prev + newWt*x) / (st.oldWt + newWt)
			}
			st.cov = (st.oldWt*(st.cov+(prev-st.mean)*(prev-st.mean)) +
				newWt*(x-st.mean)*(x-st.mean)) / (st.oldWt + newWt)
			st.sumWt += newWt
			st.sumWt2 += newWt * newWt
			st.oldWt += newWt
			if e.params.NoAdjust {
				st.sumWt /= st.oldWt
				st.sumWt2 /= st.oldWt * st.oldWt
				st.oldWt = 1
			}
		}
	}
	if obs {
		st.nobs++
	}
	if !obs || !st.seen || st.nobs < e.minN {
		return
	}
	switch e.fn {
	case expr.WinEwmMean:
		e.res[row], e.ok[row] = st.mean, true
	default:
		v := st.cov
		if !e.params.Bias {
			num := st.sumWt * st.sumWt
			den := num - st.sumWt2
			if !(den > 0) {
				return
			}
			v = num / den * st.cov
		}
		if e.fn == expr.WinEwmStd {
			v = math.Sqrt(v)
		}
		e.res[row], e.ok[row] = v, true
	}
}

// sumStep is Polars' EwmSumState (crates/polars-compute/src/ewm/sum.rs, step 285): a
// value is the running sum decayed by 1−alpha, plus itself, the first its value; a
// null decays the sum, but with IgnoreNulls; a row answers once MinSamples values
// have been seen, and a null row is null. A Float32 is summed in float32.
func (e *ewmRun) sumStep(st *EwmState, row int, x float64, obs bool) {
	keep := e.keep
	if e.f32 {
		keep = float64(1 - float32(e.params.Alpha))
	}
	decay := func(v float64) float64 {
		if e.f32 {
			return float64(float32(v) * float32(keep))
		}
		return v * keep
	}
	switch {
	case obs && st.nobs == 0:
		st.sum = x
	case obs && e.f32:
		st.sum = float64(float32(decay(st.sum)) + float32(x))
	case obs:
		st.sum = float64(st.sum*keep) + x
	case st.nobs > 0 && !e.params.IgnoreNulls:
		st.sum = decay(st.sum)
	}
	if !obs {
		return
	}
	if st.nobs++; st.nobs >= e.minN {
		e.res[row], e.ok[row] = st.sum, true
	}
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
	if out.IsTemporal() {
		return winInterpolateTicks(name, out, col, seg)
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
