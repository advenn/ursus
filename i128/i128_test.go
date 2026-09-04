package i128_test

import (
	"math"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/i128"
)

// The oracle is math/big, which is a genuinely independent implementation — this
// package's String() is written by repeated division precisely so that the
// comparison is not two wrappers around the same code.
//
// math/big is imported only from the test, so `go list`'s non-test imports stay
// empty and i128 remains an L0 leaf.

var two128 = new(big.Int).Lsh(big.NewInt(1), 128)

// toBig converts an Int128 to a big.Int by way of its own String, which means the
// tests below check String and the arithmetic together. A bug in String that
// happened to cancel out in the round trip would be caught by TestString's
// explicit cases.
func toBig(t *testing.T, v i128.Int128) *big.Int {
	t.Helper()
	b, ok := new(big.Int).SetString(v.String(), 10)
	if !ok {
		t.Fatalf("String produced unparseable output: %q", v.String())
	}
	return b
}

// wrap reduces a big.Int into the signed 128-bit range, so the oracle wraps the
// same way the implementation does.
func wrap(b *big.Int) *big.Int {
	m := new(big.Int).Mod(b, two128)
	if m.Cmp(new(big.Int).Lsh(big.NewInt(1), 127)) >= 0 {
		m.Sub(m, two128)
	}
	return m
}

func corpus() []i128.Int128 {
	vs := []i128.Int128{
		i128.Zero, i128.One, i128.One.Neg(),
		i128.Min, i128.Max,
		i128.FromInt64(math.MaxInt64), i128.FromInt64(math.MinInt64),
		i128.FromUint64(math.MaxUint64),
		{Hi: 0, Lo: 1 << 63}, // 2^63
		{Hi: 1, Lo: 0},       // 2^64
		{Hi: -1, Lo: 0},      // -2^64
		{Hi: -1, Lo: 1},      // -2^64+1
		{Hi: 0, Lo: math.MaxUint64},
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		vs = append(vs, i128.Int128{Hi: int64(rng.Uint64()), Lo: rng.Uint64()})
	}
	return vs
}

func TestString(t *testing.T) {
	cases := []struct {
		v    i128.Int128
		want string
	}{
		{i128.Zero, "0"},
		{i128.One, "1"},
		{i128.One.Neg(), "-1"},
		{i128.FromInt64(math.MaxInt64), "9223372036854775807"},
		{i128.FromInt64(math.MinInt64), "-9223372036854775808"},
		{i128.FromUint64(math.MaxUint64), "18446744073709551615"},
		{i128.Int128{Hi: 1, Lo: 0}, "18446744073709551616"}, // 2^64
		{i128.Max, "170141183460469231731687303715884105727"},
		{i128.Min, "-170141183460469231731687303715884105728"},
	}
	for _, c := range cases {
		if got := c.v.String(); got != c.want {
			t.Errorf("String(%d,%d) = %s, want %s", c.v.Hi, c.v.Lo, got, c.want)
		}
	}
}

