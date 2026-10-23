package kernel

import (
	"math"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// The temporal namespace, continued (step 272, D of v0.6.6-scope.md): iso_year,
// century and millennium; a zone's base and daylight-saving offsets; with_time_unit;
// the totals in the smaller units and with their fraction; second with its fraction;
// and epoch in a unit.

// floorDiv is a/b rounded toward negative infinity, b positive.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b < 0 {
		q--
	}
	return q
}

// dtMore is the step-272 functions of one operand.
func dtMore(fn expr.CallFn, name string, out dtype.DataType, c *data.Column, args []any) (*data.Column, error) {
	dt := c.DType()
	if fn == expr.FnDtWithTimeUnit {
		// The same integers, read in another unit, as Polars reinterprets them.
		return c.WithDType(out).Rename(name), nil
	}
	if fn == expr.FnDtCastTimeUnit {
		// The same instants or lengths in another unit, as a cast gives them: an
		// instant floored, a length truncated toward zero, as Polars' are (step 325).
		return Cast(name, out, true, c)
	}
	ticks, err := readTicks(c)
	if err != nil {
		return nil, err
	}
	n := c.Len()
	valid := c.Validity()
	npt, ok := dt.NanosPerTick()
	if !ok {
		return nil, uerr.Internalf("kernel: %s on %s", fn, dt)
	}
	switch fn {
	case expr.FnDtIsoYear, expr.FnDtCentury, expr.FnDtMillennium, expr.FnDtSecondFractional:
		loc := locationOf(dt)
		years := make([]int32, n)
		var secs []float64
		if fn == expr.FnDtSecondFractional {
			secs = make([]float64, n)
		}
		for i, t := range ticks {
			if !valid.Get(i) {
				continue
			}
			tm, ok := dt.ToTime(t)
			if !ok {
				return nil, uerr.Internalf("kernel: %s cannot read %s as an instant", fn, dt)
			}
			if loc != nil {
				tm = tm.In(loc)
			}
			switch fn {
			case expr.FnDtIsoYear:
				y, _ := tm.ISOWeek()
				years[i] = int32(y)
			case expr.FnDtCentury:
				// Year 1 to 100 is the first century and year 0 none, as Polars counts.
				years[i] = int32(floorDiv(int64(tm.Year())+99, 100))
			case expr.FnDtMillennium:
				years[i] = int32(floorDiv(int64(tm.Year())+999, 1000))
			default:
				// The fraction times a nanosecond's reciprocal, as Polars divides by
				// a constant; the conversion keeps the product from fusing with the add.
				secs[i] = float64(float64(tm.Nanosecond())*(1/1e9)) + float64(tm.Second())
			}
		}
		if secs != nil {
			return data.NewFixed(name, dtype.Float64, secs, valid), nil
		}
		return data.NewFixed(name, dtype.Int32, years, valid), nil

	case expr.FnDtBaseUtcOffset, expr.FnDtDstOffset:
		return zoneOffsets(fn, name, dt, ticks, valid, npt)

	case expr.FnDtTotalMilliseconds, expr.FnDtTotalMicroseconds, expr.FnDtTotalNanoseconds:
		per := map[expr.CallFn]int64{expr.FnDtTotalMilliseconds: 1e6, expr.FnDtTotalMicroseconds: 1e3,
			expr.FnDtTotalNanoseconds: 1}[fn]
		vals := make([]int64, n)
		for i, t := range ticks {
			if !valid.Get(i) {
				continue
			}
			if npt < per {
				vals[i] = t / (per / npt) // toward zero, as the coarser totals are
				continue
			}
			m := npt / per
			if overflowsI64(expr.OpMul, t, m) {
				return nil, uerr.New(uerr.KindValue, "dt", "%s of %s is past an Int64",
					fn, dtype.FormatTemporal(dt, t))
			}
			vals[i] = t * m
		}
		return data.NewFixed(name, dtype.Int64, vals, valid), nil

	case expr.FnDtTotalFractional:
		per, _ := argInt(args, 0)
		if per <= 0 {
			return nil, uerr.Internalf("kernel: %s in %d nanoseconds", fn, per)
		}
		// Ticks times the unit's reciprocal in ticks, as Polars divides by a constant.
		var scale float64
		if per >= npt {
			scale = 1 / float64(per/npt)
		} else {
			scale = float64(npt / per)
		}
		vals := make([]float64, n)
		for i, t := range ticks {
			if valid.Get(i) {
				vals[i] = float64(t) * scale
			}
		}
		return data.NewFixed(name, dtype.Float64, vals, valid), nil

	case expr.FnDtEpochIn:
		per, _ := argInt(args, 0)
		if per <= 0 {
			return nil, uerr.Internalf("kernel: %s in %d nanoseconds", fn, per)
		}
		vals := make([]int64, n)
		for i, t := range ticks {
			if !valid.Get(i) {
				continue
			}
			if npt < per {
				// Floored: the unit the instant lies in, so a microsecond before 1970
				// is millisecond -1, as Polars counts it.
				vals[i] = floorDiv(t, per/npt)
			} else {
				m := npt / per
				if overflowsI64(expr.OpMul, t, m) {
					return nil, uerr.New(uerr.KindValue, "dt",
						"%s at row %d is too far from the epoch to count in the unit", dtype.FormatTemporal(dt, t), i)
				}
				vals[i] = t * m
			}
			if per == expr.EpochDay && (vals[i] < math.MinInt32 || vals[i] > math.MaxInt32) {
				return nil, uerr.New(uerr.KindValue, "dt",
					"%s at row %d is too far from the epoch to count its days as an Int32",
					dtype.FormatTemporal(dt, t), i)
			}
		}
		if per == expr.EpochDay {
			days := make([]int32, n)
			for i, v := range vals {
				days[i] = int32(v)
			}
			return data.NewFixed(name, dtype.Int32, days, valid), nil
		}
		return data.NewFixed(name, dtype.Int64, vals, valid), nil
	}
	return nil, uerr.Internalf("kernel: %s is not a temporal function", fn)
}

