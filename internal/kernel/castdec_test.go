package kernel_test

// Every cast into or out of a Decimal is exact or refused, against math/big.
//
// A Decimal is stored as its UNSCALED integer, so the characteristic failure of a
// Decimal cast is not a crash but a number off by 10^scale — 12.34 as 1234 — that
// looks entirely plausible. Agreement between CanCast and the kernel cannot see
// that; only values can. So every value here is compared as an exact rational.
//
// The grid covers precision 1 and 38, scale 0 and scale == precision, and both
// sides of 2^53 and 10^19 (where scaleDown switches to two divisions). Each is
// crossed with every integer type, in both directions, and with every other
// Decimal in the grid.

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

var decGrid = []dtype.DataType{
	dtype.Decimal(1, 0), dtype.Decimal(1, 1), dtype.Decimal(5, 2),
	dtype.Decimal(10, 2), dtype.Decimal(18, 3), dtype.Decimal(19, 0),
	dtype.Decimal(20, 1), dtype.Decimal(38, 0), dtype.Decimal(38, 10),
	dtype.Decimal(38, 38),
}

func pow10Big(k int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(k)), nil)
}

// representable reports whether r is exactly a value of type dt.
func representable(r *big.Rat, dt dtype.DataType) bool {
	if dt.IsInteger() {
		return r.IsInt() && inRange(r.Num(), dt)
	}
	x := new(big.Rat).Mul(r, new(big.Rat).SetInt(pow10Big(int(dt.Scale()))))
	if !x.IsInt() {
		return false
	}
	return new(big.Int).Abs(x.Num()).Cmp(pow10Big(int(dt.Precision()))) < 0
}

// sourceValues is the exact values a column of type dt holds in this sweep: every
// integer type's edges, the powers of ten and their neighbours that bound each
// Decimal, and — for a Decimal — its own extremes and a fractional value.
func sourceValues(dt dtype.DataType, ints []dtype.DataType) []*big.Rat {
	var out []*big.Rat
	seen := map[string]bool{}
	add := func(r *big.Rat) {
		if representable(r, dt) && !seen[r.RatString()] {
			seen[r.RatString()] = true
			out = append(out, r)
		}
	}
	one := big.NewInt(1)
	for _, v := range edges(ints) {
		add(new(big.Rat).SetInt(v))
	}
	for k := range 40 {
		p := pow10Big(k)
		for _, v := range []*big.Int{p, new(big.Int).Sub(p, one)} {
			add(new(big.Rat).SetInt(v))
			add(new(big.Rat).SetInt(new(big.Int).Neg(v)))
		}
	}
	if dt.ID() == dtype.TypeDecimal {
		p, s := int(dt.Precision()), int(dt.Scale())
		ten := pow10Big(s)
		for _, u := range []*big.Int{
			new(big.Int).Sub(pow10Big(p), one), // the largest value, all nines
			big.NewInt(1),                      // the smallest step
			big.NewInt(1234),                   // 12.34 at scale 2
			new(big.Int).Mul(ten, big.NewInt(7)),
		} {
			add(new(big.Rat).SetFrac(u, ten))
			add(new(big.Rat).SetFrac(new(big.Int).Neg(u), ten))
		}
		// A one in every digit position. Rescaling divides by 10^k, twice when k
		// passes 19, and a value is exact only if BOTH divisions are: 3e-19 at scale
		// 38 survives a single division by 10^19 and not the second. Without these,
		// every value in the grid was either divisible by nothing or by everything.
		for j := range p {
			add(new(big.Rat).SetFrac(pow10Big(j), ten))
			add(new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(-3), pow10Big(j)), ten))
		}
	}
	return out
}

// columnOf builds a column of type dt holding vals, each of which it can represent.
func columnOf(t *testing.T, dt dtype.DataType, vals []*big.Rat) *data.Column {
	t.Helper()
	if dt.ID() != dtype.TypeDecimal {
		ints := make([]*big.Int, len(vals))
		for i, r := range vals {
			ints[i] = r.Num()
		}
		return intColumn(t, dt, ints)
	}
	scale := new(big.Rat).SetInt(pow10Big(int(dt.Scale())))
	u := make([]i128.Int128, len(vals))
	for i, r := range vals {
		x := new(big.Rat).Mul(r, scale)
		v, ok := i128.Parse(x.Num().String())
		if !ok {
			t.Fatalf("i128.Parse(%s)", x.Num())
		}
		u[i] = v
	}
	return data.NewFixed("c", dt, u, bitmap.AllSet(len(vals)))
}

