package kernel

import (
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Temporal kernels.
//
// # Go's time package is the source of truth
//
// Calendar extraction is not arithmetic. Month lengths, leap years and ISO week
// numbering are rules, not formulas, and re-deriving them is how off-by-one-day
// bugs enter a codebase for a decade. Every component below goes through
// time.Time, and the differential test checks each against the same method it
// delegates to — which sounds circular but is not: what it verifies is the tick
// count -> time.Time conversion, the unit handling and the null propagation, which
// is where the bugs actually live.
//
// The conversion itself is dtype.ToTime, shared with the frame renderer, the CSV
// reader and the CSV writer, so all four agree by construction.
//
// Like the string kernels, these are scalar-only: temporal columns are physically
// Int32/Int64 and simd_amd64.go records that only float64 has a portable SIMD path
// today. The families are the same shape as the float64 one and can be vectorised
// when archsimd lands.

// DtCall applies a temporal function to a column.
func DtCall(fn expr.CallFn, name string, out dtype.DataType,
	c *data.Column, args []any) (*data.Column, error) {

	dt := c.DType()
	ticks, err := readTicks(c)
	if err != nil {
		return nil, err
	}
	n := c.Len()
	valid := c.Validity()

	// Duration totals divide a tick count; no calendar involved.
	if per, ok := durationDivisor(fn); ok {
		npt, has := dt.NanosPerTick()
		if !has {
			return nil, uerr.Internalf("kernel: %s on %s", fn, dt)
		}
		vals := make([]int64, n)
		for i, t := range ticks {
			if !valid.Get(i) {
				continue
			}
			// Multiply before dividing so a coarse unit does not floor to zero:
			// Duration(s) total_minutes must not compute (t/60e9)*... first.
			vals[i] = t * npt / per
		}
		return data.NewFixed(name, out, vals, valid), nil
	}

	if fn == expr.FnDtEpoch {
		npt, _ := dt.NanosPerTick()
		vals := make([]int64, n)
		for i, t := range ticks {
			if valid.Get(i) {
				vals[i] = t * npt / int64(time.Second)
			}
		}
		return data.NewFixed(name, out, vals, valid), nil
	}

	if fn == expr.FnDtTruncate {
		return truncateTemporal(name, out, dt, ticks, valid, args)
	}

	// Calendar components.
	vals := make([]int32, n)
	loc := locationOf(dt)
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
		vals[i] = component(fn, tm)
	}
	return data.NewFixed(name, out, vals, valid), nil
}

// truncateCalendar floors to a month or day grid, IN THE COLUMN'S OWN TIMEZONE.
//
// # Why this cannot be tick arithmetic
//
// The nanosecond path above divides a tick count, which is exact for any grid whose
// spacing is a fixed number of nanoseconds. A month is not — it is 28 to 31 days — and
// neither is a day in a zone that changes its offset twice a year. So this one goes
// through time.Time, which owns the calendar, exactly as the component path two
// functions above does and for the same reason.
//
// # The zone is read once per column
//
// A day boundary is a LOCAL midnight. Flooring the raw tick count would give 00:00
// UTC, which in New York is 19:00 or 20:00 the previous evening — so every row of a
// `group_by 1 day` would land in the wrong bucket, by a whole day, for the hours
// between local midnight and UTC midnight. That is the difference between
// Every("1d") and a 24-hour Duration, and it is why they are different types.
func truncateCalendar(name string, out, dt dtype.DataType, ticks []int64,
	valid bitmap.View, iv dtype.Interval,
) (*data.Column, error) {

	if dt.ID() == dtype.TypeTime {
		// A Time is a wall clock with no date, so it has no month and no day to floor
		// to. Refusing beats answering about an instant the column does not describe.
		return nil, uerr.New(uerr.KindType, "dt",
			"truncate by %s is not defined for %s", iv, dt).
			Hint("a Time has no date, so it cannot be floored to a day or a month").
			Hint("use a sub-day interval, or cast to Datetime first")
	}

	loc := locationOf(dt)
	n := len(ticks)
	res := make([]int64, n)
	for i, t := range ticks {
		if !valid.Get(i) {
			continue
		}
		tm, ok := dt.ToTime(t)
		if !ok {
			return nil, uerr.Internalf("kernel: truncate cannot read %s as an instant", dt)
		}
		if loc != nil {
			tm = tm.In(loc)
		}
		v, ok := dt.FromTime(iv.TruncateTo(tm))
		if !ok {
			return nil, uerr.Internalf("kernel: truncate cannot store %s", dt)
		}
		res[i] = v
	}
	return packTicks(name, out, res, valid), nil
}

