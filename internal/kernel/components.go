package kernel

import (
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Values from their components (step 265, G3 of v0.6.6-scope.md): Polars' date,
// datetime, time, duration, from_epoch and business_day_count. Each operand is a
// column of the batch's height or a literal one row long; a null operand gives null.
// Components that name no date or time are refused, naming them and their row, as
// Polars refuses them.

// intsOf is each operand as Int64, or as a column of type Null.
func intsOf(ops []*data.Column) ([]*data.Column, error) {
	out := make([]*data.Column, len(ops))
	for i, c := range ops {
		var err error
		if out[i], err = asInt64(c); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rowInts is row i of each column, and whether every one holds a value.
func rowInts(cols []*data.Column, i int, into []int64) bool {
	for k, c := range cols {
		v, ok := int64At(c, i)
		if !ok {
			return false
		}
		into[k] = v
	}
	return true
}

// civil is the instant of y-m-d h:mi:s.us in UTC, or what its components fail to
// name, "date" or "time": time.Date normalises a month 13 or an hour 25, which Polars
// refuses.
func civil(v []int64) (time.Time, string) {
	y, m, d, h, mi, s, us := v[0], v[1], v[2], v[3], v[4], v[5], v[6]
	if h < 0 || h > 23 || mi < 0 || mi > 59 || s < 0 || s > 59 || us < 0 || us > 999_999 {
		return time.Time{}, "time"
	}
	if y < -1_000_000 || y > 1_000_000 || m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, "date"
	}
	t := time.Date(int(y), time.Month(m), int(d), int(h), int(mi), int(s), int(us)*1000, time.UTC)
	if t.Day() != int(d) {
		return time.Time{}, "date"
	}
	return t, ""
}

// makeTemporal is date(y, m, d), datetime(y, m, d, h, mi, s, us) or time(h, mi, s,
// us) by the number of operands, of type out.
func makeTemporal(op, name string, out dtype.DataType, ops []*data.Column, n int) (*data.Column, error) {
	cols, err := intsOf(ops)
	if err != nil {
		return nil, err
	}
	v := make([]int64, 7)
	part := v // the components the operands give
	if len(cols) == 4 {
		v[0], v[1], v[2] = 1970, 1, 1
		part = v[3:]
	}
	res := make([]int64, n)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		clear(part)
		if !rowInts(cols, i, part) {
			valid.Append(false)
			continue
		}
		t, bad := civil(v)
		if bad != "" {
			text := make([]string, len(cols))
			for k, x := range part[:len(cols)] {
				text[k] = strconv.FormatInt(x, 10)
			}
			return nil, uerr.New(uerr.KindValue, op, "%s's components (%s) in row %d name no %s",
				op, strings.Join(text, ", "), i, bad)
		}
		var tick int64
		ok := true
		switch out.ID() {
		case dtype.TypeTime:
			tick = int64(t.Sub(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)))
		default:
			tick, ok = fromTime(out, t)
			if !ok {
				return nil, uerr.New(uerr.KindValue, op, "%s's components %v in row %d are outside the range of %s",
					op, part, i, out)
			}
		}
		res[i] = tick
		valid.Append(true)
	}
	return packTicks(name, out, res, valid.Finish()), nil
}

// tickFactors are a duration's parts in nanoseconds: weeks, days, hours, minutes,
// seconds, milliseconds, microseconds and nanoseconds.
var tickFactors = [8]int64{7 * 86_400e9, 86_400e9, 3_600e9, 60e9, 1e9, 1e6, 1e3, 1}

