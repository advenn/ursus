package kernel

import (
	"math"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// The rolling skew, kurtosis and rank (step 284): Polars' rolling_skew,
// rolling_kurtosis and rolling_rank, by its algorithms, so each answer is Polars' to
// the bit.
//
// # Every product is rounded where it is written
//
// Polars is Rust, which never fuses a*b+c. Go may fuse it into one FMA under
// GOAMD64=v3, across statements too, and then the last bits are not Polars'. An
// explicit float64() conversion rounds and so forbids the fusion: every product that
// feeds an addition or a subtraction here is written float64(a * b).

// pebay is a window's moments, Polars' SkewState and KurtosisState in one: its
// weight, mean, and the sums of the second, third and fourth powers of its
// deviations, by Pébay's updates (crates/polars-compute/src/moment.rs).
type pebay struct{ w, mean, m2, m3, m4 float64 }

// insert adds one value, Polars' insert_one.
func (s *pebay) insert(x float64) {
	w := s.w + 1
	d := x - s.mean
	dw := d / w
	mean := s.mean + dw
	wd := s.w - 1
	u := float64((x - mean) * d)
	m2 := s.m2 + u
	m3 := s.m3 + float64(dw*(float64(u*wd)-float64(3*s.m2)))
	inner := float64(u*(float64(s.w*wd)+1)) + float64(6*s.m2)
	m4 := s.m4 + float64(dw*(float64(dw*inner)-float64(4*s.m3)))
	*s = pebay{w, mean, m2, m3, m4}
	s.clearZero()
}

// combine merges o's values into s, Polars' combine.
func (s *pebay) combine(o pebay) {
	switch {
	case o.w == 0:
		return
	case s.w == 0:
		*s = o
		return
	}
	w := s.w + o.w
	d := o.mean - s.mean
	dw := d / w
	mean := s.mean + float64(o.w*dw)
	wd := s.w - o.w
	sOm2 := float64(s.w * o.m2)
	oSm2 := float64(o.w * s.m2)
	u := float64(float64(o.w*(o.mean-mean)) * d)
	m2 := s.m2 + o.m2 + u
	m3 := s.m3 + o.m3 + float64(dw*(float64(u*wd)+float64(3*(sOm2-oSm2))))
	a := float64(u*(float64(s.w*wd)+float64(o.w*o.w))) + float64(6*(float64(s.w*sOm2)+float64(o.w*oSm2)))
	b := float64(4 * (float64(s.w*o.m3) - float64(o.w*s.m3)))
	m4 := s.m4 + o.m4 + float64(dw*(float64(dw*a)+b))
	*s = pebay{w, mean, m2, m3, m4}
	s.clearZero()
}

// clearZero clears the NaNs a division by a zero weight leaves.
func (s *pebay) clearZero() {
	if s.w == 0 {
		*s = pebay{}
	}
}

// zeroSpread reports whether the second moment is zero to the mean's precision, where
// Polars answers NaN.
func (s *pebay) zeroSpread(m2 float64) bool {
	t := float64(epsilon * s.mean)
	return m2 <= t*t
}

const epsilon = 0x1p-52

// skew is Polars' SkewState finalize: m3/m2^1.5, corrected by sqrt(n(n−1))/(n−2)
// without bias, null below three values then.
func (s *pebay) skew(bias bool) (float64, bool) {
	m2, m3 := s.m2/s.w, s.m3/s.w
	est := math.NaN()
	if !s.zeroSpread(m2) {
		est = m3 / pow15(m2)
	}
	switch {
	case bias:
		return est, s.w != 0
	case s.w <= 2:
		return 0, false
	}
	return math.Sqrt(s.w*(s.w-1)) / (s.w - 2) * est, true
}

// kurtosis is Polars' KurtosisState finalize: m4/m2², corrected without bias, null
// below four values then, less 3 with fisher.
func (s *pebay) kurtosis(fisher, bias bool) (float64, bool) {
	m4, m2 := s.m4/s.w, s.m2/s.w
	est := math.NaN()
	if !s.zeroSpread(m2) {
		est = m4 / (m2 * m2)
	}
	out := est
	switch {
	case bias && s.w == 0:
		return 0, false
	case !bias && s.w <= 3:
		return 0, false
	case !bias:
		n := s.w
		a, b, c := (n-1)/(n-2), (n+1)/(n-3), (n-1)/(n-3)
		out = float64(a*(float64(b*est)-float64(3*c))) + 3
	}
	if fisher {
		out -= 3
	}
	return out, true
}

// pow15 is x^1.5 rounded once, x·sqrt(x) in double-double, for glibc's pow, which
// Polars' powf calls. Go's Pow(x, 1.5) is Exp(Log(x)/2)·x, which agreed with glibc's
// for 12% of 50,000 values probed, and this for 99.9%. Where they differ glibc's is
// off: its pow errs by up to 0.52 ulp, so near a midpoint it can round the wrong
// way, 0.503 ulp from the exact value where this is 0.497.
func pow15(x float64) float64 {
	if !(x > 0) || math.IsInf(x, 1) {
		return math.Pow(x, 1.5)
	}
	s := math.Sqrt(x)
	p := float64(x * s)
	if math.IsInf(p, 0) || p == 0 {
		return p
	}
	d := math.FMA(-s, s, x) / (2 * s) // sqrt(x) − s, to first order
	pe := math.FMA(x, s, -p)          // x·s − p, exactly
	return p + float64(pe+float64(x*d))
}

// momentQueue is Polars' MomentWindow: a window's moments as a queue of two stacks,
// so a value leaves without being subtracted. A value enters the back, merged into
// one state. When a value must leave and the front is empty, the back is walked
// newest first into the front, each entry the state of it and every value after it,
// and the front's top leaves. The window's state is the back's merged with the
// front's top. A NaN or an infinity enters as 0 and is counted, and a window holding
// one answers NaN.
type momentQueue struct {
	front     []pebay
	back      []float64
	agg       pebay
	nonFinite int
}

func (q *momentQueue) reset() {
	q.front, q.back, q.agg, q.nonFinite = q.front[:0], q.back[:0], pebay{}, 0
}

func (q *momentQueue) push(x float64) {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		x = 0
		q.nonFinite++
	}
	q.back = append(q.back, x)
	q.agg.insert(x)
}