func TestAddSubNegAgainstBig(t *testing.T) {
	vs := corpus()
	for _, a := range vs {
		ba := toBig(t, a)

		if got, want := toBig(t, a.Neg()), wrap(new(big.Int).Neg(ba)); got.Cmp(want) != 0 {
			t.Fatalf("Neg(%s) = %s, want %s", a, got, want)
		}

		for _, b := range vs {
			bb := toBig(t, b)

			if got, want := toBig(t, a.Add(b)), wrap(new(big.Int).Add(ba, bb)); got.Cmp(want) != 0 {
				t.Fatalf("%s + %s = %s, want %s", a, b, got, want)
			}
			if got, want := toBig(t, a.Sub(b)), wrap(new(big.Int).Sub(ba, bb)); got.Cmp(want) != 0 {
				t.Fatalf("%s - %s = %s, want %s", a, b, got, want)
			}
			if got, want := a.Cmp(b), ba.Cmp(bb); got != want {
				t.Fatalf("Cmp(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

// TestOrderEncodingMatchesOrder checks the property the group-key encoder and any
// future spill file depend on: big-endian bytes with a flipped sign bit sort in
// value order.
func TestOrderEncodingMatchesOrder(t *testing.T) {
	vs := corpus()
	for _, a := range vs {
		for _, b := range vs {
			ea := string(a.AppendBigEndian(nil))
			eb := string(b.AppendBigEndian(nil))

			var byteCmp int
			switch {
			case ea < eb:
				byteCmp = -1
			case ea > eb:
				byteCmp = 1
			}
			if got := a.Cmp(b); got != byteCmp {
				t.Fatalf("encoding order disagrees for %s vs %s: value %d, bytes %d",
					a, b, got, byteCmp)
			}
		}
	}
}

func TestRoundTrips(t *testing.T) {
	for _, v := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64, 1 << 40, -(1 << 40)} {
		got, ok := i128.FromInt64(v).Int64()
		if !ok || got != v {
			t.Errorf("Int64 round trip failed for %d: %d, %v", v, got, ok)
		}
	}
	for _, v := range []uint64{0, 1, math.MaxUint64, 1 << 63} {
		got, ok := i128.FromUint64(v).Uint64()
		if !ok || got != v {
			t.Errorf("Uint64 round trip failed for %d: %d, %v", v, got, ok)
		}
	}

	// Narrowing must REPORT failure rather than truncate: this is the boundary
	// where an overflow that was unreachable in the accumulator becomes visible.
	if _, ok := i128.Max.Int64(); ok {
		t.Error("Max must not narrow to int64")
	}
	if _, ok := i128.Min.Int64(); ok {
		t.Error("Min must not narrow to int64")
	}
	if _, ok := (i128.Int128{Hi: 1, Lo: 0}).Int64(); ok {
		t.Error("2^64 must not narrow to int64")
	}
	if _, ok := i128.FromInt64(-1).Uint64(); ok {
		t.Error("-1 must not narrow to uint64")
	}
}

func TestParse(t *testing.T) {
	for _, v := range corpus() {
		got, ok := i128.Parse(v.String())
		if !ok {
			t.Fatalf("Parse(%q) failed", v.String())
		}
		if got != v {
			t.Fatalf("Parse round trip: %s -> %s", v, got)
		}
	}
	for _, bad := range []string{"", "-", "+", "abc", "1a", "1.5", " 1"} {
		if _, ok := i128.Parse(bad); ok {
			t.Errorf("Parse(%q) should have failed", bad)
		}
	}
}

func TestFloat64(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 1 << 40, -(1 << 40), math.MaxInt64} {
		if got, want := i128.FromInt64(v).Float64(), float64(v); got != want {
			t.Errorf("Float64(%d) = %v, want %v", v, got, want)
		}
	}
	// Values past 2^53 round, which is expected and must not be an error.
	if got := i128.Max.Float64(); got <= 1.7e38 {
		t.Errorf("Float64(Max) = %v, want ~1.70e38", got)
	}

	for _, f := range []float64{0, 1, -1, 1e18, -1e18, 1e30, -1e30} {
		v, ok := i128.FromFloat64(f)
		if !ok {
			t.Fatalf("FromFloat64(%v) failed", f)
		}
		if back := v.Float64(); math.Abs(back-f) > math.Abs(f)*1e-15 {
			t.Errorf("FromFloat64(%v).Float64() = %v", f, back)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1e40, -1e40} {
		if _, ok := i128.FromFloat64(bad); ok {
			t.Errorf("FromFloat64(%v) should have failed", bad)
		}
	}
}

// TestSumDoesNotOverflow is the reason this package exists: accumulating the
// largest possible int64 values must stay exact well past where an int64
// accumulator would have wrapped.
func TestSumDoesNotOverflow(t *testing.T) {
	const n = 1000
	acc := i128.Zero
	for range n {
		acc = acc.Add(i128.FromInt64(math.MaxInt64))
	}
	want := new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(n))
	if got := toBig(t, acc); got.Cmp(want) != 0 {
		t.Fatalf("sum of %d MaxInt64 = %s, want %s", n, got, want)
	}
	// And the same for unsigned, which is why unsigned sums share this accumulator.
	acc = i128.Zero
	for range n {
		acc = acc.Add(i128.FromUint64(math.MaxUint64))
	}
	want = new(big.Int).Mul(new(big.Int).SetUint64(math.MaxUint64), big.NewInt(n))
	if got := toBig(t, acc); got.Cmp(want) != 0 {
		t.Fatalf("sum of %d MaxUint64 = %s, want %s", n, got, want)
	}
}