// durationTicks is duration(weeks, days, …, nanoseconds) in ticks of the unit whose
// nanoseconds per tick is the last operand, as Int64. Integer parts sum exactly, and
// a total past int64 is refused, where Polars wraps. With a float part the total is
// summed in float64 and truncated toward zero, as Polars sums it: 0.6µs and 0.0004ms
// make one microsecond.
func durationTicks(name string, ops []*data.Column, n int) (*data.Column, error) {
	npt := data.MustValues[int64](ops[8])[0]
	parts := ops[:8]
	floating := false
	for _, c := range parts {
		floating = floating || c.DType().IsFloat()
	}
	res := make([]int64, n)
	valid := bitmap.NewBuilder(n)
	refuse := func(i int) error {
		return uerr.New(uerr.KindValue, "duration", "duration's parts in row %d pass the range of a Duration", i).
			Hint("a coarser unit holds a longer duration")
	}
	if floating {
		cols := make([]*data.Column, len(parts))
		for k, c := range parts {
			if c.DType().IsNull() || c.DType() == dtype.Float64 {
				cols[k] = c
				continue
			}
			var err error
			if cols[k], err = Cast(c.Name(), dtype.Float64, true, c); err != nil {
				return nil, err
			}
		}
		for i := range n {
			total, ok := 0.0, true
			for k, c := range cols {
				r := rowOf(c, i)
				if c.DType().IsNull() || !c.IsValid(r) {
					ok = false
					break
				}
				total += data.MustValues[float64](c)[r] * float64(tickFactors[k]) / float64(npt)
			}
			valid.Append(ok)
			if !ok {
				continue
			}
			t := math.Trunc(total)
			if math.IsNaN(t) || t >= math.MaxInt64 || t < math.MinInt64 {
				return nil, refuse(i)
			}
			res[i] = int64(t)
		}
		return data.NewFixed(name, dtype.Int64, res, valid.Finish()), nil
	}
	cols, err := intsOf(parts)
	if err != nil {
		return nil, err
	}
	v := make([]int64, 8)
	for i := range n {
		if !rowInts(cols, i, v) {
			valid.Append(false)
			continue
		}
		// Each part in nanoseconds, then the total in ticks: a unit coarser than a
		// nanosecond divides each part's product exactly, but for nanoseconds and
		// microseconds below a millisecond, which are summed before dividing.
		var total, rest int64
		for k, x := range v {
			per := tickFactors[k]
			if per%npt == 0 {
				t, ok := mulInt64(x, per/npt)
				if !ok || overflowsI64Add(total, t) {
					return nil, refuse(i)
				}
				total += t
				continue
			}
			t, ok := mulInt64(x, per)
			if !ok || overflowsI64Add(rest, t) {
				return nil, refuse(i)
			}
			rest += t
		}
		if overflowsI64Add(total, rest/npt) {
			return nil, refuse(i)
		}
		res[i] = total + rest/npt
		valid.Append(true)
	}
	return data.NewFixed(name, dtype.Int64, res, valid.Finish()), nil
}

// mulInt64 is a*b, and whether int64 holds it.
func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	p := a * b
	return p, p/b == a && !(a == -1 && b == math.MinInt64) && !(b == -1 && a == math.MinInt64)
}

// overflowsI64Add reports whether a+b passes int64.
func overflowsI64Add(a, b int64) bool {
	return (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b)
}

// epochTicks is from_epoch's ticks: x times factor, as Int64. An integer x
// multiplies exactly, and a product past int64 is refused, where Polars wraps; a
// float x is truncated toward zero, as Polars truncates it.
func epochTicks(name string, x *data.Column, factor int64, n int) (*data.Column, error) {
	res := make([]int64, n)
	valid := bitmap.NewBuilder(n)
	refuse := func(i int) error {
		return uerr.New(uerr.KindValue, "from_epoch", "from_epoch's value in row %d passes the range of an instant", i)
	}
	if x.DType().IsFloat() {
		f := x
		if f.DType() != dtype.Float64 {
			var err error
			if f, err = Cast(x.Name(), dtype.Float64, true, x); err != nil {
				return nil, err
			}
		}
		vals := data.MustValues[float64](f)
		for i := range n {
			r := rowOf(f, i)
			if !f.IsValid(r) {
				valid.Append(false)
				continue
			}
			t := math.Trunc(vals[r] * float64(factor))
			if math.IsNaN(t) || t >= math.MaxInt64 || t < math.MinInt64 {
				return nil, refuse(i)
			}
			res[i] = int64(t)
			valid.Append(true)
		}
		return data.NewFixed(name, dtype.Int64, res, valid.Finish()), nil
	}
	c, err := asInt64(x)
	if err != nil {
		return nil, err
	}
	for i := range n {
		v, ok := int64At(c, i)
		if !ok {
			valid.Append(false)
			continue
		}
		t, fits := mulInt64(v, factor)
		if !fits {
			return nil, refuse(i)
		}
		res[i] = t
		valid.Append(true)
	}
	return data.NewFixed(name, dtype.Int64, res, valid.Finish()), nil
}

