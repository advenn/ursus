package kernel

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// The rolling functions (step 149): each row's answer over the N rows of its
// partition up to and including it, in the window's order, or null where fewer than
// MinSamples of them hold a value — Polars' rolling_* with a fixed window_size and
// center=False.
//
// Each is O(1) a row: a value enters as its row is reached and leaves N rows later.
//
//   - min and max keep a monotonic deque of the window's rows, and answer by Take,
//     so every type with an order works, in the order Sort uses: NaN above every
//     number, as Max of a column holding one is NaN;
//   - an integer sum is an Int128, added to and subtracted from exactly;
//   - a float sum, mean, var and std keep the window's finite values' compensated
//     sum and Welford state, and count its NaNs and infinities apart, so an Inf that
//     has left the window leaves no NaN behind. Subtracting rounds, so the state is
//     computed afresh from the window every N removals, which keeps the error that
//     of at most 2N steps however long the partition.
func winRolling(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments) (*data.Column, error) {

	if params.Center {
		return rollingCentered(fn, params, name, out, col, seg)
	}
	size, minN := int(params.N), int(params.MinSamples)
	if minN == 0 {
		minN = size
	}
	if size < 1 || minN < 1 || minN > size {
		return nil, uerr.Internalf("kernel: %s over %d rows needing %d", fn, size, minN)
	}
	col, err := readable(col)
	if err != nil {
		return nil, err
	}
	switch {
	case fn == expr.WinRollingMin || fn == expr.WinRollingMax:
		return rollingExtremum(fn == expr.WinRollingMax, name, col, seg, size, minN)
	case fn == expr.WinRollingSum && out.Physical().ID() == dtype.TypeInt128:
		return rollingIntSum(name, out, col, seg, size, minN)
	}
	return rollingFloat(fn, int(params.Ddof), name, out, col, seg, size, minN)
}

// rollingCentered is a rolling window labelled at its middle row, as Polars'
// center=True (step 172): a window of size rows covers size/2 before the row and
// (size-1)/2 after. That is the trailing window ending (size-1)/2 rows later, so each
// partition is padded with that many null rows after its last, the trailing kernel
// runs unchanged, and each row takes the answer from those rows on. A padded row is
// null, so it counts as no value, which is Polars' partial window at the end.
func rollingCentered(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments) (*data.Column, error) {

	params.Center = false
	k := (int(params.N) - 1) / 2
	if k == 0 {
		return winRolling(fn, params, name, out, col, seg)
	}
	col, err := readable(col)
	if err != nil {
		return nil, err
	}
	parts, n := len(seg.Bounds)-1, col.Len()
	pad, err := NullColumn(col.Name(), col.DType(), parts*k)
	if err != nil {
		return nil, err
	}
	ext, err := concatColumn([]*data.Column{col, pad}, n+parts*k)
	if err != nil {
		return nil, err
	}
	perm := make([]int32, 0, len(seg.Perm)+parts*k)
	bounds := make([]int32, 1, parts+1)
	next := int32(n)
	for p := range parts {
		perm = append(perm, seg.Perm[seg.Bounds[p]:seg.Bounds[p+1]]...)
		for range k {
			perm = append(perm, next)
			next++
		}
		bounds = append(bounds, int32(len(perm)))
	}
	res, err := winRolling(fn, params, name, out, ext, Segments{Perm: perm, Bounds: bounds})
	if err != nil {
		return nil, err
	}
	sel := make([]int32, n)
	for i := range sel {
		sel[i] = NullIndex
	}
	for p := range parts {
		rows := seg.Perm[seg.Bounds[p]:seg.Bounds[p+1]]
		for i, row := range rows {
			sel[row] = perm[int(bounds[p])+i+k]
		}
	}
	return Take(res, sel)
}

// rollingExtremum answers each row with the window's least or greatest row, kept at
// the front of a deque whose rows only worsen toward the back: a row that can never
// be the answer again — a better one has arrived after it — is dropped as it does.
func rollingExtremum(max bool, name string, col *data.Column, seg Segments, size, minN int) (*data.Column, error) {
	cmp, err := valueComparator(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	sel := make([]int32, col.Len())
	var deque []int // positions in the partition
	forEachOrdered(seg, false, func(rows []int32) {
		deque = deque[:0]
		count := 0
		for i, row := range rows {
			if valid.Get(int(row)) {
				count++
				for len(deque) > 0 {
					c := cmp(int(rows[deque[len(deque)-1]]), int(row))
					if (max && c > 0) || (!max && c < 0) {
						break
					}
					deque = deque[:len(deque)-1]
				}
				deque = append(deque, i)
			}
			if i >= size {
				if valid.Get(int(rows[i-size])) {
					count--
				}
				if len(deque) > 0 && deque[0] <= i-size {
					deque = deque[1:]
				}
			}
			sel[row] = NullIndex
			if count >= minN {
				sel[row] = rows[deque[0]]
			}
		}
	})
	res, err := Take(col, sel)
	if err != nil {
		return nil, err
	}
	return res.Rename(name), nil
}

// rollingIntSum sums an integer or Bool window exactly, as an Int128: N values of 64
// bits cannot leave 128.
func rollingIntSum(name string, out dtype.DataType, col *data.Column, seg Segments, size, minN int) (*data.Column, error) {
	vals, err := widenToInt128(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]i128.Int128, col.Len())
	ok := make([]bool, col.Len())
	forEachOrdered(seg, false, func(rows []int32) {
		var sum i128.Int128
		count := 0
		for i, row := range rows {
			if valid.Get(int(row)) {
				sum, count = sum.Add(vals[row]), count+1
			}
			if i >= size {
				if old := rows[i-size]; valid.Get(int(old)) {
					sum, count = sum.Sub(vals[old]), count-1
				}
			}
			if count >= minN {
				res[row], ok[row] = sum, true
			}
		}
	})
	return data.NewFixed(name, out, res, seenBitmap(ok, len(ok))), nil
}