func (q *momentQueue) pop(x float64) {
	if len(q.front) == 0 {
		var agg pebay
		for len(q.back) > 0 {
			agg.insert(q.back[len(q.back)-1])
			q.back = q.back[:len(q.back)-1]
			q.front = append(q.front, agg)
		}
		q.agg = pebay{}
	}
	q.front = q.front[:len(q.front)-1]
	if math.IsNaN(x) || math.IsInf(x, 0) {
		q.nonFinite--
	}
}

func (q *momentQueue) len() int { return len(q.front) + len(q.back) }

func (q *momentQueue) state() pebay {
	s := q.agg
	if len(q.front) > 0 {
		s.combine(q.front[len(q.front)-1])
	}
	return s
}

// rollingMoment answers each row with its window's skew or kurtosis. A window is
// walked as Polars walks it: the values that left popped, then those that entered
// pushed, and a window starting at or past the last one's end starts afresh.
func rollingMoment(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments, win windowsOf, minN int) (*data.Column, error) {

	vals, err := toFloat64(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]float64, col.Len())
	ok := make([]bool, col.Len())
	forEachOrdered(seg, false, func(rows []int32) {
		lo, hi := win(rows)
		var q momentQueue
		from, to := 0, 0
		for i, row := range rows {
			if int(lo[i]) >= to {
				q.reset()
				from, to = int(lo[i]), int(lo[i])
			}
			for ; from < int(lo[i]); from++ {
				if r := rows[from]; valid.Get(int(r)) {
					q.pop(vals[r])
				}
			}
			for ; to < int(hi[i]); to++ {
				if r := rows[to]; valid.Get(int(r)) {
					q.push(vals[r])
				}
			}
			if q.len() < minN {
				continue
			}
			s := q.state()
			var v float64
			var has bool
			if fn == expr.WinRollingKurtosis {
				v, has = s.kurtosis(params.Fisher, params.Bias)
			} else {
				v, has = s.skew(params.Bias)
			}
			if has && q.nonFinite > 0 {
				v = math.NaN()
			}
			res[row], ok[row] = v, has
		}
	})
	return floatsOut(name, out, res, ok), nil
}

