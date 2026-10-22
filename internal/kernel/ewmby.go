package kernel

import (
	"cmp"
	"math"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// ewm_mean_by and ewm_sum_by (step 285): Polars' ewm_by_core
// (crates/polars-ops/src/series/ops/ewm_by.rs at py-1.44.1). In by's order, each row
// holding a value and a time decays the last answer by 0.5^(Δt/half-life), Δt from
// the last such row:
//
//	mean: d·last + (1−d)·x, or x where x equals the last
//	sum:  d·last + x
//
// and a row missing either is null and skipped. The first such row is its value.
// Times are in Polars' units: a Datetime's own, a Date's microseconds, and the
// half-life truncated to them. A Float32 is computed in float32, as Polars computes
// it. Every product feeding a sum is rounded as written (rollingmore.go).

// ewmByRun is one ewm by time over one pair column.
type ewmByRun struct {
	mean       bool
	f32        bool
	half       int64
	vals       []float64
	times      []int64
	pv, vv, tv bitmap.View
	res        []float64
	ok         []bool
}

func newEwmByRun(fn expr.WinFnOp, params expr.WinParams, out dtype.DataType, pair *data.Column) (*ewmByRun, error) {
	value, by, err := pairFields(pair)
	if err != nil {
		return nil, err
	}
	if value, err = readable(value); err != nil {
		return nil, err
	}
	if by, err = readable(by); err != nil {
		return nil, err
	}
	vals, err := toFloat64(value)
	if err != nil {
		return nil, err
	}
	times, err := byTicks(by)
	if err != nil {
		return nil, err
	}
	hl := params.Period
	nanos := int64(hl.Days())*86_400_000_000_000 + hl.Nanos()
	var half int64
	switch by.DType().ID() {
	case dtype.TypeDate:
		// A Date is a Datetime in microseconds there.
		us := make([]int64, len(times))
		for i, d := range times {
			us[i] = d * 86_400_000_000
		}
		times, half = us, nanos/1000
	case dtype.TypeDatetime:
		npt, _ := by.DType().NanosPerTick()
		half = nanos / npt
	case dtype.TypeInt32, dtype.TypeInt64, dtype.TypeUint32, dtype.TypeUint64:
		// An integer by counts its own ticks, and its half-life is "Ni" (step 312).
		half = nanos
	default:
		return nil, uerr.Internalf("kernel: %s_by over %s", fn, by.DType())
	}
	return &ewmByRun{mean: fn == expr.WinEwmMean, f32: out == dtype.Float32, half: half,
		vals: vals, times: times, pv: pair.Validity(), vv: value.Validity(), tv: by.Validity(),
		res: make([]float64, pair.Len()), ok: make([]bool, pair.Len())}, nil
}

func (e *ewmByRun) has(row int) bool { return e.pv.Get(row) && e.vv.Get(row) && e.tv.Get(row) }

// step answers row, the next in by's order, continuing st.
func (e *ewmByRun) step(st *EwmState, row int) {
	if !e.has(row) {
		return
	}
	x, t := e.vals[row], e.times[row]
	var r float64
	switch {
	case !st.seen:
		st.seen, r = true, x
	case e.mean && x == st.mean:
		r = x
	case e.f32:
		d := float32(halfPow(float64(float32(t-st.prevTime) / float32(e.half))))
		w := float32(1)
		if e.mean {
			w = 1 - d
		}
		r = float64(float32(d*float32(st.mean)) + float32(w*float32(x)))
	default:
		d := halfPow(float64(t-st.prevTime) / float64(e.half))
		w := 1.0
		if e.mean {
			w = 1 - d
		}
		r = float64(d*st.mean) + float64(w*x)
	}
	st.prevTime, st.mean = t, r
	e.res[row], e.ok[row] = r, true
}

// winEwmBy runs each partition in by's order: its rows holding a value and a time,
// sorted stably by the time, so rows of one time are taken in the window's order.
func winEwmBy(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	pair *data.Column, seg Segments) (*data.Column, error) {

	e, err := newEwmByRun(fn, params, out, pair)
	if err != nil {
		return nil, err
	}
	var sorted []int32
	forEachOrdered(seg, false, func(rows []int32) {
		sorted = sorted[:0]
		for _, r := range rows {
			if e.has(int(r)) {
				sorted = append(sorted, r)
			}
		}
		slices.SortStableFunc(sorted, func(a, b int32) int { return cmp.Compare(e.times[a], e.times[b]) })
		var st EwmState
		for _, r := range sorted {
			e.step(&st, int(r))
		}
	})
	return floatsAs(name, out, e.res, seenBitmap(e.ok, len(e.ok))), nil
}

// halfPow is 0.5^r for r >= 0, rounded once: glibc's pow(0.5, r), which Polars'
// powf calls, is within 0.52 ulp, and Go's Pow and Exp2 are not, agreeing with it
// for 86% and 89% of 50,000 values probed. It is 2^-floor(r) times exp(−f·ln 2) for
// r's fraction f, the exponential in double-double: the argument scaled by 2^-10, a
// series of nine terms, and squared back ten times.
func halfPow(r float64) float64 {
	switch {
	case r == 0:
		return 1
	case math.IsNaN(r):
		return r
	case r > 1080:
		return 0
	}
	n := math.Floor(r)
	f := r - n // exact
	th, tl := twoProd(f, ln2Hi)
	th, tl = fastTwoSum(th, tl+float64(f*ln2Lo))
	th, tl = -th/1024, -tl/1024 // exact: a power of two
	sh, sl := 1.0, 0.0
	for k := 9.0; k >= 1; k-- {
		sh, sl = ddMul(sh, sl, th, tl)
		sh, sl = ddDiv(sh, sl, k)
		sh, sl = ddAdd(sh, sl, 1, 0)
	}
	for range 10 {
		sh, sl = ddMul(sh, sl, sh, sl)
	}
	return math.Ldexp(sh+sl, -int(n))
}

// ln 2 in double-double.
const (
	ln2Hi = 0x1.62e42fefa39efp-1
	ln2Lo = 0x1.abc9e3b39803fp-56
)

// The double-double operations: a value as an unevaluated sum of two float64s.
// Products are rounded where written, so v1 and v3 agree.

func twoProd(a, b float64) (p, e float64) {
	p = float64(a * b)
	return p, math.FMA(a, b, -p)
}

func twoSum(a, b float64) (s, e float64) {
	s = a + b
	bb := s - a
	return s, (a - (s - bb)) + (b - bb)
}

func fastTwoSum(a, b float64) (s, e float64) {
	s = a + b
	return s, b - (s - a)
}

func ddMul(ah, al, bh, bl float64) (float64, float64) {
	p, e := twoProd(ah, bh)
	return fastTwoSum(p, e+float64(ah*bl)+float64(al*bh))
}

func ddAdd(ah, al, bh, bl float64) (float64, float64) {
	s, e := twoSum(ah, bh)
	return fastTwoSum(s, e+al+bl)
}

func ddDiv(ah, al, k float64) (float64, float64) {
	q := ah / k
	p, pe := twoProd(q, k)
	return fastTwoSum(q, ((ah-p)-pe+al)/k)
}