// rollingState is a float window's running state: its finite values' count,
// compensated sum, and Welford mean and sum of squared deviations, and how many
// NaNs and infinities of each sign it holds.
type rollingState struct {
	n               int
	sum, comp       float64 // Neumaier's
	mean, m2        float64 // Welford's
	nan, pinf, ninf int
}

func (s *rollingState) add(x float64) {
	switch {
	case math.IsNaN(x):
		s.nan++
		return
	case math.IsInf(x, 1):
		s.pinf++
		return
	case math.IsInf(x, -1):
		s.ninf++
		return
	}
	s.n++
	s.neumaier(x)
	d := x - s.mean
	s.mean += d / float64(s.n)
	s.m2 += d * (x - s.mean)
}

func (s *rollingState) remove(x float64) {
	switch {
	case math.IsNaN(x):
		s.nan--
		return
	case math.IsInf(x, 1):
		s.pinf--
		return
	case math.IsInf(x, -1):
		s.ninf--
		return
	}
	s.n--
	if s.n == 0 {
		s.sum, s.comp, s.mean, s.m2 = 0, 0, 0, 0
		return
	}
	s.neumaier(-x)
	d := x - s.mean
	s.mean -= d / float64(s.n)
	s.m2 -= d * (x - s.mean)
}

// neumaier adds x to the compensated sum.
func (s *rollingState) neumaier(x float64) {
	t := s.sum + x
	if math.Abs(s.sum) >= math.Abs(x) {
		s.comp += (s.sum - t) + x
	} else {
		s.comp += (x - t) + s.sum
	}
	s.sum = t
}

// nonFinite is the window's answer when it holds a NaN or an infinity, as IEEE
// arithmetic over its values would give it: NaN for a NaN or both infinities, else
// the one infinity. ok is false when it holds neither.
func (s *rollingState) nonFinite() (float64, bool) {
	switch {
	case s.nan > 0 || (s.pinf > 0 && s.ninf > 0):
		return math.NaN(), true
	case s.pinf > 0:
		return math.Inf(1), true
	case s.ninf > 0:
		return math.Inf(-1), true
	}
	return 0, false
}

// rollingFloat computes a float sum, mean, var or std over each row's window.
func rollingFloat(fn expr.WinFnOp, ddof int, name string, out dtype.DataType, col *data.Column,
	seg Segments, size, minN int) (*data.Column, error) {

	vals, err := toFloat64(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]float64, col.Len())
	ok := make([]bool, col.Len())
	forEachOrdered(seg, false, func(rows []int32) {
		var s rollingState
		count, removed := 0, 0
		for i, row := range rows {
			if valid.Get(int(row)) {
				s.add(vals[row])
				count++
			}
			if i >= size {
				if old := rows[i-size]; valid.Get(int(old)) {
					s.remove(vals[old])
					count--
					if removed++; removed == size {
						// Afresh from the window, so rounding cannot build up.
						s = rollingState{}
						for _, r := range rows[i-size+1 : i+1] {
							if valid.Get(int(r)) {
								s.add(vals[r])
							}
						}
						removed = 0
					}
				}
			}
			if count < minN {
				continue
			}
			v, answered := rollingAnswer(fn, ddof, &s)
			res[row], ok[row] = v, answered
		}
	})
	valid32 := seenBitmap(ok, len(ok))
	if out == dtype.Float32 {
		f32 := make([]float32, len(res))
		for i, v := range res {
			f32[i] = float32(v)
		}
		return data.NewFixed(name, out, f32, valid32), nil
	}
	return data.NewFixed(name, out, res, valid32), nil
}

// rollingAnswer is fn over a window's state. A variance of no more values than ddof
// is null, as Var's is, whatever the values; past that, a NaN or an infinity makes
// it NaN.
func rollingAnswer(fn expr.WinFnOp, ddof int, s *rollingState) (float64, bool) {
	variance := fn == expr.WinRollingVar || fn == expr.WinRollingStd
	if variance && s.n+s.nan+s.pinf+s.ninf <= ddof {
		return 0, false
	}
	if v, ok := s.nonFinite(); ok {
		if variance {
			return math.NaN(), true
		}
		return v, true
	}
	switch fn {
	case expr.WinRollingSum:
		return s.sum + s.comp, true
	case expr.WinRollingMean:
		return (s.sum + s.comp) / float64(s.n), true
	}
	v := max(s.m2, 0) / float64(s.n-ddof)
	if fn == expr.WinRollingStd {
		v = math.Sqrt(v)
	}
	return v, true
}
