package kernel

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// rollingBy is a rolling function by time (step 177), Polars' rolling_*_by: row i's
// window is the rows of its partition whose by value t lies within the period of its
// own, (tᵢ − period, tᵢ] by default, wherever they are in the partition.
//
// Each partition's rows with a by value are sorted by it, stably, and the windows are
// then ranges of that order, whose two ends only rise: the same kernels as a window
// of N rows walk them. A row whose by is null is in no window and answers null, as
// Polars'. Rows that tie on by share one window, the later ones included.
//
// A calendar period counts months and days on the wall clock of by's zone, as the
// frame-level Rolling does, so "1d" across a change of offset is a calendar day,
// where FromDuration(24h) is twenty-four hours.
func rollingBy(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	pair *data.Column, seg Segments) (*data.Column, error) {

	value, by, err := pairFields(pair)
	if err != nil {
		return nil, err
	}
	ticks, err := readTicks(by)
	if err != nil {
		return nil, err
	}
	pv, bv := pair.Validity(), by.Validity()
	sorted := Segments{Perm: make([]int32, 0, len(seg.Perm)), Bounds: make([]int32, 1, len(seg.Bounds))}
	for p := 0; p+1 < len(seg.Bounds); p++ {
		start := len(sorted.Perm)
		for _, r := range seg.Perm[seg.Bounds[p]:seg.Bounds[p+1]] {
			if pv.Get(int(r)) && bv.Get(int(r)) {
				sorted.Perm = append(sorted.Perm, r)
			}
		}
		slices.SortStableFunc(sorted.Perm[start:], func(a, b int32) int { return cmp.Compare(ticks[a], ticks[b]) })
		sorted.Bounds = append(sorted.Bounds, int32(len(sorted.Perm)))
	}

	starts, err := windowStarts(by.DType(), params.Period, ticks)
	if err != nil {
		return nil, err
	}
	closed := params.Closed.Or(expr.ClosedRight)
	win := func(rows []int32) (lo, hi []int32) {
		lo, hi = make([]int32, len(rows)), make([]int32, len(rows))
		l, h := 0, 0
		for i, r := range rows {
			start, end := starts[r], ticks[r]
			// The first row in the window, and the first past it. Both only rise, as
			// the rows' own values do.
			for l < len(rows) && (ticks[rows[l]] < start || closed.LowerOpen() && ticks[rows[l]] == start) {
				l++
			}
			h = max(h, l)
			for h < len(rows) && (ticks[rows[h]] < end || closed.UpperClosed() && ticks[rows[h]] == end) {
				h++
			}
			lo[i], hi[i] = int32(l), int32(max(h, l))
		}
		return lo, hi
	}
	minN := int(params.MinSamples)
	if minN < 1 {
		return nil, uerr.Internalf("kernel: %s by time needing %d values", fn, minN)
	}
	return rollingOver(fn, params, name, out, value, sorted, win, minN)
}

// windowStarts gives each row's window start, its by value less the period, as a
// tick of by's type. A start before the first tick by's type holds is that tick, as
// every value is after it.
//
// A period of whole ticks is subtracted as ticks, saturating. A calendar one is
// moved on the wall clock of by's zone, through dtype.Interval.AddTo, row by row.
func windowStarts(dt dtype.DataType, period dtype.Interval, ticks []int64) ([]int64, error) {
	npt, ok := dt.NanosPerTick()
	if !ok {
		return nil, uerr.Internalf("kernel: a window by %s", dt)
	}
	starts := make([]int64, len(ticks))
	if !period.IsCalendar() {
		back := period.Nanos() / npt
		for i, t := range ticks {
			if t < math.MinInt64+back {
				starts[i] = math.MinInt64
				continue
			}
			starts[i] = t - back
		}
		return starts, nil
	}
	var loc *time.Location
	if tz := dt.TimeZone(); tz != "" && tz != "UTC" {
		var err error
		if loc, err = time.LoadLocation(tz); err != nil {
			return nil, uerr.Internalf("kernel: the time zone %q passed planning but does not load: %v", tz, err)
		}
	}
	back := period.Neg()
	for i, t := range ticks {
		tm, ok := dt.ToTime(t)
		if !ok {
			return nil, uerr.Internalf("kernel: %s cannot read %d as an instant", dt, t)
		}
		if loc != nil {
			tm = tm.In(loc)
		}
		starts[i] = dt.FromTimeBound(back.AddTo(tm), false)
	}
	return starts, nil
}
