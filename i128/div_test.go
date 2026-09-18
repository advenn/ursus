package i128_test

// DivUint64, against math/big.
//
// The oracle is big.Int for the same reason step 61's temporal sweep used one:
// checking 128-bit arithmetic with 64-bit arithmetic agrees with the defect. big.Int
// also fixes the rounding question independently — Quo truncates toward zero, which
// is the behaviour DivUint64 promises.

import (
	"math"
	"math/big"
	"testing"

	"github.com/advenn/ursus/i128"
)

// values spans the sign boundary, the word boundary and both extremes.
func values() []i128.Int128 {
	out := []i128.Int128{
		i128.Zero, i128.One, i128.Min, i128.Max,
		{Hi: 0, Lo: 1},
		{Hi: 0, Lo: math.MaxUint64}, // 2^64 - 1: the word boundary from below
		{Hi: 1, Lo: 0},              // 2^64: and from above
		{Hi: 1, Lo: 1},
		{Hi: 0, Lo: 1 << 53}, // float64's exactness boundary
		{Hi: 0, Lo: (1 << 53) + 1},
		{Hi: 0, Lo: math.MaxInt64},
		{Hi: 0, Lo: uint64(math.MaxInt64) + 1}, // int64 max + 1
		{Hi: 1 << 62, Lo: 12345},
	}
	// Every value's negation too, which is where the magnitude handling lives.
	for _, v := range append([]i128.Int128(nil), out...) {
		out = append(out, v.Neg())
	}
	return out
}

var divisors = []uint64{1, 2, 3, 7, 10, 1 << 32, math.MaxInt64,
	uint64(math.MaxInt64) + 1, math.MaxUint64}

func big128(v i128.Int128) *big.Int {
	// Reconstructed from the words rather than parsed from String(), so the oracle
	// does not depend on the formatting this package also owns.
	hi := big.NewInt(v.Hi)
	hi.Lsh(hi, 64)
	return hi.Or(hi, new(big.Int).SetUint64(v.Lo))
}

func TestDivUint64MatchesBigInt(t *testing.T) {
	var checked int
	for _, v := range values() {
		for _, d := range divisors {
			q, r, ok := v.DivUint64(d)
			if !ok {
				t.Fatalf("%s / %d refused", v, d)
			}

			wantQ, wantR := new(big.Int).QuoRem(big128(v), new(big.Int).SetUint64(d), new(big.Int))
			if got := big128(q); got.Cmp(wantQ) != 0 {
				t.Errorf("%s / %d = %s, want %s", v, d, got, wantQ)
			}
			if got := new(big.Int).SetUint64(r); got.Cmp(wantR.Abs(wantR)) != 0 {
				t.Errorf("%s %% %d = %d, want %s (magnitude)", v, d, r, wantR)
			}
			checked++
		}
	}
	if checked < 200 {
		t.Errorf("only %d divisions checked", checked)
	}
}

// TestDivUint64NeverPanics is the structural half: the two-step division exists
// because bits.Div64 panics when its high word reaches the divisor. A single call
// would panic for every value at or above d*2^64.
func TestDivUint64NeverPanics(t *testing.T) {
	for _, v := range values() {
		for _, d := range divisors {
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("%s / %d panicked: %v", v, d, p)
					}
				}()
				v.DivUint64(d)
			}()
		}
	}
}

// TestDivUint64AtInt128Min pins the corner the magnitude handling exists for. Min's
// Neg is Min, so the code cannot take |Min| by negating into a signed value — it
// reads the same bit pattern as unsigned, where it is exactly 2^127.
func TestDivUint64AtInt128Min(t *testing.T) {
	for _, c := range []struct {
		d    uint64
		want string
	}{
		{1, "-170141183460469231731687303715884105728"},
		{2, "-85070591730234615865843651857942052864"},
		{3, "-56713727820156410577229101238628035242"},
	} {
		q, _, ok := i128.Min.DivUint64(c.d)
		if !ok || q.String() != c.want {
			t.Errorf("Min / %d = %s (ok=%v), want %s", c.d, q, ok, c.want)
		}
	}
}

// TestDivUint64TruncatesTowardZero states the rounding as a fact rather than letting
// it be whatever the implementation happens to do. A floor would answer -2 here.
func TestDivUint64TruncatesTowardZero(t *testing.T) {
	for _, c := range []struct {
		v    i128.Int128
		d    uint64
		want int64
		rem  uint64
	}{
		{i128.FromInt64(-3), 2, -1, 1},
		{i128.FromInt64(3), 2, 1, 1},
		{i128.FromInt64(-1), 2, 0, 1},
		{i128.FromInt64(-7), 7, -1, 0},
	} {
		q, r, ok := c.v.DivUint64(c.d)
		got, fits := q.Int64()
		if !ok || !fits || got != c.want || r != c.rem {
			t.Errorf("%s / %d = %v rem %d, want %d rem %d", c.v, c.d, q, r, c.want, c.rem)
		}
	}
}

func TestDivUint64ByZeroIsRefused(t *testing.T) {
	if _, _, ok := i128.One.DivUint64(0); ok {
		t.Error("division by zero was accepted")
	}
}
