package kernel

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// temporalRanges is date_ranges, datetime_ranges and time_ranges (step 264, G2 of
// v0.6.6-scope.md): each row's values from start to end every interval, of out's
// element type. The k-th is start plus k intervals, k = 0, 1, …, as Polars counts
// them, not each from the one before: a month from January 31st is February 29th,
// then March 31st. Calendar parts move the wall clock in the column's zone and the
// rest elapsed time, as OffsetBy moves them. closed says whether the ends belong. A
// negative interval gives an empty list, as Polars gives one.
//
// ops are start, end, the interval's months, days and nanoseconds, and closed, the
// last four literals. An end of another temporal type, a Date where out is
// Datetime(us), is cast to it first.
func temporalRanges(fn expr.CallFn, name string, out dtype.DataType, ops []*data.Column,
	n int) (*data.Column, error) {

	dt := out.Inner()
	lit := func(i int) int64 { return data.MustValues[int64](ops[i])[0] }
	iv := dtype.IntervalOf(int32(lit(2)), int32(lit(3)), lit(4))
	closed := expr.Closed(lit(5))
	if err := expr.OffsetRefusal(iv, dt); err != nil {
		return nil, err
	}
	ticks := make([][]int64, 2)
	ends := make([]*data.Column, 2)
	for k, c := range ops[:2] {
		if c.DType().IsNull() {
			return NullColumn(name, out, n)
		}
		if c.DType() != dt {
			var err error
			if c, err = Cast(c.Name(), dt, true, c); err != nil {
				return nil, err
			}
		}
		t, err := readTicks(c)
		if err != nil {
			return nil, err
		}
		ends[k], ticks[k] = c, t
	}
	npt, _ := dt.NanosPerTick()
	loc := locationOf(dt)
	calendar := iv.IsCalendar() && !(dt.ID() == dtype.TypeDate && iv.Months() == 0)
	var step int64 // the interval in ticks, where it is elapsed time or whole days
	switch {
	case dt.ID() == dtype.TypeDate:
		step = int64(iv.Days()) + iv.Nanos()/npt
	case !calendar:
		step = iv.Nanos() / npt
	}
	if !calendar && step <= 0 && !iv.Negative() {
		return nil, uerr.Internalf("kernel: %s steps by %d ticks", fn, step)
	}
	// at is the k-th value from s, and whether the type holds it.
	at := func(s, k int64) (int64, bool) {
		if !calendar {
			if k > math.MaxInt64/step || overflowsI64(expr.OpAdd, s, k*step) {
				return 0, false
			}
			return s + k*step, fitsTicks(dt, s+k*step)
		}
		mo, d, ns := int64(iv.Months())*k, int64(iv.Days())*k, iv.Nanos()*k
		if mo > math.MaxInt32 || d > math.MaxInt32 || k != 0 && ns/k != iv.Nanos() {
			return 0, false
		}
		tm, ok := dt.ToTime(s)
		if !ok {
			return 0, false
		}
		if loc != nil {
			tm = tm.In(loc)
		}
		return fromTime(dt, dtype.IntervalOf(int32(mo), int32(d), ns).AddTo(tm))
	}
	var vals []int64
	offs := make([]int32, 1, n+1)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		rs, re := rowOf(ends[0], i), rowOf(ends[1], i)
		ok := ends[0].IsValid(rs) && ends[1].IsValid(re)
		if ok && !iv.Negative() {
			s, e := ticks[0][rs], ticks[1][re]
			for k := int64(0); ; k++ {
				// A value the type cannot hold is past the end, which it holds.
				v, fits := at(s, k)
				if !fits || v > e || v == e && !closed.UpperClosed() {
					break
				}
				if k == 0 && closed.LowerOpen() {
					continue
				}
				if len(vals) == maxListElems {
					return nil, tooLong(fn.String(), i)
				}
				vals = append(vals, v)
			}
		}
		offs = append(offs, int32(len(vals)))
		valid.Append(ok)
	}
	child := packTicks(name, dt, vals, bitmap.AllSet(len(vals)))
	return data.NewList(name, offs, child, valid.Finish()), nil
}
