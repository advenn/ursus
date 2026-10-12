package kernel

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// dtCombine is dt.combine (step 230): each row's day, from a Date or a Datetime,
// with the Time of day beside it, as Polars' dt.combine.
//
// A Datetime's day is the one on its own wall clock: a zoned column is read as its
// wall clocks, the day and the time joined there, and the result put back in the
// zone, refusing a wall clock the zone skips or holds twice, as Polars does. The
// time of day is truncated to the answer's unit, as a cast would truncate it.
func dtCombine(name string, out dtype.DataType, recv, tod *data.Column, n int) (*data.Column, error) {
	tpd, ok := dtype.TicksPerDay(out)
	if !ok || out.ID() != dtype.TypeDatetime {
		return nil, uerr.Internalf("kernel: dt.combine into %s", out)
	}
	naive := dtype.Datetime(out.TimeUnit(), "")
	zone := out.TimeZone()
	isDate := recv.DType().ID() == dtype.TypeDate
	wall := recv
	if !isDate && zone != "" {
		var err error
		if wall, err = replaceTimeZone(name, naive, recv, []any{"", expr.ZoneRaise, expr.ZoneRaise}); err != nil {
			return nil, err
		}
	}
	day, err := readTicks(wall)
	if err != nil {
		return nil, err
	}
	var at []int64
	var todNpt int64 = 1
	if tod.DType().ID() == dtype.TypeTime {
		if at, err = readTicks(tod); err != nil {
			return nil, err
		}
		todNpt, _ = tod.DType().NanosPerTick()
	}
	outNpt, _ := out.NanosPerTick()
	res := make([]int64, n)
	valid := make([]bool, n)
	for i := range n {
		r, t := rowOf(recv, i), rowOf(tod, i)
		if !wall.IsValid(r) || at == nil || !tod.IsValid(t) {
			continue
		}
		var start int64
		if isDate {
			d := day[r]
			if d > math.MaxInt64/tpd || d < math.MinInt64/tpd {
				return nil, uerr.New(uerr.KindValue, "dt",
					"dt.combine: the day %s is outside the range of %s", dtype.FormatTemporal(recv.DType(), d), out).
					Hint("a Date this far from 1970 has no %s", out)
			}
			start = d * tpd
		} else {
			start = day[r] - ((day[r]%tpd)+tpd)%tpd
		}
		// A time of day is under a day of nanoseconds, so the product fits; the sum
		// need not, at the last day a Datetime(ns) holds.
		add := at[t] * todNpt / outNpt
		if start > math.MaxInt64-add {
			return nil, uerr.New(uerr.KindValue, "dt",
				"dt.combine: row %d is outside the range of %s", i, out).
				Hint("cast to a coarser unit first")
		}
		res[i], valid[i] = start+add, true
	}
	col := data.NewFixed(name, naive, res, seenBitmap(valid, n))
	if zone == "" {
		return col, nil
	}
	return replaceTimeZone(name, out, col, []any{zone, expr.ZoneRaise, expr.ZoneRaise})
}