// packTicks stores a tick slice at the output type's physical width.
func packTicks(name string, out dtype.DataType, res []int64, valid bitmap.View) *data.Column {
	if out.Physical().ID() == dtype.TypeInt32 {
		d32 := make([]int32, len(res))
		for i, v := range res {
			d32[i] = int32(v)
		}
		return data.NewFixed(name, out, d32, valid)
	}
	return data.NewFixed(name, out, res, valid)
}

func component(fn expr.CallFn, t time.Time) int32 {
	switch fn {
	case expr.FnDtYear:
		return int32(t.Year())
	case expr.FnDtMonth:
		return int32(t.Month())
	case expr.FnDtDay:
		return int32(t.Day())
	case expr.FnDtHour:
		return int32(t.Hour())
	case expr.FnDtMinute:
		return int32(t.Minute())
	case expr.FnDtSecond:
		return int32(t.Second())
	case expr.FnDtMillisecond:
		return int32(t.Nanosecond() / 1e6)
	case expr.FnDtMicrosecond:
		return int32(t.Nanosecond() / 1e3)
	case expr.FnDtNanosecond:
		return int32(t.Nanosecond())
	case expr.FnDtWeekday:
		// ISO: Monday is 1, Sunday is 7. Go's Weekday puts Sunday at 0, and using
		// that directly is the classic off-by-one in every date library.
		wd := int32(t.Weekday())
		if wd == 0 {
			return 7
		}
		return wd
	case expr.FnDtOrdinalDay:
		return int32(t.YearDay())
	case expr.FnDtQuarter:
		return int32((int(t.Month())-1)/3 + 1)
	case expr.FnDtWeek:
		// ISO 8601 week, which is NOT "day of year / 7" — the first week of a year
		// can begin in the previous one.
		_, wk := t.ISOWeek()
		return int32(wk)
	default:
		return 0
	}
}

// durationDivisor returns the nanoseconds per unit for the Total* family.
func durationDivisor(fn expr.CallFn) (int64, bool) {
	switch fn {
	case expr.FnDtTotalDays:
		return int64(24 * time.Hour), true
	case expr.FnDtTotalHours:
		return int64(time.Hour), true
	case expr.FnDtTotalMinutes:
		return int64(time.Minute), true
	case expr.FnDtTotalSeconds:
		return int64(time.Second), true
	default:
		return 0, false
	}
}

// truncateTemporal floors an instant to a multiple of a duration.
//
// The floor is toward negative infinity, so truncating an instant before the epoch
// moves it backwards like every other instant rather than jumping forward — the
// same asymmetry the rescale path avoids, for the same reason.
func truncateTemporal(name string, out, dt dtype.DataType, ticks []int64,
	valid bitmap.View, args []any) (*data.Column, error) {

	mo, _ := argInt(args, 0)
	dd, _ := argInt(args, 1)
	ns, _ := argInt(args, 2)
	iv := dtype.IntervalOf(int32(mo), int32(dd), ns)
	if iv.IsZero() || iv.Negative() {
		return nil, uerr.New(uerr.KindValue, "dt",
			"truncate needs a positive interval, got %s", iv).
			Hint(`pass a time.Duration or an interval, e.g. .Dt().Truncate(time.Hour) ` +
				`or .Dt().Truncate(ursus.Every("1mo"))`)
	}
	npt, has := dt.NanosPerTick()
	if !has {
		return nil, uerr.Internalf("kernel: truncate on %s", dt)
	}

	if iv.IsCalendar() {
		return truncateCalendar(name, out, dt, ticks, valid, iv)
	}
	every := iv.Nanos()
	step := every / npt
	if step <= 0 {
		// The interval is finer than the column's resolution, so every instant is
		// already on a boundary.
		step = 1
	}

	n := len(ticks)
	res := make([]int64, n)
	for i, t := range ticks {
		if !valid.Get(i) {
			continue
		}
		q := t / step
		if t%step != 0 && t < 0 {
			q--
		}
		res[i] = q * step
	}
	return packTicks(name, out, res, valid), nil
}

// locationOf resolves a Datetime's timezone once per column.
//
// A component must be read in the column's own zone or `hour` is wrong for every
// row: 2024-01-01T00:00:00+09:00 is hour 0 in Tokyo and hour 15 in UTC. An
// unloadable zone falls back to UTC rather than failing the query, matching what
// rows.go's timeSetter already does.
func locationOf(dt dtype.DataType) *time.Location {
	tz := dt.TimeZone()
	if tz == "" || tz == "UTC" {
		return nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil
	}
	return loc
}
