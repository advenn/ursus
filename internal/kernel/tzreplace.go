package kernel

import (
	"math"
	"slices"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// replaceTimeZone is Polars' dt.replace_time_zone: each value's wall clock, read in
// its own zone, or as it is when naive, is read again in the zone out names, so the
// instant moves and the clock does not. out's zone "" makes the values naive.
//
// A wall clock the new zone reads twice, in a fall-back fold, or never, in a
// spring-forward gap, is answered by the policies in args[1] and args[2]
// (expr.ZoneRaise and the rest), as Polars answers it.
//
// Every value moves by a whole number of seconds, the old offset less the new, so
// the ticks are shifted in integers, and the time package is asked only where an
// offset may change (zoneClock).
func replaceTimeZone(name string, out dtype.DataType, c *data.Column, args []any) (*data.Column, error) {
	dt := c.DType()
	if dt.ID() != dtype.TypeDatetime || out.ID() != dtype.TypeDatetime || out.TimeUnit() != dt.TimeUnit() {
		return nil, uerr.Internalf("kernel: replace_time_zone from %s to %s", dt, out)
	}
	// A zone's own wall clocks are its instants, a fold's second reading included:
	// Polars answers the same zone with the values as they are, and so does this.
	if dt.TimeZone() == out.TimeZone() {
		return c.WithDType(out).Rename(name), nil
	}
	ambiguous, _ := argString(args, 1)
	nonExistent, _ := argString(args, 2)
	from, err := newZoneClock(dt.TimeZone())
	if err != nil {
		return nil, err
	}
	to, err := newZoneClock(out.TimeZone())
	if err != nil {
		return nil, err
	}
	ticks, err := readTicks(c)
	if err != nil {
		return nil, err
	}
	npt, _ := dt.NanosPerTick()
	perSec := int64(time.Second) / npt
	valid := c.Validity()
	res := make([]int64, len(ticks))
	var dropped []bool // the rows a policy made null, made when one does
	for i, t := range ticks {
		if !valid.Get(i) {
			continue
		}
		sec := t / perSec // floored: the second the instant lies in
		if t%perSec < 0 {
			sec--
		}
		off := from.offsetAt(sec)
		if overflowsI64(expr.OpAdd, sec, off) {
			return nil, uerr.New(uerr.KindValue, "dt",
				"replace_time_zone of %s to %q is outside the range of %s",
				dtype.FormatTemporal(dt, t), out.TimeZone(), out).Hint(widerUnitHint)
		}
		wall := sec + off
		at, n := to.instants(wall)
		var inst int64
		switch {
		case n == 1:
			inst = at[0]
		case n == 0 && nonExistent == expr.ZoneNull,
			n > 1 && ambiguous == expr.ZoneNull:
			if dropped == nil {
				dropped = make([]bool, len(ticks))
			}
			dropped[i] = true
			continue
		case n == 0:
			return nil, uerr.New(uerr.KindValue, "dt",
				"the wall clock %s does not exist in %q: its clocks skip it",
				wallText(dt, t, wall-sec, perSec), out.TimeZone()).
				Hint("ReplaceTimeZone(tz, ursus.NonExistentNull) makes such a value null")
		case ambiguous == expr.ZoneEarliest:
			inst = at[0]
		case ambiguous == expr.ZoneLatest:
			inst = at[n-1]
		default:
			return nil, uerr.New(uerr.KindValue, "dt",
				"the wall clock %s is ambiguous in %q: its clocks read it twice",
				wallText(dt, t, wall-sec, perSec), out.TimeZone()).
				Hint("pass ursus.AmbiguousEarliest, ursus.AmbiguousLatest or ursus.AmbiguousNull to choose")
		}
		// The shift is within a few days' seconds, so only the add can leave int64.
		shift := (inst - sec) * perSec
		if overflowsI64(expr.OpAdd, t, shift) {
			return nil, uerr.New(uerr.KindValue, "dt",
				"replace_time_zone of %s to %q is outside the range of %s",
				dtype.FormatTemporal(dt, t), out.TimeZone(), out).Hint(widerUnitHint)
		}
		res[i] = t + shift
	}
	if dropped != nil {
		ok := bitmap.NewBuilder(len(ticks))
		for i := range ticks {
			ok.Append(valid.Get(i) && !dropped[i])
		}
		valid = ok.Finish()
	}
	return data.NewFixed(name, out, res, valid), nil
}

// wallText renders the wall clock of the instant t, offset seconds east, as a naive
// value of t's unit, for a refusal to name.
func wallText(dt dtype.DataType, t, offset, perSec int64) string {
	naive := dtype.Datetime(dt.TimeUnit(), "")
	if overflowsI64(expr.OpAdd, t, offset*perSec) {
		return dtype.FormatTemporal(dt, t)
	}
	return dtype.FormatTemporal(naive, t+offset*perSec)
}

// zoneClock answers a zone's offsets, keeping the last period of constant offset it
// was asked about, so a column of nearby values asks the time package once per
// transition rather than once per row. A naive column, or UTC, is offset 0
// throughout and has no location.
type zoneClock struct {
	loc    *time.Location
	lo, hi int64 // the kept period, [lo, hi) in Unix seconds
	off    int64 // the offset in force throughout it, seconds east of UTC
	kept   bool
}

// zoneMargin is two days: farther than any two offsets differ, so an instant two days
// clear of its period's ends is the only one with its wall clock.
const zoneMargin = 2 * 24 * 60 * 60

// zoneSpan bounds the seconds the time package is asked about, about 34,000 years
// either side of 1970: beyond it a zone's offset is taken as at its edge, which keeps
// the time package's own arithmetic in range.
const zoneSpan = 1 << 40

func newZoneClock(tz string) (*zoneClock, error) {
	if tz == "" || tz == "UTC" {
		return &zoneClock{}, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, uerr.Internalf("kernel: the time zone %q passed planning but does not load: %v", tz, err)
	}
	return &zoneClock{loc: loc}, nil
}

// offsetAt is the zone's offset at the instant s, in seconds east of UTC.
func (z *zoneClock) offsetAt(s int64) int64 {
	if z.loc == nil {
		return 0
	}
	if z.kept && s >= z.lo && s < z.hi {
		return z.off
	}
	t := time.Unix(min(max(s, -zoneSpan), zoneSpan), 0).In(z.loc)
	_, off := t.Zone()
	start, end := t.ZoneBounds()
	z.lo, z.hi, z.off, z.kept = math.MinInt64, math.MaxInt64, int64(off), true
	if !start.IsZero() && s > -zoneSpan {
		z.lo = start.Unix()
	}
	if !end.IsZero() && s < zoneSpan {
		z.hi = end.Unix()
	}
	return z.off
}

// instants are the instants at which the zone's wall clock reads w seconds past the
// epoch, read as if it were UTC, earliest first: one, none in a gap, or two in a
// fold. n is how many; a third reading is not known in any zone, and is not looked
// for.
func (z *zoneClock) instants(w int64) (at [2]int64, n int) {
	if z.loc == nil {
		return [2]int64{w}, 1
	}
	if w <= -zoneSpan || w >= zoneSpan {
		return [2]int64{w - z.offsetAt(w)}, 1
	}
	// Two days clear of the kept period's ends, the reading at its offset is the only
	// one: any other offset's instant lies within two days of it, inside the period,
	// where that offset is not in force.
	if s := w - z.off; z.kept && z.lo < s-zoneMargin && s+zoneMargin < z.hi {
		return [2]int64{s}, 1
	}
	// Otherwise every offset in force within two days of w is a candidate, and a
	// reading where it holds at its own instant.
	for s, steps := w-zoneMargin, 0; steps < 8; steps++ {
		off := z.offsetAt(s)
		next := z.hi
		if inst := w - off; z.offsetAt(inst) == off && n < len(at) && !slices.Contains(at[:n], inst) {
			at[n] = inst
			n++
		}
		if next >= w+zoneMargin {
			break
		}
		s = next
	}
	if n == 2 && at[1] < at[0] {
		at[0], at[1] = at[1], at[0]
	}
	return at, n
}
