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

	switch fn {
	case expr.FnDtStrftime:
		return strftimeCall(name, c, args)
	case expr.FnDtConvertTimeZone:
		return convertTimeZone(name, out, c)
	}

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
		// The goal the old comment stated — a coarse unit must not floor to zero —
		// was implemented as `t * npt / per`, and the multiply wrapped: a
		// Duration(s) of three centuries total_days'd to -103928. It never needed a
		// multiply. Every total is a whole number of ticks (per >= 1e9 >= npt at
		// every unit), so per/npt is an integer and t/(per/npt) is the SAME rational
		// floored the same way, with nothing to overflow.
		ticksPer := per / npt
		if ticksPer <= 0 || per%npt != 0 {
			return nil, uerr.Internalf("kernel: %s on %s has no whole tick ratio", fn, dt)
		}
		vals := make([]int64, n)
		for i, t := range ticks {
			if !valid.Get(i) {
				continue
			}
			vals[i] = t / ticksPer
		}
		return data.NewFixed(name, out, vals, valid), nil
	}

	if fn == expr.FnDtEpoch {
		npt, has := dt.NanosPerTick()
		if !has {
			return nil, uerr.Internalf("kernel: %s on %s", fn, dt)
		}
		// Same defect, one function over, and this one is reachable from an ordinary
		// column: a Date is 86_400e9 nanos per tick, so `t * npt` wrapped for every
		// date past 2262-04-11 and Dt().Epoch() on 2300-06-01 answered -8019905673.
		//
		// A tick is either a whole number of seconds (Date, Datetime(s)) or a whole
		// fraction of one, so exactly one of these is a multiply and it is bounded:
		// int32 days times 86_400 cannot leave int64.
		const nsPerSec = int64(time.Second)
		vals := make([]int64, n)
		for i, t := range ticks {
			if !valid.Get(i) {
				continue
			}
			if npt >= nsPerSec {
				secPerTick := npt / nsPerSec
				if overflowsI64(expr.OpMul, t, secPerTick) {
					return nil, uerr.New(uerr.KindValue, "dt",
						"%s at row %d is too far from the epoch to count in seconds",
						dtype.FormatTemporal(dt, t), i)
				}
				vals[i] = t * secPerTick
				continue
			}
			// Floored, not truncated: the epoch second an instant lies IN, so
			// 1969-12-31T23:59:59.5 is -1, as Truncate and Polars floor. Go's / rounds
			// toward zero and answered 0, the second after it.
			per := nsPerSec / npt
			q := t / per
			if t%per != 0 && t < 0 {
				q--
			}
			vals[i] = q
		}
		return data.NewFixed(name, out, vals, valid), nil
	}

	switch fn {
	case expr.FnDtTruncate, expr.FnDtRound:
		return truncateTemporal(fn, name, out, dt, ticks, valid, args)
	case expr.FnDtOffsetBy:
		return offsetBy(name, out, dt, ticks, valid, args)
	case expr.FnDtMonthStart, expr.FnDtMonthEnd:
		move := dtype.MonthStart
		if fn == expr.FnDtMonthEnd {
			move = dtype.MonthEnd
		}
		// Each moves by less than a month, but the first of 1677-09 is before
		// Datetime(ns) begins, and the end of 2262-04 after it.
		return eachInstant(fn, name, out, dt, ticks, valid, move, func(t int64) error {
			return uerr.New(uerr.KindValue, "dt",
				"%s of %s is outside the range of %s", fn, dtype.FormatTemporal(dt, t), dt).
				Hint(widerUnitHint)
		})
	}

	loc := locationOf(dt)
	if fn == expr.FnDtIsLeapYear {
		if !isDateLike(dt) {
			return nil, uerr.Internalf("kernel: %s on %s", fn, dt)
		}
		bits := bitmap.NewBuilder(n)
		for i, t := range ticks {
			leap := false
			if valid.Get(i) {
				tm, ok := dt.ToTime(t)
				if !ok {
					return nil, uerr.Internalf("kernel: %s cannot read %s as an instant", fn, dt)
				}
				if loc != nil {
					tm = tm.In(loc)
				}
				leap = dtype.IsLeapYear(tm.Year())
			}
			bits.Append(leap)
		}
		return data.NewBool(name, bits.Finish(), valid), nil
	}

	// Calendar components.
	vals := make([]int32, n)
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
func truncateCalendar(fn expr.CallFn, name string, out, dt dtype.DataType, ticks []int64,
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
		var at time.Time
		if fn == expr.FnDtRound {
			at = iv.RoundTo(tm)
		} else {
			at = iv.TruncateTo(tm)
		}
		v, ok := fromTime(dt, at)
		if !ok {
			// A floor only moves back, so near the earliest instant the type holds
			// it can leave the type: 1677-09-21 floored to the month is 1677-09-01,
			// before Datetime(ns) begins; and a round can move forward past the
			// latest. Data reaches this, so it is the data's error, not an internal
			// one.
			return nil, gridRange(fn, dt, iv, t)
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

// truncateTemporal floors an instant to a multiple of a duration, or rounds it to
// the nearer multiple, halfway up, for dt.round.
//
// The floor is toward negative infinity, so truncating an instant before the epoch
// moves it backwards like every other instant rather than jumping forward — the
// same asymmetry the rescale path avoids, for the same reason.
func truncateTemporal(fn expr.CallFn, name string, out, dt dtype.DataType, ticks []int64,
	valid bitmap.View, args []any) (*data.Column, error) {

	mo, _ := argInt(args, 0)
	dd, _ := argInt(args, 1)
	ns, _ := argInt(args, 2)
	iv := dtype.IntervalOf(int32(mo), int32(dd), ns)
	// The plan-time rule, asked again: DtCall is reachable without resolving.
	if err := expr.GridRefusal(fn, iv, dt); err != nil {
		return nil, err
	}
	npt, has := dt.NanosPerTick()
	if !has {
		return nil, uerr.Internalf("kernel: %s on %s", fn, dt)
	}

	// An Interval floors the column's wall clock, sub-day included, so it takes the
	// calendar path whenever the column has a zone; a Duration (wall == 0) floors
	// the absolute instant below. Without a zone the two grids are one, and the
	// tick arithmetic below is the faster way to it.
	if wall, _ := argInt(args, 3); iv.IsCalendar() || (wall == 1 && locationOf(dt) != nil) {
		return truncateCalendar(fn, name, out, dt, ticks, valid, iv)
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
		// The floor of an instant within one step of the earliest tick is below
		// it, and q*step wrapped: 1677-09-21T00:12:43 floored to the hour at
		// Datetime(ns) came back in 2262.
		if overflowsI64(expr.OpMul, q, step) {
			return nil, gridRange(fn, dt, iv, t)
		}
		v := q * step
		// Halfway rounds up, as Polars does: t-v is in [0, step), so neither side of
		// the comparison can overflow, and only the move to the next one can.
		if r := t - v; fn == expr.FnDtRound && r >= step-r {
			if overflowsI64(expr.OpAdd, v, step) {
				return nil, gridRange(fn, dt, iv, t)
			}
			v += step
		}
		if !fitsTicks(dt, v) {
			return nil, gridRange(fn, dt, iv, t)
		}
		res[i] = v
	}
	return packTicks(name, out, res, valid), nil
}

// gridRange refuses a floor or a round the column's type cannot hold. Polars wraps
// it.
func gridRange(fn expr.CallFn, dt dtype.DataType, iv dtype.Interval, t int64) error {
	verb := "truncate"
	if fn == expr.FnDtRound {
		verb = "round"
	}
	return uerr.New(uerr.KindValue, "dt",
		"%s of %s by %s is outside the range of %s", verb, dtype.FormatTemporal(dt, t), iv, dt).
		Hint(widerUnitHint)
}

const widerUnitHint = "a coarser unit holds a wider range: cast first, e.g. " +
	".Cast(ursus.Datetime(ursus.Micro, \"\"))"

// fromTime is dt.FromTime, refusing a Date past an int32 of days as well, which
// FromTime returns as an int64 and packTicks would wrap.
func fromTime(dt dtype.DataType, t time.Time) (int64, bool) {
	v, ok := dt.FromTime(t)
	return v, ok && fitsTicks(dt, v)
}

// fitsTicks reports whether v fits dt's physical width.
func fitsTicks(dt dtype.DataType, v int64) bool {
	return dt.Physical().ID() != dtype.TypeInt32 || (v >= math.MinInt32 && v <= math.MaxInt32)
}

func isDateLike(dt dtype.DataType) bool {
	return dt.ID() == dtype.TypeDate || dt.ID() == dtype.TypeDatetime
}

// convertTimeZone relabels a zoned Datetime with the zone out names: the ticks are
// UTC instants, so the same ticks are the same instants, and only where their wall
// clocks are read changes.
func convertTimeZone(name string, out dtype.DataType, c *data.Column) (*data.Column, error) {
	dt := c.DType()
	if dt.ID() != dtype.TypeDatetime || dt.TimeZone() == "" ||
		out.ID() != dtype.TypeDatetime || out.TimeUnit() != dt.TimeUnit() || out.TimeZone() == "" {
		return nil, uerr.Internalf("kernel: convert_time_zone from %s to %s", dt, out)
	}
	return c.WithDType(out).Rename(name), nil
}

// offsetBy moves each instant by an interval: its months and days on the wall clock
// of the column's zone, its nanoseconds in elapsed time, as dtype.Interval.AddTo
// does. Without months or days the move is a whole number of ticks
// (expr.OffsetRefusal), added to each.
func offsetBy(name string, out, dt dtype.DataType, ticks []int64, valid bitmap.View,
	args []any) (*data.Column, error) {

	mo, _ := argInt(args, 0)
	dd, _ := argInt(args, 1)
	ns, _ := argInt(args, 2)
	iv := dtype.IntervalOf(int32(mo), int32(dd), ns)
	if !isDateLike(dt) {
		return nil, uerr.Internalf("kernel: offset_by on %s", dt)
	}
	if err := expr.OffsetRefusal(iv, dt); err != nil {
		return nil, err
	}
	refuse := func(t int64) error {
		return uerr.New(uerr.KindValue, "dt",
			"offset_by of %s by %s is outside the range of %s", dtype.FormatTemporal(dt, t), iv, dt).
			Hint(widerUnitHint)
	}
	if iv.IsCalendar() {
		return eachInstant(expr.FnDtOffsetBy, name, out, dt, ticks, valid, iv.AddTo, refuse)
	}
	npt, _ := dt.NanosPerTick()
	by := iv.Nanos() / npt
	res := make([]int64, len(ticks))
	for i, t := range ticks {
		if !valid.Get(i) {
			continue
		}
		if overflowsI64(expr.OpAdd, t, by) || !fitsTicks(dt, t+by) {
			return nil, refuse(t)
		}
		res[i] = t + by
	}
	return packTicks(name, out, res, valid), nil
}

// eachInstant applies move to each value, read as a time.Time in the column's zone,
// and stores the instant it gives at the column's unit, or refuses the value whose
// answer the unit cannot hold. offset_by, month_start and month_end are this.
func eachInstant(fn expr.CallFn, name string, out, dt dtype.DataType, ticks []int64,
	valid bitmap.View, move func(time.Time) time.Time, refuse func(int64) error,
) (*data.Column, error) {

	if !isDateLike(dt) {
		return nil, uerr.Internalf("kernel: %s on %s", fn, dt)
	}
	loc := locationOf(dt)
	res := make([]int64, len(ticks))
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
		v, ok := fromTime(dt, move(tm))
		if !ok {
			return nil, refuse(t)
		}
		res[i] = v
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
