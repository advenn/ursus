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

func TestDivUint64ByZeroIsRefused(t *testing.T) {
	if _, _, ok := i128.One.DivUint64(0); ok {
		t.Error("division by zero was accepted")
	}
}