// zoneOffsets is each instant's zone's base offset from UTC, or its daylight-saving
// offset, the two summing to its offset, in milliseconds: Polars' base_utc_offset and
// dst_offset. Go's zones say only whether a period is daylight-saving time, so a
// period that is takes its base from the nearest earlier one that is not, and the
// difference is its daylight-saving offset. That gives Polars' answers where a zone's
// daylight-saving time is negative, as Dublin's winter and Casablanca's Ramadan are.
// The offsets are found once per period of the zone.
func zoneOffsets(fn expr.CallFn, name string, dt dtype.DataType, ticks []int64, valid bitmap.View,
	npt int64) (*data.Column, error) {

	loc := locationOf(dt)
	perSec := int64(time.Second) / npt // a Datetime's tick is a second or a fraction of one
	vals := make([]int64, len(ticks))
	var lo, hi, base, dst int64
	kept := false
	for i, t := range ticks {
		if !valid.Get(i) || loc == nil {
			continue
		}
		sec := floorDiv(t, perSec)
		if !kept || sec < lo || sec >= hi {
			tm := time.Unix(min(max(sec, -zoneSpan), zoneSpan), 0).In(loc)
			base, dst = baseAndDST(tm)
			start, end := tm.ZoneBounds()
			lo, hi, kept = math.MinInt64, math.MaxInt64, true
			if !start.IsZero() {
				lo = start.Unix()
			}
			if !end.IsZero() {
				hi = end.Unix()
			}
		}
		if fn == expr.FnDtBaseUtcOffset {
			vals[i] = base * 1000
		} else {
			vals[i] = dst * 1000
		}
	}
	return data.NewFixed(name, dtype.Duration(dtype.Milli), vals, valid), nil
}

// baseAndDST is the base offset and the daylight-saving offset at tm, in seconds: its
// own offset and none, or where its period is daylight-saving time the offset of the
// nearest period before it that is not, else after it, and the difference.
func baseAndDST(tm time.Time) (int64, int64) {
	_, total := tm.Zone()
	if !tm.IsDST() {
		return int64(total), 0
	}
	for _, back := range []bool{true, false} {
		p := tm
		for range 8 {
			start, end := p.ZoneBounds()
			edge := start.Add(-time.Second)
			if !back {
				edge = end
			}
			if (back && start.IsZero()) || (!back && end.IsZero()) {
				break
			}
			p = edge.In(tm.Location())
			if !p.IsDST() {
				_, b := p.Zone()
				return int64(b), int64(total - b)
			}
		}
	}
	return int64(total), 0
}
