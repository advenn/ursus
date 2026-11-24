package dtype_test

// A Datetime's text names its own instant at every tick (step 545). Go's calendar
// computes a date only from about the year -292,277,022,399, so a Datetime(s) near
// int64's minimum was written as another instant: qcheck's DuckDB run found
// Cast(String) of -9223372036854775807 seconds not starting with "-". Past 10^12
// days the text is written from civil_from_days, in Go's layout.
//
// The oracle is Hinnant's civil_from_days over math/big and fmt's formatting, which
// share no code with the text's: each instant, naive and in UTC, on both sides of
// the 10^12-day bound and at int64's ends, is the oracle's text.

import (
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/advenn/ursus/dtype"
)

// wantText is the oracle's text of s seconds since the epoch, naive, or with "Z".
func wantText(s int64, zoned bool) string {
	n := func(v int64) *big.Int { return big.NewInt(v) }
	day := n(86_400)
	days, tod := new(big.Int).DivMod(n(s), day, new(big.Int)) // Euclidean: tod in [0, day)
	z := new(big.Int).Add(days, n(719_468))
	era, doeB := new(big.Int).DivMod(z, n(146_097), new(big.Int))
	doe := doeB.Int64()
	yoe := (doe - doe/1460 + doe/36_524 - doe/146_096) / 365
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if m > 12 {
		m -= 12
	}
	y := new(big.Int).Add(n(yoe), new(big.Int).Mul(era, n(400)))
	if m <= 2 {
		y.Add(y, n(1))
	}
	sign := ""
	if y.Sign() < 0 {
		sign = "-"
		y.Neg(y)
	}
	ys := y.String()
	for len(ys) < 4 {
		ys = "0" + ys
	}
	t := tod.Int64()
	text := fmt.Sprintf("%s%s-%02d-%02dT%02d:%02d:%02d", sign, ys, m, d, t/3600, t/60%60, t%60)
	if zoned {
		text += "Z"
	}
	return text
}

func TestFarDatetimeText(t *testing.T) {
	const perDay = 86_400
	bound := int64(1e12) * perDay
	// ±bound are the last instants Go's calendar writes, the rest past it.
	for _, s := range []int64{math.MinInt64, math.MinInt64 + 1, -9223372036854775807, -bound - 1, -bound,
		-bound - perDay, bound, bound + perDay, bound + perDay + 45_296, math.MaxInt64 - 1, math.MaxInt64} {
		for _, zoned := range []bool{false, true} {
			zone := ""
			if zoned {
				zone = "UTC"
			}
			got := dtype.FormatTemporal(dtype.Datetime(dtype.Second, zone), s)
			if want := wantText(s, zoned); got != want {
				t.Errorf("%d seconds, zone %q: %q, want %q", s, zone, got, want)
			}
		}
	}
}
