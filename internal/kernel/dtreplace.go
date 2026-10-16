package kernel

import (
	"math"
	"math/bits"
	"slices"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Business days and replace (step 272, D of v0.6.6-scope.md): Polars'
// is_business_day, add_business_days and dt.replace, each a horizontal call, a value
// a row from its operands.

// Add is the day n business days after d, or before it where n is negative; d is a
// business day. Whole weeks are jumped, less the holidays in them, so a count of
// thousands takes a few steps.
func (b BusinessDays) Add(d, n int64) int64 {
	k := int64(bits.OnesCount8(b.Mask))
	for n != 0 {
		if n >= k || n <= -k {
			w := n / k
			nd := d + 7*w
			if w > 0 {
				// The business days in (d, nd]: w weeks' mask days, less the holidays.
				lo, _ := slices.BinarySearch(b.Holidays, d+1)
				hi, _ := slices.BinarySearch(b.Holidays, nd+1)
				n -= w*k - int64(hi-lo)
			} else {
				// And in [nd, d), going back.
				lo, _ := slices.BinarySearch(b.Holidays, nd)
				hi, _ := slices.BinarySearch(b.Holidays, d)
				n -= w*k + int64(hi-lo)
			}
			d = nd
			continue
		}
		step := int64(1)
		if n < 0 {
			step = -1
		}
		d += step
		if b.Holds(d) {
			n -= step
		}
	}
	return d
}

// roll is d, or where it is not a business day the next one, forward, or the one
// before, backward.
func (b BusinessDays) roll(d int64, forward bool) int64 {
	for !b.Holds(d) {
		if forward {
			d++
		} else {
			d--
		}
	}
	return d
}

// calendarOf is a horizontal call's calendar operands, as business_day_count's.
func calendarOf(mask, holidays *data.Column) BusinessDays {
	hl := holidays.Lists()
	lo, hi, _ := hl.Get(0)
	var days []int64
	if lo < hi {
		days = data.MustValues[int64](hl.Child())[lo:hi]
	}
	return NewBusinessDays(uint8(data.MustValues[int64](mask)[0]), days)
}

// wallClock reads a Date or a Datetime's values as their local wall clocks: the day,
// the second within it and what is left in ticks, so a value can be rebuilt from
// them. A Date's day is its value. zc is the value's zone.
type wallClock struct {
	dt     dtype.DataType
	zc     *zoneClock
	perSec int64 // a Datetime's ticks a second
}

func newWallClock(dt dtype.DataType) (*wallClock, error) {
	w := &wallClock{dt: dt}
	if dt.ID() != dtype.TypeDatetime {
		return w, nil
	}
	npt, _ := dt.NanosPerTick()
	w.perSec = int64(time.Second) / npt
	zc, err := newZoneClock(dt.TimeZone())
	if err != nil {
		return nil, err
	}
	w.zc = zc
	return w, nil
}

// read is t's local day, its second within the day, and its ticks within the second.
func (w *wallClock) read(t int64) (day, sec, sub int64) {
	if w.zc == nil {
		return t, 0, 0
	}
	s := floorDiv(t, w.perSec)
	wall := s + w.zc.offsetAt(s)
	day = floorDiv(wall, 86_400)
	return day, wall - day*86_400, t - s*w.perSec
}

// build is the value whose local wall clock is day, sec and sub, a fold read by the
// policy ambiguous and a gap by nonExistent: its ticks and whether it is a value, or
// a refusal.
func (w *wallClock) build(fn expr.CallFn, day, sec, sub int64, ambiguous, nonExistent string) (int64, bool, error) {
	if w.zc == nil {
		if day < math.MinInt32 || day > math.MaxInt32 {
			return 0, false, uerr.New(uerr.KindValue, "dt", "%s gives a date outside the range of a Date", fn)
		}
		return day, true, nil
	}
	wall := day*86_400 + sec
	at, n := w.zc.instants(wall)
	naive := dtype.Datetime(w.dt.TimeUnit(), "")
	text := func() string {
		if overflowsI64(expr.OpMul, wall, w.perSec) {
			return "out of range"
		}
		return dtype.FormatTemporal(naive, wall*w.perSec+sub)
	}
	var inst int64
	switch {
	case n == 1:
		inst = at[0]
	case n == 0 && nonExistent == expr.ZoneNull, n > 1 && ambiguous == expr.ZoneNull:
		return 0, false, nil
	case n == 0:
		return 0, false, uerr.New(uerr.KindValue, "dt",
			"%s gives the wall clock %s, which does not exist in %q: its clocks skip it",
			fn, text(), w.dt.TimeZone())
	case ambiguous == expr.ZoneEarliest:
		inst = at[0]
	case ambiguous == expr.ZoneLatest:
		inst = at[n-1]
	default:
		return 0, false, uerr.New(uerr.KindValue, "dt",
			"%s gives the wall clock %s, which is ambiguous in %q: its clocks read it twice",
			fn, text(), w.dt.TimeZone()).
			Hint("pass ursus.AmbiguousEarliest, ursus.AmbiguousLatest or ursus.AmbiguousNull to choose")
	}
	if overflowsI64(expr.OpMul, inst, w.perSec) || overflowsI64(expr.OpAdd, inst*w.perSec, sub) {
		return 0, false, uerr.New(uerr.KindValue, "dt", "%s gives a value outside the range of %s", fn, w.dt).
			Hint(widerUnitHint)
	}
	return inst*w.perSec + sub, true, nil
}

// isBusinessDay is whether each value's local date is a business day: ops are the
// value and the calendar.
func isBusinessDay(name string, ops []*data.Column, n int) (*data.Column, error) {
	v := ops[0]
	if v.DType().IsNull() {
		return NullColumn(name, dtype.Bool, n)
	}
	b := calendarOf(ops[1], ops[2])
	w, err := newWallClock(v.DType())
	if err != nil {
		return nil, err
	}
	ticks, err := readTicks(v)
	if err != nil {
		return nil, err
	}
	holds := bitmap.NewBuilder(n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		r := rowOf(v, i)
		valid := v.IsValid(r)
		day := int64(0)
		if valid {
			day, _, _ = w.read(ticks[r])
		}
		holds.Append(valid && b.Holds(day))
		ok.Append(valid)
	}
	return data.NewBool(name, holds.Finish(), ok.Finish()), nil
}

// addBusinessDays is each value n business days on, its time of day kept: ops are the
// value, n, the calendar and the roll. A value not on a business day is refused, or
// rolled to the next or the one before, first, as Polars rolls it.
func addBusinessDays(name string, out dtype.DataType, ops []*data.Column, n int) (*data.Column, error) {
	v, count := ops[0], ops[1]
	if v.DType().IsNull() || count.DType().IsNull() {
		return NullColumn(name, out, n)
	}
	b := calendarOf(ops[2], ops[3])
	roll := ops[4].Strings().Get(0)
	nc, err := asInt64(count)
	if err != nil {
		return nil, err
	}
	w, err := newWallClock(v.DType())
	if err != nil {
		return nil, err
	}
	ticks, err := readTicks(v)
	if err != nil {
		return nil, err
	}
	res := make([]int64, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		r := rowOf(v, i)
		k, kok := int64At(nc, i)
		if !v.IsValid(r) || !kok {
			ok.Append(false)
			continue
		}
		day, sec, sub := w.read(ticks[r])
		if !b.Holds(day) {
			if roll == expr.RollRaise {
				return nil, uerr.New(uerr.KindValue, "dt.add_business_days",
					"%s is not on a business day", dtype.FormatTemporal(v.DType(), ticks[r])).
					Hint("ursus.RollForward or ursus.RollBackward moves it to the next or the last first")
			}
			day = b.roll(day, roll == expr.RollForward)
		}
		t, isValue, err := w.build(expr.FnDtAddBusinessDays, b.Add(day, k), sec, sub, expr.ZoneRaise, expr.ZoneRaise)
		if err != nil {
			return nil, err
		}
		res[i] = t
		ok.Append(isValue)
	}
	return packTicks(name, out, res, ok.Finish()), nil
}

// dtReplace is each value with the components given set, on its local wall clock:
// Polars' dt.replace. ops are the value; the year, month, day, hour, minute, second
// and microsecond, each a null literal where not given, a null in a column keeping
// the value's own too; then the ambiguous and non_existent policies. A Date's time
// is not read. A microsecond replaces the whole fraction of the second. Components
// that are no date or time, the 30th of February or an hour of 24, are refused, as
// Polars refuses them.
func dtReplace(name string, out dtype.DataType, ops []*data.Column, n int) (*data.Column, error) {
	v := ops[0]
	dt := v.DType()
	ambiguous := ops[8].Strings().Get(0)
	nonExistent := ops[9].Strings().Get(0)
	parts := make([]*data.Column, 7)
	for k, c := range ops[1:8] {
		p, err := asInt64(c)
		if err != nil {
			return nil, err
		}
		parts[k] = p
	}
	w, err := newWallClock(dt)
	if err != nil {
		return nil, err
	}
	ticks, err := readTicks(v)
	if err != nil {
		return nil, err
	}
	npt, _ := dt.NanosPerTick()
	isDate := dt.ID() == dtype.TypeDate
	res := make([]int64, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		r := rowOf(v, i)
		if !v.IsValid(r) {
			ok.Append(false)
			continue
		}
		day, sec, sub := w.read(ticks[r])
		civil := time.Unix(day*86_400+sec, 0).UTC()
		cur := []int64{int64(civil.Year()), int64(civil.Month()), int64(civil.Day()),
			int64(civil.Hour()), int64(civil.Minute()), int64(civil.Second()), sub * npt / 1000}
		set := cur
		for k, p := range parts {
			if x, given := int64At(p, i); given {
				if k == 6 {
					sub = x * 1000 / npt
				}
				set[k] = x
			}
		}
		y, mo, d := set[0], set[1], set[2]
		if mo < 1 || mo > 12 || d < 1 || d > int64(daysIn(y, mo)) {
			return nil, uerr.New(uerr.KindValue, "dt.replace",
				"replace gives no date: year %d, month %d, day %d", y, mo, d)
		}
		h, mi, s, us := set[3], set[4], set[5], set[6]
		if !isDate && (h < 0 || h > 23 || mi < 0 || mi > 59 || s < 0 || s > 59 || us < 0 || us > 999_999) {
			return nil, uerr.New(uerr.KindValue, "dt.replace",
				"replace gives no time of day: hour %d, minute %d, second %d, microsecond %d", h, mi, s, us)
		}
		if y < -1_000_000 || y > 1_000_000 {
			return nil, uerr.New(uerr.KindValue, "dt.replace", "replace gives the year %d, outside the range of %s", y, dt)
		}
		nday := floorDiv(time.Date(int(y), time.Month(mo), int(d), 0, 0, 0, 0, time.UTC).Unix(), 86_400)
		t, isValue, err := w.build(expr.FnDtReplace, nday, h*3600+mi*60+s, sub, ambiguous, nonExistent)
		if err != nil {
			return nil, err
		}
		res[i] = t
		ok.Append(isValue)
	}
	return packTicks(name, out, res, ok.Finish()), nil
}

// daysIn is the length of month mo of year y.
func daysIn(y, mo int64) int {
	return time.Date(int(y), time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