// weekdayOf is a day's weekday, Monday 0: 1970-01-01 was a Thursday.
func weekdayOf(day int64) int {
	return int(((day+3)%7 + 7) % 7)
}

// BusinessDays counts, between two days, the days of the week mask holds, Monday
// bit 0, that are not holidays.
type BusinessDays struct {
	Mask     uint8
	Holidays []int64 // the holidays mask holds, sorted and distinct
}

// NewBusinessDays is a mask and its holidays, those on a day it holds kept.
func NewBusinessDays(mask uint8, holidays []int64) BusinessDays {
	h := make([]int64, 0, len(holidays))
	for _, d := range holidays {
		if mask&(1<<weekdayOf(d)) != 0 {
			h = append(h, d)
		}
	}
	slices.Sort(h)
	return BusinessDays{Mask: mask, Holidays: slices.Compact(h)}
}

// Holds reports whether day is a business day.
func (b BusinessDays) Holds(day int64) bool {
	if b.Mask&(1<<weekdayOf(day)) == 0 {
		return false
	}
	_, holiday := slices.BinarySearch(b.Holidays, day)
	return !holiday
}

// Count is the business days in [s, e), and minus those in [e, s) where e is before
// s: Polars' business_day_count.
func (b BusinessDays) Count(s, e int64) int64 {
	if e < s {
		return -b.Count(e, s)
	}
	weeks := (e - s) / 7
	n := weeks * int64(bits.OnesCount8(b.Mask))
	for d := s + weeks*7; d < e; d++ {
		if b.Mask&(1<<weekdayOf(d)) != 0 {
			n++
		}
	}
	lo, _ := slices.BinarySearch(b.Holidays, s)
	hi, _ := slices.BinarySearch(b.Holidays, e)
	return n - int64(hi-lo)
}

// businessDayCount is business_day_count(start, end, mask, holidays): ops are the
// Date ends, the mask as an Int64 literal, and the holidays a List(Int64) literal of
// days since the epoch.
func businessDayCount(name string, ops []*data.Column, n int) (*data.Column, error) {
	mask := uint8(data.MustValues[int64](ops[2])[0])
	hl := ops[3].Lists()
	lo, hi, _ := hl.Get(0)
	var days []int64
	if lo < hi {
		days = data.MustValues[int64](hl.Child())[lo:hi]
	}
	b := NewBusinessDays(mask, days)
	ends := make([][]int64, 2)
	for k, c := range ops[:2] {
		if c.DType().IsNull() {
			return NullColumn(name, dtype.Int32, n)
		}
		t, err := readTicks(c)
		if err != nil {
			return nil, err
		}
		ends[k] = t
	}
	res := make([]int32, n)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		rs, re := rowOf(ops[0], i), rowOf(ops[1], i)
		ok := ops[0].IsValid(rs) && ops[1].IsValid(re)
		valid.Append(ok)
		if ok {
			res[i] = int32(b.Count(ends[0][rs], ends[1][re]))
		}
	}
	return data.NewFixed(name, dtype.Int32, res, valid.Finish()), nil
}
