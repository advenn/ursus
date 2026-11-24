package kernel_test

// A Datetime's calendar parts are its proleptic Gregorian date's, or refused where
// ursus has no calendar (step 543). qcheck's DuckDB run found two silent wrong
// answers there:
//
//   - Dt().Date() converted the day count to an int32 unchecked, so a Datetime(s) of
//     -281474976710655, about 8.9 million years before the epoch, came back as
//     another date. A day past int32 is now refused, the maintainer's choice.
//   - Go's time package computes a date only from about the year -292,277,022,399,
//     so the Month of a Datetime(s) near int64's minimum was another instant's.
//     An instant whose year is past Int32, which only a Datetime(s) reaches, is
//     now refused by every calendar part.
//
// The oracle is Hinnant's civil_from_days over math/big, which shares no code with
// time.Time: each part of each instant, one row at a time, answers the oracle's
// value or is refused with ErrValue exactly where the oracle says it falls outside.

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// civil is the oracle: the proleptic Gregorian year, month, day, ISO weekday and
// ordinal day of day z since 1970-01-01 (Hinnant's civil_from_days).
func civil(z *big.Int) (y *big.Int, m, d, wd, yday int64) {
	n := func(v int64) *big.Int { return big.NewInt(v) }
	fdiv := func(a, b *big.Int) *big.Int { // floored
		q, r := new(big.Int).QuoRem(a, b, new(big.Int))
		if r.Sign() < 0 {
			q.Sub(q, n(1))
		}
		return q
	}
	zz := new(big.Int).Add(z, n(719468))
	era := fdiv(zz, n(146097))
	doe := new(big.Int).Sub(zz, new(big.Int).Mul(era, n(146097))).Int64()
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d = doy - (153*mp+2)/5 + 1
	m = mp + 3
	if mp >= 10 {
		m = mp - 9
	}
	y = new(big.Int).Add(n(yoe), new(big.Int).Mul(era, n(400)))
	if m <= 2 {
		y.Add(y, n(1))
	}
	// ISO weekday: 1970-01-01 was a Thursday, 4.
	wdz := new(big.Int).Mod(new(big.Int).Add(z, n(3)), n(7)).Int64()
	wd = wdz + 1
	// Ordinal day: days since January 1 of the same year, plus one.
	leap := func(yr *big.Int) bool {
		div := func(k int64) bool { return new(big.Int).Mod(yr, n(k)).Sign() == 0 }
		return div(4) && (!div(100) || div(400))
	}
	cum := []int64{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	yday = cum[m-1] + d
	if m > 2 && leap(y) {
		yday++
	}
	return y, m, d, wd, yday
}

func TestCalendarPartsOrRefused(t *testing.T) {
	const perDay = 86_400
	maxDays := int64(1e12) // the kernel's calendarDays
	instants := []int64{math.MinInt64, math.MinInt64 + 1, -9223372036854775807 + 86_400*366*300,
		-maxDays*perDay - 1, -maxDays * perDay, -784_000_000_000 * perDay, -281474976710655,
		-(int64(math.MaxInt32)+1)*perDay - 1, -(int64(math.MaxInt32) + 1) * perDay, -62135596800, -1, 0, 1,
		1_700_000_000, 253402300799, int64(math.MaxInt32) * perDay, (int64(math.MaxInt32) + 1) * perDay,
		784_000_000_000 * perDay, maxDays * perDay, maxDays*perDay + 1, math.MaxInt64}
	parts := []struct {
		fn  expr.CallFn
		get func(y *big.Int, m, d, wd, yday int64) int64
	}{
		{expr.FnDtYear, func(y *big.Int, _, _, _, _ int64) int64 { return y.Int64() }},
		{expr.FnDtMonth, func(_ *big.Int, m, _, _, _ int64) int64 { return m }},
		{expr.FnDtDay, func(_ *big.Int, _, d, _, _ int64) int64 { return d }},
		{expr.FnDtWeekday, func(_ *big.Int, _, _, wd, _ int64) int64 { return wd }},
		{expr.FnDtOrdinalDay, func(_ *big.Int, _, _, _, yday int64) int64 { return yday }},
	}
	dt := dtype.Datetime(dtype.Second, "")
	refusals, answers := 0, 0
	for _, s := range instants {
		col := data.NewFixed("c", dt, []int64{s}, bitmap.AllSet(1))
		q := s / perDay // the floored day
		if s%perDay < 0 {
			q--
		}
		z := big.NewInt(q)
		y, m, d, wd, yday := civil(z)
		inCalendar := z.CmpAbs(big.NewInt(maxDays)) <= 0 && y.IsInt64() &&
			y.Int64() >= math.MinInt32 && y.Int64() <= math.MaxInt32
		for _, p := range parts {
			got, err := kernel.DtCall(p.fn, "c", dtype.Int32, col, nil)
			switch {
			case !inCalendar:
				if !errors.Is(err, uerr.ErrValue) {
					t.Errorf("%v of %d (year %v): %v, want an ErrValue refusal", p.fn, s, y, err)
				}
				refusals++
			case err != nil:
				t.Errorf("%v of %d (year %v): refused: %v", p.fn, s, y, err)
			default:
				v, _ := data.Values[int32](got)
				if want := p.get(y, m, d, wd, yday); int64(v[0]) != want {
					t.Errorf("%v of %d: %d, want %d", p.fn, s, v[0], want)
				}
				answers++
			}
		}
		// Date: the day itself, refused past int32 (and past the calendar).
		got, err := kernel.DtCall(expr.FnDtDate, "c", dtype.Date, col, nil)
		fits := inCalendar && z.IsInt64() && z.Int64() >= math.MinInt32 && z.Int64() <= math.MaxInt32
		switch {
		case !fits:
			if !errors.Is(err, uerr.ErrValue) {
				t.Errorf("Date of %d (day %v): %v, want an ErrValue refusal", s, z, err)
			}
		case err != nil:
			t.Errorf("Date of %d: refused: %v", s, err)
		default:
			v, _ := data.Values[int32](got)
			if int64(v[0]) != z.Int64() {
				t.Errorf("Date of %d: day %d, want %v", s, v[0], z)
			}
		}
	}
	if refusals == 0 || answers == 0 {
		t.Fatalf("%d refusals and %d answers: the instants miss an edge", refusals, answers)
	}
}

// TestDateOfMillisecondsPastInt32 holds a Datetime(ms) to the Date refusal too: its
// years fit Int32 everywhere, but its days pass int32 about 5.9 million years out,
// where qcheck found 288230376151711743 ms wrapped.
func TestDateOfMillisecondsPastInt32(t *testing.T) {
	dt := dtype.Datetime(dtype.Milli, "")
	const perDay = 86_400_000
	for _, ms := range []int64{288230376151711743, int64(math.MaxInt32) * perDay, (int64(math.MaxInt32) + 1) * perDay,
		int64(math.MinInt32) * perDay, int64(math.MinInt32)*perDay - 1, -1, 0} {
		col := data.NewFixed("c", dt, []int64{ms}, bitmap.AllSet(1))
		got, err := kernel.DtCall(expr.FnDtDate, "c", dtype.Date, col, nil)
		z := ms / perDay
		if ms < 0 && ms%perDay != 0 {
			z--
		}
		if z < math.MinInt32 || z > math.MaxInt32 {
			if !errors.Is(err, uerr.ErrValue) {
				t.Errorf("Date of %d ms (day %d): %v, want an ErrValue refusal", ms, z, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Date of %d ms: refused: %v", ms, err)
		}
		if v, _ := data.Values[int32](got); int64(v[0]) != z {
			t.Errorf("Date of %d ms: day %d, want %d", ms, v[0], z)
		}
	}
}