// ratsOf reads an integer or Decimal column back as exact rationals, nil for null.
func ratsOf(t *testing.T, c *data.Column) []*big.Rat {
	t.Helper()
	ints := intValues(t, c) // a Decimal's physical type is Int128: its unscaled digits
	var scale *big.Int
	if c.DType().ID() == dtype.TypeDecimal {
		scale = pow10Big(int(c.DType().Scale()))
	}
	out := make([]*big.Rat, len(ints))
	for i, v := range ints {
		switch {
		case v == nil:
		case scale != nil:
			out[i] = new(big.Rat).SetFrac(v, scale)
		default:
			out[i] = new(big.Rat).SetInt(v)
		}
	}
	return out
}

func ratString(r *big.Rat) string {
	if r == nil {
		return "null"
	}
	return r.RatString()
}

func TestDecimalCastsAreExactOrRefused(t *testing.T) {
	ints := integerTypes(t)
	types := append(append([]dtype.DataType(nil), decGrid...), ints...)
	var pairs, delivered, nulled int

	for _, from := range types {
		vals := sourceValues(from, ints)
		col := columnOf(t, from, vals)
		for _, to := range types {
			if from.ID() != dtype.TypeDecimal && to.ID() != dtype.TypeDecimal {
				continue // integer -> integer is intcast_test.go's
			}
			pairs++
			name := from.String() + "->" + to.String()

			got, err := kernel.Cast("c", to, false, col)
			if err != nil {
				t.Errorf("%s: CastLossy refused: %v", name, err)
				continue
			}
			if got.DType() != to {
				t.Errorf("%s: result is %s", name, got.DType())
			}
			for i, g := range ratsOf(t, got) {
				want := vals[i]
				if !representable(want, to) {
					want = nil
				}
				if (g == nil) != (want == nil) || (g != nil && g.Cmp(want) != 0) {
					t.Errorf("%s: %s -> %s, want %s", name, vals[i].RatString(),
						ratString(g), ratString(want))
				}
				if g == nil {
					nulled++
				} else {
					delivered++
				}
			}

			// Strict refuses exactly the values that are not representable, with
			// ErrValue.
			for _, v := range vals {
				_, err := kernel.Cast("c", to, true, columnOf(t, from, []*big.Rat{v}))
				switch {
				case representable(v, to) && err != nil:
					t.Errorf("%s: %s refused: %v", name, v.RatString(), err)
				case !representable(v, to) && err == nil:
					t.Errorf("%s: %s converted under a strict cast", name, v.RatString())
				case err != nil && !errors.Is(err, uerr.ErrValue):
					t.Errorf("%s: %s refused with the wrong kind: %v", name, v.RatString(), err)
				}
			}
		}
	}
	// Both outcomes must occur in quantity, or "always null" and "never refuse"
	// would each pass half of this.
	if pairs != 280 || delivered < 2000 || nulled < 2000 {
		t.Fatalf("%d pairs, %d delivered, %d nulled — the sweep has gone vacuous",
			pairs, delivered, nulled)
	}
}

// TestDecimalToFloatIsTheNearestDouble: the oracle is the String cast, which is
// exact, parsed by strconv, which is correctly rounded. So the float a Decimal
// casts to must be the double nearest its value — whichever path decimalToFloat
// took, including past 2^53 and past scale 22, where its fast path stops.
func TestDecimalToFloatIsTheNearestDouble(t *testing.T) {
	var checked int
	for _, dt := range decGrid {
		vals := sourceValues(dt, integerTypes(t))
		col := columnOf(t, dt, vals)
		strs, err := kernel.Cast("s", dtype.String, true, col)
		if err != nil {
			t.Fatal(err)
		}
		f64, err := kernel.Cast("f", dtype.Float64, true, col)
		if err != nil {
			t.Fatalf("%s -> Float64 refused: %v", dt, err)
		}
		got, err := data.Values[float64](f64)
		if err != nil {
			t.Fatal(err)
		}
		for i := range vals {
			s := strs.Strings().Get(i)
			want, err := strconv.ParseFloat(s, 64)
			if err != nil {
				t.Fatal(err)
			}
			if got[i] != want {
				t.Errorf("%s %s -> %v, want %v", dt, s, got[i], want)
			}
			checked++
		}
	}
	if checked < 300 {
		t.Fatalf("only %d values checked", checked)
	}
}

