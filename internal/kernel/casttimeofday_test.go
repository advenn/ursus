package kernel_test

// A Datetime casts to a Time as its time of day, for every pair of units and every
// instant int64 holds (step 542). The cast widened the whole instant to the Time's
// unit before it folded it into its day, so an instant whose ticks overflowed the
// finer unit was refused under a strict cast and null under a lossy one, though its
// time of day exists: qcheck's self check found a Datetime(ms) of 18014398509481983
// refused as a Time(us).
//
// The instants are int64's ends, each widening's limit and one past it, and random
// ones. The oracle is math/big, which shares no code with the kernel: the instant's
// nanoseconds folded into a day, as the UTC wall clock, then floored to the Time's
// unit.

import (
	"math"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// timeOfDay is the oracle: instant v of nanosPerTick, as a Time of toNanos.
func timeOfDay(v, nanosPerTick, toNanos int64) int64 {
	day := big.NewInt(int64(24 * 60 * 60 * 1e9))
	ns := new(big.Int).Mul(big.NewInt(v), big.NewInt(nanosPerTick))
	tod := new(big.Int).Mod(ns, day) // Euclidean: in [0, day)
	return tod.Quo(tod, big.NewInt(toNanos)).Int64()
}

func TestDatetimeToTimeIsTheTimeOfDay(t *testing.T) {
	units := []dtype.TimeUnit{dtype.Second, dtype.Milli, dtype.Micro, dtype.Nano}
	nanos := func(u dtype.TimeUnit) int64 {
		npt, _ := dtype.Time(u).NanosPerTick()
		return npt
	}
	vals := []int64{math.MinInt64, math.MinInt64 + 1, -1, 0, 1, math.MaxInt64 - 1, math.MaxInt64}
	for _, f := range []int64{1e3, 1e6, 1e9} {
		limit := math.MaxInt64 / f
		vals = append(vals, limit, limit+1, -limit, -limit-1)
	}
	r := rand.New(rand.NewPCG(542, 542))
	for range 200 {
		vals = append(vals, int64(r.Uint64()))
	}
	checked := 0
	for _, zone := range []string{"", "Asia/Kolkata"} {
		for _, from := range units {
			col := data.NewFixed("c", dtype.Datetime(from, zone), vals, bitmap.AllSet(len(vals)))
			for _, to := range units {
				for _, strict := range []bool{true, false} {
					got, err := kernel.Cast("c", dtype.Time(to), strict, col)
					if err != nil {
						t.Errorf("%s -> %s, strict %v: %v", col.DType(), dtype.Time(to), strict, err)
						continue
					}
					gv, err := data.Values[int64](got)
					if err != nil {
						t.Fatal(err)
					}
					for i, v := range vals {
						want := timeOfDay(v, nanos(from), nanos(to))
						if !got.IsValid(i) || gv[i] != want {
							t.Errorf("%s -> %s, strict %v: %d is %v (valid %v), want %d",
								col.DType(), dtype.Time(to), strict, v, gv[i], got.IsValid(i), want)
						}
						checked++
					}
				}
			}
		}
	}
	t.Logf("%d instants checked", checked)
}
