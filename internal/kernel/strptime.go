package kernel

import (
	"fmt"
	"math"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/strftime"
	"github.com/advenn/ursus/internal/uerr"
)

// strptimeCall parses a String column with a strftime-style format into out — a
// Date, Datetime or Time — as str.to_date, str.to_datetime and str.to_time.
//
// A value that does not match the format, or names a date that does not exist, is
// an error naming it when strict and a null otherwise. A Datetime takes its
// instant from a parsed offset (%z) when the format reads one; otherwise the wall
// clock is read in the column's zone, or kept as it is for a naive one. Digits
// finer than the unit are dropped, toward the past.
func strptimeCall(fn expr.CallFn, name string, out dtype.DataType, c *data.Column, args []any) (*data.Column, error) {
	src, _ := argString(args, 0)
	strict, _ := argBool(args, 1)
	f, err := strftime.Compile(src)
	if err != nil {
		return nil, uerr.Internalf("kernel: %s: the format %q did not compile: %v", fn, src, err)
	}
	loc := time.UTC
	if out.ID() == dtype.TypeDatetime {
		if l := locationOf(out); l != nil {
			loc = l
		}
	}
	perTick := int64(1)
	if out.ID() != dtype.TypeDate {
		npt, ok := out.NanosPerTick()
		if !ok {
			return nil, uerr.Internalf("kernel: %s to %s has no tick", fn, out)
		}
		perTick = npt
	}

	acc := c.Strings()
	n := c.Len()
	in := c.Validity()
	ticks := make([]int64, n)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		if !in.Get(i) {
			valid.Append(false)
			continue
		}
		s := acc.Get(i)
		p, perr := f.Parse(s)
		var v int64
		if perr == nil {
			v, perr = toTicks(p, out.ID(), loc, perTick)
		}
		if perr != nil {
			if strict {
				return nil, uerr.New(uerr.KindValue, fn.String(),
					"%q does not parse with the format %q: %v", s, src, perr).
					Hint("it is row %d of its batch", i).
					Hint("pass strict=false to get a null for every value that does not parse")
			}
			valid.Append(false)
			continue
		}
		ticks[i] = v
		valid.Append(true)
	}
	if out.ID() == dtype.TypeDate {
		days := make([]int32, n)
		for i, v := range ticks {
			days[i] = int32(v)
		}
		return data.NewFixed(name, out, days, valid.Finish()), nil
	}
	return data.NewFixed(name, out, ticks, valid.Finish()), nil
}

type rangeErr struct{}

func (rangeErr) Error() string { return "it is outside the range of the type" }

var errOutOfRange error = rangeErr{}

// toTicks is a parsed value as the target type's storage: days for a Date, ticks of
// perTick nanoseconds since the epoch for a Datetime, since midnight for a Time.
func toTicks(p strftime.Fields, id dtype.TypeID, loc *time.Location, perTick int64) (int64, error) {
	switch id {
	case dtype.TypeDate:
		days := time.Date(p.Year, time.Month(p.Month), p.Day, 0, 0, 0, 0, time.UTC).Unix() / 86400
		if days < math.MinInt32 || days > math.MaxInt32 {
			return 0, errOutOfRange
		}
		return days, nil
	case dtype.TypeTime:
		secs := int64(p.Hour*3600 + p.Minute*60 + p.Second)
		return secs*(1e9/perTick) + int64(p.Nanos)/perTick, nil
	default: // Datetime
		zone := loc
		if p.HasZone {
			zone = time.FixedZone("", p.Offset)
		}
		t := time.Date(p.Year, time.Month(p.Month), p.Day, p.Hour, p.Minute, p.Second, p.Nanos, zone)
		if !p.HasZone && zone != time.UTC {
			if err := oneInstant(t, p, zone); err != nil {
				return 0, err
			}
		}
		perSec := 1e9 / perTick
		secs := t.Unix()
		if secs > math.MaxInt64/perSec-1 || secs < math.MinInt64/perSec+1 {
			return 0, errOutOfRange
		}
		return secs*perSec + int64(t.Nanosecond())/perTick, nil
	}
}

// oneInstant reports whether the wall clock p names is exactly one instant in loc,
// given t, the instant time.Date chose for it.
//
// time.Date answers both failures silently. A time the clocks skip, 02:30 on the
// morning New York springs forward, is moved by the gap, to 03:30 EDT; one they pass
// twice, 01:30 on the morning it falls back, becomes either. Polars raises on both,
// and so does a strict parse here; a lenient one gives null.
func oneInstant(t time.Time, p strftime.Fields, loc *time.Location) error {
	if !sameWallClock(t, p) {
		return fmt.Errorf("it does not exist in %s: the clocks skip it", loc)
	}
	// A wall clock happens twice only across a transition, where the offset changes:
	// the other instant is t moved by the difference. Three hours either side finds
	// every transition there is, and away from one this is two offset lookups.
	_, off := t.Zone()
	for _, d := range []time.Duration{-3 * time.Hour, 3 * time.Hour} {
		if _, o := t.Add(d).Zone(); o != off {
			other := t.Add(time.Duration(off-o) * time.Second)
			if !other.Equal(t) && sameWallClock(other.In(loc), p) {
				return fmt.Errorf("it is ambiguous in %s: the clocks pass it twice", loc)
			}
		}
	}
	return nil
}

func sameWallClock(t time.Time, p strftime.Fields) bool {
	return t.Year() == p.Year && int(t.Month()) == p.Month && t.Day() == p.Day &&
		t.Hour() == p.Hour && t.Minute() == p.Minute && t.Second() == p.Second
}

// strftimeCall formats a Date, Datetime or Time column with a strftime-style
// format, as dt.strftime: a zoned Datetime in its own zone's wall clock.
func strftimeCall(name string, c *data.Column, args []any) (*data.Column, error) {
	src, _ := argString(args, 0)
	f, err := strftime.Compile(src)
	if err != nil {
		return nil, uerr.Internalf("kernel: dt.strftime: the format %q did not compile: %v", src, err)
	}
	dt := c.DType()
	ticks, err := readTicks(c)
	if err != nil {
		return nil, err
	}
	var (
		perTick int64 = 1
		loc           = locationOf(dt)
		zoned         = dt.ID() == dtype.TypeDatetime && dt.TimeZone() != ""
	)
	if dt.ID() != dtype.TypeDate {
		npt, ok := dt.NanosPerTick()
		if !ok {
			return nil, uerr.Internalf("kernel: dt.strftime on %s has no tick", dt)
		}
		perTick = npt
	}
	perSec := 1e9 / perTick

	n := c.Len()
	in := c.Validity()
	offs := make([]int32, 1, n+1)
	var chars []byte
	for i := range n {
		if in.Get(i) {
			var t time.Time
			if dt.ID() == dtype.TypeDate {
				t = time.Unix(ticks[i]*86400, 0).UTC()
			} else {
				secs, sub := ticks[i]/perSec, ticks[i]%perSec
				if sub < 0 {
					secs, sub = secs-1, sub+perSec
				}
				t = time.Unix(secs, sub*perTick).UTC()
				if loc != nil {
					t = t.In(loc)
				}
			}
			if chars, err = f.Append(chars, t, zoned); err != nil {
				return nil, uerr.Internalf("kernel: dt.strftime on %s: %v", dt, err)
			}
		}
		offs = append(offs, int32(len(chars)))
	}
	return data.NewStringParts(name, offs, chars, in), nil
}
