package dtype_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
)

// TestAppendISOIsTheTimeLayouts: AppendISO, the CSV writer's default text for a Date,
// a naive Datetime and a Time since step 390, writes what Go's time package writes
// for the layouts it replaces, for every unit, either side of the epoch, about the
// leap days of 1900, 2000 and 2024 and at the ends of the years 0 and 9999; and
// declines a year past them, a Time outside a day and a type with no layout.
func TestAppendISOIsTheTimeLayouts(t *testing.T) {
	frac := map[dtype.TimeUnit]string{dtype.Second: "", dtype.Milli: ".000", dtype.Micro: ".000000", dtype.Nano: ".000000000"}
	perSec := map[dtype.TimeUnit]int64{dtype.Second: 1, dtype.Milli: 1e3, dtype.Micro: 1e6, dtype.Nano: 1e9}
	check := func(d dtype.DataType, layout string, ticks int64) {
		t.Helper()
		got, ok := dtype.AppendISO([]byte("x"), d, ticks)
		tm, tok := d.ToTime(ticks)
		inRange := tok && tm.Year() >= 0 && tm.Year() <= 9999
		if d.ID() == dtype.TypeTime {
			inRange = tok && ticks >= 0 && ticks < 86_400*perSec[d.TimeUnit()]
		}
		if ok != inRange {
			t.Fatalf("%s %d: ok %v, want %v (%v)", d, ticks, ok, inRange, tm)
		}
		if !ok {
			if string(got) != "x" {
				t.Fatalf("%s %d: declined, and appended %q", d, ticks, got)
			}
			return
		}
		if want := string(tm.AppendFormat([]byte("x"), layout)); string(got) != want {
			t.Fatalf("%s %d: %q, want %q", d, ticks, got, want)
		}
	}
	day := int64(86_400)
	// Days of interest: the epoch, the leap days, the ends of the range and past them.
	days := []int64{0, -1, 1, -719_528, -719_529, 2_932_896, 2_932_897, -25_508, -25_509, -25_567,
		10_956, 10_957, 11_016, 11_017, 19_782, 19_783, 19_812, 19_813}
	for _, n := range days {
		check(dtype.Date, "2006-01-02", n)
	}
	r := rand.New(rand.NewPCG(390, 1))
	for range 200_000 {
		check(dtype.Date, "2006-01-02", r.Int64N(2*3_000_000)-3_000_000)
	}
	for u, f := range frac {
		dt, tod := dtype.Datetime(u, ""), dtype.Time(u)
		ps := perSec[u]
		for _, n := range days {
			for _, s := range []int64{0, 1, day - 1} {
				for _, sub := range []int64{0, 1, ps - 1} {
					check(dt, "2006-01-02T15:04:05"+f, (n*day+s)*ps+sub)
				}
			}
		}
		lim := int64(math.MaxInt64)
		if u != dtype.Nano {
			lim = 3_000_000 * day * ps // past the year 9999 either way
		}
		for range 200_000 {
			check(dt, "2006-01-02T15:04:05"+f, r.Int64N(lim)-r.Int64N(lim))
			check(tod, "15:04:05"+f, r.Int64N(day*ps))
		}
		for _, ticks := range []int64{0, 1, day*ps - 1, day * ps, -1, math.MinInt64, math.MaxInt64} {
			check(dt, "2006-01-02T15:04:05"+f, ticks)
			check(tod, "15:04:05"+f, ticks)
		}
	}
	for _, d := range []dtype.DataType{dtype.Int64, dtype.Duration(dtype.Micro), dtype.String} {
		if got, ok := dtype.AppendISO(nil, d, 5); ok || len(got) != 0 {
			t.Fatalf("%s: %q, %v; want it declined", d, got, ok)
		}
	}
}