// TestFloatToDecimalIsExactOrRefused: a float converts exactly when rounding it to
// the target's scale changes nothing — Round(v, s) == v, with Round's own
// arithmetic, math.Round(v*10^s)/10^s — and the result is that rounding's digits.
// Past 2^53, where that arithmetic is no longer exact, what is asserted is the
// defining property instead: a value that converts comes back unchanged.
func TestFloatToDecimalIsExactOrRefused(t *testing.T) {
	floats := []float64{
		0, math.Copysign(0, -1), 0.1, 0.123, 2.675, 1.005, 12.34, -0.05, 0.5, -2.5,
		1.0 / 3, 9.999, 99999999.99, 123456.789, 1e15, 1e20, -1e37, 5e-324, 1e-10,
		math.NaN(), math.Inf(1), math.Inf(-1), float64(1<<53 + 2),
	}
	var exact, refused int
	for _, dt := range decGrid {
		s, p := int(dt.Scale()), int(dt.Precision())
		col := data.NewFixed("f", dtype.Float64, floats, bitmap.AllSet(len(floats)))
		out, err := kernel.Cast("d", dt, false, col)
		if err != nil {
			t.Fatalf("CastLossy to %s refused: %v", dt, err)
		}
		got := ratsOf(t, out)
		for i, f := range floats {
			scaled := f * math.Pow10(s)
			switch {
			case math.IsNaN(f) || math.IsInf(f, 0):
				if got[i] != nil {
					t.Errorf("%v -> %s = %s, want null", f, dt, got[i].RatString())
				}
			case math.Abs(scaled) < 1<<53 && s <= 22:
				cand := math.Round(scaled)
				want := cand/math.Pow10(s) == f &&
					new(big.Int).Abs(bigOf(cand)).Cmp(pow10Big(p)) < 0
				if want != (got[i] != nil) {
					t.Errorf("%v -> %s: converted = %v, but Round(v, %d) == v is %v",
						f, dt, got[i] != nil, s, want)
					continue
				}
				if want {
					w := new(big.Rat).SetFrac(bigOf(cand), pow10Big(s))
					if got[i].Cmp(w) != 0 {
						t.Errorf("%v -> %s = %s, want %s", f, dt, got[i].RatString(), w.RatString())
					}
				}
			case got[i] != nil:
				back, _ := got[i].Float64()
				if back != f {
					t.Errorf("%v -> %s = %s, which is %v as a float", f, dt, got[i].RatString(), back)
				}
			}
			if got[i] != nil {
				exact++
			} else {
				refused++
			}
		}
	}
	if exact < 40 || refused < 40 {
		t.Fatalf("%d converted, %d refused — the sweep has gone vacuous", exact, refused)
	}
}

func bigOf(f float64) *big.Int {
	b, _ := new(big.Float).SetFloat64(f).Int(nil)
	return b
}

// TestDecimalCastToAnInvalidDecimalIsRefused: Decimal(200, 3) constructs, because
// a type constructor returns no error, so the cast is where it is refused.
func TestDecimalCastToAnInvalidDecimalIsRefused(t *testing.T) {
	col := data.NewFixed("c", dtype.Int64, []int64{1}, bitmap.AllSet(1))
	for _, to := range []dtype.DataType{
		dtype.Decimal(200, 3), dtype.Decimal(39, 0), dtype.Decimal(0, 0), dtype.Decimal(5, 6),
	} {
		if dtype.CanCast(dtype.Int64, to) {
			t.Errorf("CanCast(Int64, %s) promises a type that cannot exist", to)
		}
		if _, err := kernel.Cast("c", to, false, col); err == nil {
			t.Errorf("the kernel cast Int64 to %s", to)
		}
	}
	// The control: the widest that CAN exist.
	if !dtype.CanCast(dtype.Int64, dtype.Decimal(38, 38)) {
		t.Error("Decimal(38, 38) is a valid type")
	}
}

// TestFloat32ToDecimalIsJudgedAsAFloat32: a Float32 0.1 is 0.100000001490116 as a
// float64, so a round trip compared at 64 bits could never accept it as 0.10. It is
// compared in the source's own precision, where 0.10 comes back as exactly the same
// Float32 — while 0.123, which no two-place decimal is, is still refused.
func TestFloat32ToDecimalIsJudgedAsAFloat32(t *testing.T) {
	col := data.NewFixed("f", dtype.Float32, []float32{0.1, 12.34, 0.123}, bitmap.AllSet(3))
	out, err := kernel.Cast("d", dtype.Decimal(10, 2), false, col)
	if err != nil {
		t.Fatal(err)
	}
	got := ratsOf(t, out)
	for i, want := range []string{"1/10", "617/50", "null"} {
		if ratString(got[i]) != want {
			t.Errorf("row %d = %s, want %s", i, ratString(got[i]), want)
		}
	}
}
