package ursus_test

import (
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// Round, Floor, Ceil and Sign of a Decimal (step 490), as Polars 2.0 answers them.

// decimalText is a Decimal query's type and its first column as text, a null nil.
func decimalText(t *testing.T, q *ursus.LazyFrame) (dtype.DataType, []any) {
	t.Helper()
	s, err := q.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	name := s.Field(0).Name
	return s.Field(0).Type, typedCells[string](t, q.Select(ursus.Col(name).Cast(ursus.String)))
}

func TestDecimalRounding(t *testing.T) {
	d := ursus.Col("d")
	even, away := ursus.RoundHalfToEven, ursus.RoundHalfAwayFromZero
	decimals := func(dt dtype.DataType, vals ...string) *ursus.LazyFrame {
		valid := make([]bool, len(vals))
		for i, v := range vals {
			valid[i] = v != ""
		}
		return ursus.Frame(ursus.ValuesNullable("d", vals, valid)).Select(d.Cast(dt))
	}
	texts := func(vals ...string) []any {
		out := make([]any, len(vals))
		for i, v := range vals {
			if v != "" {
				out[i] = v
			}
		}
		return out
	}

	// Polars 2.1.0 gives these: a result that can carry into a new integer digit,
	// 9.99 to 10.00, is Decimal(p+1, s), at most 38; one where nothing is dropped
	// keeps the type. Its round's default mode is half to even, ursus's half away
	// from zero, so each mode is named here. Polars writes a zero of scale 10 as
	// 0E-10, ursus as 0.0000000000.
	d32 := decimals(dtype.Decimal(3, 2), "1.25", "-1.25", "1.35", "-1.35", "2.50", "-2.50", "0.05", "-0.05",
		"9.99", "-9.99", "0.00", "", "1.24", "1.26")
	d1210 := decimals(dtype.Decimal(12, 10), "0.1234567891", "-0.0000000005", "0.0000000015")
	z10 := "0.0000000000"
	for _, c := range []struct {
		name string
		q    *ursus.LazyFrame
		typ  dtype.DataType
		want []any
	}{
		{"round(0) half to even", d32.Select(d.Round(0, even)), dtype.Decimal(4, 2),
			texts("1.00", "-1.00", "1.00", "-1.00", "2.00", "-2.00", "0.00", "0.00", "10.00", "-10.00", "0.00", "", "1.00", "1.00")},
		{"round(0) half away", d32.Select(d.Round(0, away)), dtype.Decimal(4, 2),
			texts("1.00", "-1.00", "1.00", "-1.00", "3.00", "-3.00", "0.00", "0.00", "10.00", "-10.00", "0.00", "", "1.00", "1.00")},
		{"round(1) half to even", d32.Select(d.Round(1, even)), dtype.Decimal(4, 2),
			texts("1.20", "-1.20", "1.40", "-1.40", "2.50", "-2.50", "0.00", "0.00", "10.00", "-10.00", "0.00", "", "1.20", "1.30")},
		{"round(1), half away by default", d32.Select(d.Round(1)), dtype.Decimal(4, 2),
			texts("1.30", "-1.30", "1.40", "-1.40", "2.50", "-2.50", "0.10", "-0.10", "10.00", "-10.00", "0.00", "", "1.20", "1.30")},
		{"round(3) keeps the type", d32.Select(d.Round(3, even)), dtype.Decimal(3, 2),
			texts("1.25", "-1.25", "1.35", "-1.35", "2.50", "-2.50", "0.05", "-0.05", "9.99", "-9.99", "0.00", "", "1.24", "1.26")},
		{"ceil", d32.Select(d.Ceil()), dtype.Decimal(4, 2),
			texts("2.00", "-1.00", "2.00", "-1.00", "3.00", "-2.00", "1.00", "0.00", "10.00", "-9.00", "0.00", "", "2.00", "2.00")},
		{"floor", d32.Select(d.Floor()), dtype.Decimal(4, 2),
			texts("1.00", "-2.00", "1.00", "-2.00", "2.00", "-3.00", "0.00", "-1.00", "9.00", "-10.00", "0.00", "", "1.00", "1.00")},
		{"sign", d32.Select(d.Sign()), dtype.Decimal(3, 2),
			texts("1.00", "-1.00", "1.00", "-1.00", "1.00", "-1.00", "1.00", "-1.00", "1.00", "-1.00", "0.00", "", "1.00", "1.00")},
		{"sign of Decimal(1, 1)", decimals(dtype.Decimal(1, 1), "0.5", "-0.9", "0.0").Select(d.Sign()), dtype.Decimal(2, 1),
			texts("1.0", "-1.0", "0.0")},
		{"ceil of Decimal(2, 2)", decimals(dtype.Decimal(2, 2), "0.50", "-0.99", "0.01").Select(d.Ceil()), dtype.Decimal(3, 2),
			texts("1.00", "0.00", "1.00")},
		{"Decimal(38, 2) stays 38", decimals(dtype.Decimal(38, 2), "1.50", "-2.50").Select(d.Round(0, even)), dtype.Decimal(38, 2),
			texts("2.00", "-2.00")},
		{"Decimal(37, 1) to 38", decimals(dtype.Decimal(37, 1), "1.5", "-2.5").Select(d.Round(0, even)), dtype.Decimal(38, 1),
			texts("2.0", "-2.0")},
		{"ceil of Decimal(38, 0)", decimals(dtype.Decimal(38, 0), strings.Repeat("9", 38)).Select(d.Ceil()), dtype.Decimal(38, 0),
			texts(strings.Repeat("9", 38))},
		{"sign of Decimal(5, 0)", decimals(dtype.Decimal(5, 0), "5", "-5", "15").Select(d.Sign()), dtype.Decimal(5, 0),
			texts("1", "-1", "1")},
		{"round(9) of Decimal(12, 10)", d1210.Select(d.Round(9, even)), dtype.Decimal(13, 10),
			texts("0.1234567890", z10, "0.0000000020")},
		{"floor of Decimal(12, 10)", d1210.Select(d.Floor()), dtype.Decimal(13, 10),
			texts(z10, "-1.0000000000", z10)},
	} {
		typ, got := decimalText(t, c.q)
		if typ != c.typ || !sameCells(got, c.want) {
			t.Errorf("%s: %s %v, Polars %s %v", c.name, typ, got, c.typ, c.want)
		}
	}

	// Polars 2.1.0 refuses these, "overflow in decimal rounding: result doesn't fit
	// Decimal(38, 2)" and "sign of a nonzero Decimal(38, 38) value doesn't fit its
	// type"; ursus too, naming the row.
	nines := strings.Repeat("9", 36) + ".99"
	half := "0." + strings.Repeat("5", 38)
	for name, q := range map[string]*ursus.LazyFrame{
		"round(0) of 99…9.99":         decimals(dtype.Decimal(38, 2), "1.50", nines).Select(d.Round(0, even)),
		"ceil of 99…9.99":             decimals(dtype.Decimal(38, 2), nines).Select(d.Ceil()),
		"floor of -99…9.99":           decimals(dtype.Decimal(38, 2), "-"+nines).Select(d.Floor()),
		"sign of Decimal(38, 38)":     decimals(dtype.Decimal(38, 38), "0", half).Select(d.Sign()),
		"round(0) of Decimal(38, 38)": decimals(dtype.Decimal(38, 38), half).Select(d.Round(0, even)),
	} {
		if _, err := q.Collect(t.Context()); !errors.Is(err, ursus.ErrValue) {
			t.Errorf("%s: %v, want a value error", name, err)
		}
	}

	// The oracle: big.Rat, over generated values of each type, ties at every place
	// among them, at every place round reaches and each mode.
	rng := rand.New(rand.NewPCG(490, 1))
	checked := 0
	types := []struct{ p, s int }{{1, 0}, {1, 1}, {3, 2}, {5, 2}, {10, 4}, {18, 9}, {19, 0}, {20, 10},
		{37, 1}, {38, 0}, {38, 2}, {38, 19}, {38, 37}, {38, 38}}
	for _, ty := range types {
		dt := dtype.Decimal(uint8(ty.p), uint8(ty.s))
		vals := []string{strings.Repeat("9", ty.p), "-" + strings.Repeat("9", ty.p), "1", "-1", "0"}
		for range 60 {
			digits, n := []byte(fmt.Sprint(rng.IntN(9)+1)), 1+rng.IntN(ty.p)
			for len(digits) < n {
				digits = append(digits, byte('0'+rng.IntN(10)))
			}
			if k := rng.IntN(len(digits) + 1); rng.IntN(2) == 0 && k > 0 {
				digits[len(digits)-k] = '5' // a tie at k places from the end
				for j := len(digits) - k + 1; j < len(digits); j++ {
					digits[j] = '0'
				}
			}
			v := string(digits)
			if rng.IntN(2) == 0 {
				v = "-" + v
			}
			vals = append(vals, v)
		}
		type round struct {
			name   string
			e      ursus.Expr
			places int
			mode   string
		}
		var rounds []round
		for _, pl := range []int{0, 1, ty.s - 1, ty.s, ty.s + 1} {
			if pl >= 0 {
				rounds = append(rounds, round{fmt.Sprintf("round(%d) even", pl), d.Round(pl, even), pl, "even"},
					round{fmt.Sprintf("round(%d) away", pl), d.Round(pl, away), pl, "away"})
			}
		}
		rounds = append(rounds, round{"floor", d.Floor(), 0, "floor"}, round{"ceil", d.Ceil(), 0, "ceil"},
			round{"sign", d.Sign(), 0, "sign"})
		for _, r := range rounds {
			// The type: Decimal(p+1, s), at most 38, where digits are dropped or a sign
			// needs an integer digit the type has not; else the operand's.
			want := dt
			if r.mode == "sign" && ty.p == ty.s || r.mode != "sign" && r.places < ty.s {
				want = dtype.Decimal(uint8(min(38, ty.p+1)), uint8(ty.s))
			}
			limit := pow10(int(want.Precision()))
			var keep, wants []string
			for _, v := range vals {
				u := decimalRounded(v, ty.s, r.places, r.mode)
				if new(big.Int).Abs(u).Cmp(limit) >= 0 {
					if _, err := ursus.Frame(i128Col(t, dt, "d", v)).Select(r.e).Collect(t.Context()); !errors.Is(err, ursus.ErrValue) {
						t.Errorf("%s %s of %s: %v, want a refusal", dt, r.name, v, err)
					}
					checked++
					continue
				}
				keep, wants = append(keep, v), append(wants, u.String())
			}
			df, err := ursus.Frame(i128Col(t, dt, "d", keep...)).Select(r.e).Collect(t.Context())
			if err != nil {
				t.Fatalf("%s %s: %v", dt, r.name, err)
			}
			typ, got, err := decimalValues(df, "d")
			if err != nil || typ != want {
				t.Fatalf("%s %s: %s, %v, want %s", dt, r.name, typ, err, want)
			}
			for i := range keep {
				if got[i] != wants[i] {
					t.Fatalf("%s %s of %s: %s, big.Rat %s (unscaled)", dt, r.name, keep[i], got[i], wants[i])
				}
			}
			checked += len(keep)
		}
	}
	if checked < 10000 {
		t.Fatalf("%d rows checked: the sweep has gone vacuous", checked)
	}
}

// decimalRounded is the unscaled value v, at scale s, rounded to places by mode, at
// scale s, by big.Rat: or its sign, 10^s.
func decimalRounded(v string, s, places int, mode string) *big.Int {
	u, _ := new(big.Int).SetString(v, 10)
	if mode == "sign" {
		return new(big.Int).Mul(big.NewInt(int64(u.Sign())), pow10(s))
	}
	if places >= s {
		return u
	}
	x := new(big.Rat).SetFrac(u, pow10(s-places)) // the value at places, unscaled
	fl := new(big.Int).Div(x.Num(), x.Denom())    // Euclid's, a floor for a positive denominator
	frac := new(big.Rat).Sub(x, new(big.Rat).SetInt(fl))
	up := false
	switch c := frac.Cmp(big.NewRat(1, 2)); mode {
	case "floor":
	case "ceil":
		up = frac.Sign() != 0
	case "even":
		up = c > 0 || c == 0 && fl.Bit(0) == 1
	case "away":
		up = c > 0 || c == 0 && u.Sign() > 0
	}
	if up {
		fl.Add(fl, big.NewInt(1))
	}
	return fl.Mul(fl, pow10(s-places))
}
