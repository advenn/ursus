package i128_test

import (
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/i128"
)

var (
	minBig = new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 127))
	maxBig = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 127), big.NewInt(1))
)

func inRange(b *big.Int) bool { return b.Cmp(minBig) >= 0 && b.Cmp(maxBig) <= 0 }

// operands is a fixed set of edge values and random values across every magnitude,
// so each check below sees small, mid-width, near-limit and both extremes.
func operands() []i128.Int128 {
	vs := []i128.Int128{
		i128.Zero, i128.One, i128.One.Neg(), i128.Min, i128.Max,
		i128.Min.Add(i128.One), i128.Max.Sub(i128.One),
		i128.FromInt64(2), i128.FromInt64(-2), i128.FromInt64(7), i128.FromInt64(-7),
		i128.FromUint64(1 << 63), i128.FromUint64(1<<64 - 1),
		{Hi: 1, Lo: 0}, {Hi: -1, Lo: 0}, {Hi: 1, Lo: 1<<64 - 1},
	}
	r := rand.New(rand.NewPCG(99, 100))
	for range 300 {
		// A random width from 1 to 127 bits, and a random sign.
		w := 1 + r.IntN(127)
		v := new(big.Int).SetUint64(r.Uint64())
		v.Lsh(v, 64).Or(v, new(big.Int).SetUint64(r.Uint64()))
		v.Rsh(v, uint(128-w))
		if r.IntN(2) == 0 {
			v.Neg(v)
		}
		x, ok := i128.Parse(v.String())
		if !ok {
			panic(v.String())
		}
		vs = append(vs, x)
	}
	return vs
}

// TestCheckedArithmeticAgainstBig: AddChecked, SubChecked and MulChecked are the
// exact result, or false exactly when it leaves the type.
func TestCheckedArithmeticAgainstBig(t *testing.T) {
	vs := operands()
	ops := []struct {
		name string
		f    func(a, b i128.Int128) (i128.Int128, bool)
		big  func(z, a, b *big.Int) *big.Int
	}{
		{"+", i128.Int128.AddChecked, (*big.Int).Add},
		{"-", i128.Int128.SubChecked, (*big.Int).Sub},
		{"*", i128.Int128.MulChecked, (*big.Int).Mul},
	}
	for _, op := range ops {
		bad := 0
		for _, a := range vs {
			for _, b := range vs {
				want := op.big(new(big.Int), toBig(t, a), toBig(t, b))
				got, ok := op.f(a, b)
				switch {
				case ok != inRange(want):
					t.Errorf("%s %s %s: ok=%v, but the exact result %s is in range: %v", a, op.name, b, ok, want, inRange(want))
				case ok && toBig(t, got).Cmp(want) != 0:
					t.Errorf("%s %s %s = %s, want %s", a, op.name, b, got, want)
				default:
					continue
				}
				if bad++; bad > 10 {
					t.Fatalf("too many failures for %s", op.name)
				}
			}
		}
	}
}

// TestFloorDivModAgainstBig: a // b rounds toward negative infinity, a % b takes the
// divisor's sign, a == b*(a//b) + a%b, and only Min // -1 leaves the type.
func TestFloorDivModAgainstBig(t *testing.T) {
	vs := operands()
	bad := 0
	for _, a := range vs {
		for _, b := range vs {
			if b.IsZero() {
				continue
			}
			ab, bb := toBig(t, a), toBig(t, b)
			// big's Div and Mod are Euclidean; floor division differs for a
			// negative divisor, so it is built from truncation.
			q, m := new(big.Int).QuoRem(ab, bb, new(big.Int))
			if m.Sign() != 0 && (m.Sign() < 0) != (bb.Sign() < 0) {
				q.Sub(q, big.NewInt(1))
				m.Add(m, bb)
			}
			gq, gm, ok := a.DivMod(b)
			switch {
			case ok != inRange(q):
				t.Errorf("%s // %s: ok=%v, but the exact quotient %s is in range: %v", a, b, ok, q, inRange(q))
			case toBig(t, gm).Cmp(m) != 0:
				t.Errorf("%s %% %s = %s, want %s", a, b, gm, m)
			case ok && toBig(t, gq).Cmp(q) != 0:
				t.Errorf("%s // %s = %s, want %s", a, b, gq, q)
			default:
				continue
			}
			if bad++; bad > 10 {
				t.Fatal("too many failures")
			}
		}
	}
}

// TestDivModByZeroPanics: the caller checks for zero, as Go's own / does.
func TestDivModByZeroPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("DivMod by zero returned")
		}
	}()
	i128.One.DivMod(i128.Zero)
}
