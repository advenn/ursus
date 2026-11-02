package dtype

import (
	"math/big"
	"testing"
)

// Step 433: two guards of the type package, recorded silent.

// TestEveryPowerOfTenIsTheTruncatedOne: Eisel-Lemire is built for a table of 10^q
// rounded down to 128 bits, and its checks absorb an error of one in the last bit, so
// no input among 1.2 million told a table rounded up from it (step 334). Each of the
// 696 entries is checked by multiplying back: its top bit set, and for q at or past 0
// the 128 leading bits of 10^q; below 0, m·10^−q ≤ 2^k < (m+1)·10^−q. And two entries
// are Go's strconv's.
func TestEveryPowerOfTenIsTheTruncatedOne(t *testing.T) {
	ten := big.NewInt(10)
	for i, e := range powersOfTen {
		q := i + minPow10
		m := new(big.Int).Lsh(new(big.Int).SetUint64(e[1]), 64)
		m.Or(m, new(big.Int).SetUint64(e[0]))
		if m.BitLen() != 128 {
			t.Fatalf("10^%d: %d bits, want the top one of 128 set", q, m.BitLen())
		}
		if q >= 0 {
			p := new(big.Int).Exp(ten, big.NewInt(int64(q)), nil)
			if n := p.BitLen(); n > 128 {
				lo, hi := new(big.Int).Lsh(m, uint(n-128)), new(big.Int).Lsh(new(big.Int).Add(m, big.NewInt(1)), uint(n-128))
				if lo.Cmp(p) > 0 || hi.Cmp(p) <= 0 {
					t.Fatalf("10^%d: not its leading 128 bits, rounded down", q)
				}
			} else if new(big.Int).Lsh(p, uint(128-n)).Cmp(m) != 0 {
				t.Fatalf("10^%d: not exact", q)
			}
			continue
		}
		d := new(big.Int).Exp(ten, big.NewInt(int64(-q)), nil)
		k := new(big.Int).Lsh(big.NewInt(1), uint(127+d.BitLen()))
		lo, hi := new(big.Int).Mul(m, d), new(big.Int).Mul(new(big.Int).Add(m, big.NewInt(1)), d)
		if lo.Cmp(k) > 0 || hi.Cmp(k) <= 0 {
			t.Fatalf("10^%d: not 2^k / 10^%d rounded down", q, -q)
		}
	}
	for q, want := range map[int][2]uint64{
		-348: {0x1732C869CD60E453, 0xFA8FD5A0081C0288},
		0:    {0, 0x8000000000000000},
	} {
		if got := powersOfTen[q-minPow10]; got != want {
			t.Errorf("10^%d is %#x, strconv's %#x", q, got, want)
		}
	}
}

// TestCategoricalIsTextUnderneath: a Categorical is stored as text (step 351), and
// says so, though nothing routes one by Physical(): every storage question is
// HasStringStorage's.
func TestCategoricalIsTextUnderneath(t *testing.T) {
	if Categorical.Physical() != String || !Categorical.HasStringStorage() {
		t.Errorf("Categorical: physical %s, string storage %v", Categorical.Physical(), Categorical.HasStringStorage())
	}
}
