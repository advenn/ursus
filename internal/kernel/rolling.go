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
//
// # A window is a range of the partition's rows (step 177)
//
// Each kernel walks a partition's rows and gives row i the rows [lo_i, hi_i), with
// lo and hi never falling as i rises: a row enters as hi passes it and leaves as lo
// does. A window of N rows is lo_i = i−N+1, hi_i = i+1. A window by time is the rows
// whose by value lies within the period of row i's, in by's order (rollingBy), so
// hi_i may pass i, where later rows tie with it.
func winRolling(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments) (*data.Column, error) {

	if params.By {
		return rollingBy(fn, params, name, out, col, seg)
	}
	size, minN := int(params.N), int(params.MinSamples)
	if minN == 0 {
		minN = size
	}
	if size < 1 || minN < 1 || minN > size {
		return nil, uerr.Internalf("kernel: %s over %d rows needing %d", fn, size, minN)
	}
	if params.Center {
		if fn == expr.WinRollingRank {
			// A rank is of the row's own value, so its window is centred on it, not
			// a trailing window's answer taken from a later row (step 284).
			return rollingOver(fn, params, name, out, col, seg, centeredWindows(size), minN)
		}
		return rollingCentered(fn, params, name, out, col, seg)
	}
	trailing := func(rows []int32) (lo, hi []int32) {
		lo, hi = make([]int32, len(rows)), make([]int32, len(rows))
		for i := range rows {
			lo[i], hi[i] = int32(max(0, i-size+1)), int32(i+1)
		}
		return lo, hi
	}
	return rollingOver(fn, params, name, out, col, seg, trailing, minN)
}

// windowsOf gives each position of a partition's rows its window, rows[lo[i]:hi[i]].
type windowsOf func(rows []int32) (lo, hi []int32)

// rollingOver runs fn's kernel over the windows win gives.
func rollingOver(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments, win windowsOf, minN int) (*data.Column, error) {

	col, err := readable(col)
	if err != nil {
		return nil, err
	}
	switch {
	case fn == expr.WinRollingMin || fn == expr.WinRollingMax:
		return rollingExtremum(fn == expr.WinRollingMax, name, col, seg, win, minN)
	case fn == expr.WinRollingSum && out.Physical().ID() == dtype.TypeInt128:
		return rollingIntSum(name, out, col, seg, win, minN)
	case fn == expr.WinRollingMedian || fn == expr.WinRollingQuantile:
		return rollingQuantile(fn, params, name, out, col, seg, win, minN)
	case fn == expr.WinRollingSkew || fn == expr.WinRollingKurtosis:
		return rollingMoment(fn, params, name, out, col, seg, win, minN)
	case fn == expr.WinRollingRank:
		return rollingRank(params, name, out, col, seg, win, minN)
	case fn == expr.WinRollingCov || fn == expr.WinRollingCorr:
		return rollingCovCorr(fn, params, name, out, col, seg, win, minN)
	}
	return rollingFloat(fn, int(params.Ddof), name, out, col, seg, win, minN)
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
func rollingExtremum(max bool, name string, col *data.Column, seg Segments, win windowsOf, minN int) (*data.Column, error) {
	cmp, err := valueComparator(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	// A row no window walk reaches, one whose by is null, is null.
	sel := make([]int32, col.Len())
	for i := range sel {
		sel[i] = NullIndex
	}
	var deque []int // positions in the partition
	forEachOrdered(seg, false, func(rows []int32) {
		lo, hi := win(rows)
		deque = deque[:0]
		count, from, to := 0, 0, 0
		for i, row := range rows {
			for ; to < int(hi[i]); to++ {
				r := rows[to]
				if !valid.Get(int(r)) {
					continue
				}
				count++
				for len(deque) > 0 {
					c := cmp(int(rows[deque[len(deque)-1]]), int(r))
					if (max && c > 0) || (!max && c < 0) {
						break
					}
					deque = deque[:len(deque)-1]
				}
				deque = append(deque, to)
			}
			for ; from < int(lo[i]); from++ {
				if valid.Get(int(rows[from])) {
					count--
				}
			}
			for len(deque) > 0 && deque[0] < from {
				deque = deque[1:]
			}
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
func rollingIntSum(name string, out dtype.DataType, col *data.Column, seg Segments, win windowsOf, minN int) (*data.Column, error) {
	vals, err := widenToInt128(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]i128.Int128, col.Len())
	ok := make([]bool, col.Len())
	forEachOrdered(seg, false, func(rows []int32) {
		lo, hi := win(rows)
		var sum i128.Int128
		count, from, to := 0, 0, 0
		for i, row := range rows {
			for ; to < int(hi[i]); to++ {
				if r := rows[to]; valid.Get(int(r)) {
					sum, count = sum.Add(vals[r]), count+1
				}
			}
			for ; from < int(lo[i]); from++ {
				if old := rows[from]; valid.Get(int(old)) {
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
	seg Segments, win windowsOf, minN int) (*data.Column, error) {

	vals, err := toFloat64(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]float64, col.Len())
	ok := make([]bool, col.Len())
	forEachOrdered(seg, false, func(rows []int32) {
		lo, hi := win(rows)
		var s rollingState
		count, removed, from, to := 0, 0, 0, 0
		for i, row := range rows {
			for ; to < int(hi[i]); to++ {
				if r := rows[to]; valid.Get(int(r)) {
					s.add(vals[r])
					count++
				}
			}
			for ; from < int(lo[i]); from++ {
				old := rows[from]
				if !valid.Get(int(old)) {
					continue
				}
				s.remove(vals[old])
				count--
				// Afresh from the window once as many values have left it as it
				// holds rows, so rounding cannot build up: an N-row window's every
				// N removals, as before windows by time.
				if removed++; removed >= max(to-from-1, 1) {
					s = rollingState{}
					for _, r := range rows[from+1 : to] {
						if valid.Get(int(r)) {
							s.add(vals[r])
						}
					}
					removed = 0
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