// floatsOut is a float answer a row, as a Float64 or a Float32, null where !ok.
func floatsOut(name string, out dtype.DataType, res []float64, ok []bool) *data.Column {
	valid := seenBitmap(ok, len(ok))
	if out == dtype.Float32 {
		f32 := make([]float32, len(res))
		for i, v := range res {
			f32[i] = float32(v)
		}
		return data.NewFixed(name, out, f32, valid)
	}
	return data.NewFixed(name, out, res, valid)
}

// rollingRank answers each row with its own value's rank among its window's values,
// as Polars' rolling_rank does by an order statistic tree: min, max, their average,
// or dense. Values compare in the order Sort uses, NaN above every number and -0
// with 0. A row whose value is null is null.
//
// The window's rows are kept sorted by value, a value's equals in the order they
// came. They leave in the order they came, so a row leaving is the first of its
// equals, found by binary search. For dense, the distinct values are kept apart, each
// with its count.
func rollingRank(params expr.WinParams, name string, out dtype.DataType, col *data.Column,
	seg Segments, win windowsOf, minN int) (*data.Column, error) {

	cmp, err := valueComparator(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	n := col.Len()
	res := make([]float64, n)
	ok := make([]bool, n)
	// first is the position of the first sorted row not below row r's value, and past
	// that of the first above it.
	first := func(sorted []int32, r int32, past bool) int {
		i, _ := slices.BinarySearchFunc(sorted, r, func(a, b int32) int {
			c := cmp(int(a), int(b))
			if past && c == 0 {
				return -1
			}
			return c
		})
		return i
	}
	type distinct struct{ row, n int32 }
	forEachOrdered(seg, false, func(rows []int32) {
		lo, hi := win(rows)
		var sorted []int32
		var uniq []distinct
		uniqAt := func(r int32) (int, bool) {
			return slices.BinarySearchFunc(uniq, r, func(a distinct, b int32) int { return cmp(int(a.row), int(b)) })
		}
		from, to := 0, 0
		for i, row := range rows {
			for ; to < int(hi[i]); to++ {
				r := rows[to]
				if !valid.Get(int(r)) {
					continue
				}
				sorted = slices.Insert(sorted, first(sorted, r, true), r)
				if params.Method == expr.RankDense {
					if j, found := uniqAt(r); found {
						uniq[j].n++
					} else {
						uniq = slices.Insert(uniq, j, distinct{r, 1})
					}
				}
			}
			for ; from < int(lo[i]); from++ {
				r := rows[from]
				if !valid.Get(int(r)) {
					continue
				}
				at := first(sorted, r, false)
				sorted = slices.Delete(sorted, at, at+1)
				if params.Method == expr.RankDense {
					if j, found := uniqAt(r); found {
						if uniq[j].n--; uniq[j].n == 0 {
							uniq = slices.Delete(uniq, j, j+1)
						}
					}
				}
			}
			if len(sorted) < minN || !valid.Get(int(row)) {
				continue
			}
			below, upto := first(sorted, row, false), first(sorted, row, true)
			switch params.Method {
			case expr.RankMin:
				res[row] = float64(below + 1)
			case expr.RankMax:
				res[row] = float64(upto)
			case expr.RankAverage:
				res[row] = (float64(below+1) + float64(upto)) / 2
			case expr.RankDense:
				j, _ := uniqAt(row)
				res[row] = float64(j + 1)
			}
			ok[row] = true
		}
	})
	if out == dtype.Float64 {
		return floatsOut(name, out, res, ok), nil
	}
	u := make([]uint32, n)
	for i, v := range res {
		u[i] = uint32(v)
	}
	return data.NewFixed(name, out, u, seenBitmap(ok, n)), nil
}

// centeredWindows is a window of size rows labelled at its middle row, as
// rollingCentered's: size/2 rows before the row and (size-1)/2 after it, cut at the
// partition's ends.
func centeredWindows(size int) windowsOf {
	return func(rows []int32) (lo, hi []int32) {
		lo, hi = make([]int32, len(rows)), make([]int32, len(rows))
		for i := range rows {
			lo[i] = int32(max(0, i-size/2))
			hi[i] = int32(min(len(rows), i+(size-1)/2+1))
		}
		return lo, hi
	}
}

// rollingCovCorr answers each row with its window's covariance or correlation of a
// pair, as Polars builds them from rolling means and variances, pandas' way: over the
// window's rows where both hold a value, n of them,
//
//	cov  = (mean(x·y) − mean(x)·mean(y)) · n/(n − ddof)
//	corr = the cov at ddof 1 / sqrt(var(x)·var(y))
//
// null where n is below MinSamples, and a correlation where a variance is, at n of
// 1. The means and variances are rollingFloat's state, so a NaN or an infinity
// answers as in a rolling mean. A Float32 answer is the float64 one, rounded once.
func rollingCovCorr(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	pair *data.Column, seg Segments, win windowsOf, minN int) (*data.Column, error) {

	x, y, err := pairFields(pair)
	if err != nil {
		return nil, err
	}
	var xs, ys []float64
	for _, c := range []struct {
		col *data.Column
		to  *[]float64
	}{{x, &xs}, {y, &ys}} {
		c.col, err = readable(c.col)
		if err != nil {
			return nil, err
		}
		if *c.to, err = toFloat64(c.col); err != nil {
			return nil, err
		}
	}
	pv, xv, yv := pair.Validity(), x.Validity(), y.Validity()
	both := func(r int32) bool { return pv.Get(int(r)) && xv.Get(int(r)) && yv.Get(int(r)) }
	corr := fn == expr.WinRollingCorr
	ddof := float64(params.Ddof)
	if corr {
		ddof = 1
	}
	res := make([]float64, pair.Len())
	ok := make([]bool, pair.Len())
	forEachOrdered(seg, false, func(rows []int32) {
		lo, hi := win(rows)
		var sx, sy, sxy rollingState
		add := func(r int32) {
			sx.add(xs[r])
			sy.add(ys[r])
			sxy.add(float64(xs[r] * ys[r]))
		}
		count, removed, from, to := 0, 0, 0, 0
		for i, row := range rows {
			for ; to < int(hi[i]); to++ {
				if r := rows[to]; both(r) {
					add(r)
					count++
				}
			}
			for ; from < int(lo[i]); from++ {
				old := rows[from]
				if !both(old) {
					continue
				}
				sx.remove(xs[old])
				sy.remove(ys[old])
				sxy.remove(float64(xs[old] * ys[old]))
				count--
				// Afresh, as rollingFloat's state is, so rounding cannot build up.
				if removed++; removed >= max(to-from-1, 1) {
					sx, sy, sxy = rollingState{}, rollingState{}, rollingState{}
					for _, r := range rows[from+1 : to] {
						if both(r) {
							add(r)
						}
					}
					removed = 0
				}
			}
			if count < minN {
				continue
			}
			mxy, _ := rollingAnswer(expr.WinRollingMean, 0, &sxy)
			mx, _ := rollingAnswer(expr.WinRollingMean, 0, &sx)
			my, _ := rollingAnswer(expr.WinRollingMean, 0, &sy)
			n := float64(count)
			v := (mxy - float64(mx*my)) * (n / (n - ddof))
			if corr {
				vx, okx := rollingAnswer(expr.WinRollingVar, 1, &sx)
				vy, oky := rollingAnswer(expr.WinRollingVar, 1, &sy)
				if !okx || !oky {
					continue
				}
				v /= math.Sqrt(vx * vy)
			}
			res[row], ok[row] = v, true
		}
	})
	return floatsOut(name, out, res, ok), nil
}
