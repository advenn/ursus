package dtype

import (
	"math"
	"math/big"
	"math/bits"
)

// pow10 is the powers of ten a float64 holds exactly.
var pow10 = [...]float64{1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10,
	1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22}

// NearestFloat is the float64 nearest man × 10^exp, negated for neg, and whether it was
// found: Clinger's exact case, man and 10^exp both exact and their product or quotient
// rounded once, else the Eisel-Lemire algorithm. Both round correctly, so a float found
// is strconv.ParseFloat's; false is a case neither settles, a halfway one, a subnormal
// or past the range, which strconv works out. The NDJSON reader and ParseFloat read a
// number's digits into man and ask it.
func NearestFloat(man uint64, exp int, neg bool) (float64, bool) {
	if man == 0 {
		if neg {
			return math.Copysign(0, -1), true
		}
		return 0, true
	}
	if man < 1<<53 && exp >= -22 && exp <= 22 {
		f := float64(man)
		if exp < 0 {
			f /= pow10[-exp]
		} else {
			f *= pow10[exp]
		}
		if neg {
			f = -f
		}
		return f, true
	}
	return eiselLemire(man, exp, neg)
}

const (
	minPow10 = -348 // the powers of ten table's least
	maxPow10 = 347  // and greatest
)

// powersOfTen is 10^q for q from minPow10 to maxPow10, as a 128-bit mantissa with its
// top bit set, rounded down: {low 64 bits, high 64 bits}. It is worked out once, with
// big integers, rather than written out.
var powersOfTen = func() (t [maxPow10 - minPow10 + 1][2]uint64) {
	ten := big.NewInt(10)
	mask := new(big.Int).SetUint64(math.MaxUint64)
	for i := range t {
		q := i + minPow10
		var m *big.Int
		if q >= 0 {
			m = new(big.Int).Exp(ten, big.NewInt(int64(q)), nil)
			if n := m.BitLen(); n > 128 {
				m.Rsh(m, uint(n-128))
			} else {
				m.Lsh(m, uint(128-n))
			}
		} else {
			// floor(2^k / 10^-q), k making it 128 bits: 10^-q is no power of two,
			// so the quotient is under 2^128.
			d := new(big.Int).Exp(ten, big.NewInt(int64(-q)), nil)
			m = new(big.Int).Lsh(big.NewInt(1), uint(127+d.BitLen()))
			m.Quo(m, d)
		}
		t[i][0] = new(big.Int).And(m, mask).Uint64()
		t[i][1] = new(big.Int).Rsh(m, 64).Uint64()
	}
	return t
}()

// eiselLemire is the float64 nearest man × 10^exp, man not 0, by Daniel Lemire's
// algorithm ("Number Parsing at a Gigabyte per Second", 2021), as Go's strconv runs it;
// false when the answer is not certain, which strconv then works out the long way.
func eiselLemire(man uint64, exp int, neg bool) (float64, bool) {
	if exp < minPow10 || exp > maxPow10 {
		return 0, false
	}
	// Normalise man, its top bit set.
	clz := bits.LeadingZeros64(man)
	man <<= uint(clz)
	const bias = 1023
	retExp2 := uint64(217706*exp>>16+64+bias) - uint64(clz)
	// Multiply by the 128-bit power of ten.
	pow := powersOfTen[exp-minPow10]
	xHi, xLo := bits.Mul64(man, pow[1])
	if xHi&0x1FF == 0x1FF && xLo+man < man {
		// The low bits may carry: the wider product decides.
		yHi, yLo := bits.Mul64(man, pow[0])
		mergedHi, mergedLo := xHi, xLo+yHi
		if mergedLo < xLo {
			mergedHi++
		}
		if mergedHi&0x1FF == 0x1FF && mergedLo+1 == 0 && yLo+man < man {
			return 0, false
		}
		xHi, xLo = mergedHi, mergedLo
	}
	// Shift to 54 bits.
	msb := xHi >> 63
	retMan := xHi >> (msb + 9)
	retExp2 -= 1 ^ msb
	// A halfway case is not settled here.
	if xLo == 0 && xHi&0x1FF == 0 && retMan&3 == 1 {
		return 0, false
	}
	// Round to 53 bits.
	retMan += retMan & 1
	retMan >>= 1
	if retMan>>53 > 0 {
		retMan >>= 1
		retExp2++
	}
	// Zero or an underflow is a subnormal, 0x7FF or past it ±Inf or NaN: strconv's.
	if retExp2-1 >= 0x7FF-1 {
		return 0, false
	}
	b := retExp2<<52 | retMan&(1<<52-1)
	if neg {
		b |= 1 << 63
	}
	return math.Float64frombits(b), true
}
