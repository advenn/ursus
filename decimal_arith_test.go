package ursus_test

// Decimal + - * / against exact rationals, generated over pairs of Decimal types and
// each type's edges.
//
// For + - and *, the answer is the exact value at the rule's scale, or — when that
// needs more digits than the rule's precision — a refusal: each such row is run on
// its own, so a refusal is attributed to the row that earns it and a row that should
// answer is never hidden behind one that should not. For /, it is the nearest
// Float64 to the exact quotient. The oracle is big.Rat, which shares no code with the
// kernels.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// knownDecimalArithMismatches counts the wrong rows per type pair and operator.
var knownDecimalArithMismatches = map[string]int{}

type decType struct{ p, s int }

func (d decType) dt() dtype.DataType { return dtype.Decimal(uint8(d.p), uint8(d.s)) }

// edges are d's unscaled edge values: ±max, ±1, 0, 7, and one when the type holds it.
func (d decType) edges() []string {
	max := strings.Repeat("9", d.p)
	out := []string{max, "-" + max, "1", "-1", "0", "7"}
	if d.s < d.p {
		out = append(out, "1"+strings.Repeat("0", d.s))
	}
	return out
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// ratOf is unscaled/10^scale.
func ratOf(unscaled string, scale int) *big.Rat {
	u, _ := new(big.Int).SetString(unscaled, 10)
	return new(big.Rat).SetFrac(u, pow10(scale))
}

// decimalRule is the result type of a op b, or ok false where it is refused at plan.
func decimalRule(op string, a, b decType) (decType, bool) {
	switch op {
	case "+", "-":
		s := max(a.s, b.s)
		return decType{min(38, max(a.p-a.s, b.p-b.s)+s+1), s}, true
	case "*":
		if a.s+b.s > 38 {
			return decType{}, false
		}
		return decType{min(38, a.p+b.p), a.s + b.s}, true
	}
	return decType{}, false // "/" is Float64
}

func TestDecimalArithmeticAgainstExactRationals(t *testing.T) {
	c := ursus.Col
	types := []decType{{1, 0}, {5, 2}, {10, 2}, {12, 3}, {18, 9}, {38, 0}, {38, 10}, {38, 38}}
	apply := func(op string) ursus.Expr {
		switch op {
		case "+":
			return c("a").Add(c("b"))
		case "-":
			return c("a").Sub(c("b"))
		case "*":
			return c("a").Mul(c("b"))
		}
		return c("a").Div(c("b"))
	}
	var pairs, rows int
	for _, A := range types {
		for _, B := range types {
			for _, op := range []string{"+", "-", "*", "/"} {
				pairs++
				key := fmt.Sprintf("%s %s %s", A.dt(), op, B.dt())
				var as, bs []string
				for _, x := range A.edges() {
					for _, y := range B.edges() {
						as, bs = append(as, x), append(bs, y)
					}
				}
				rows += len(as)
				wrong := decimalPairWrong(t, op, A, B, as, bs, apply(op))
				n, known := knownDecimalArithMismatches[key]
				switch {
				case known && wrong == 0:
					t.Errorf("%s answers correctly now; delete it from knownDecimalArithMismatches", key)
				case known && wrong != n:
					t.Errorf("%s: %d wrong rows, the ratchet says %d", key, wrong, n)
				case !known && wrong > 0:
					t.Errorf("%s: %d wrong rows", key, wrong)
				}
			}
		}
	}
	if pairs != 256 || rows < 10000 {
		t.Fatalf("%d pairs, %d rows — the sweep has gone vacuous", pairs, rows)
	}
}

// decimalPairWrong runs a op b over the rows and counts those answered wrongly.
func decimalPairWrong(t *testing.T, op string, A, B decType, as, bs []string, e ursus.Expr) int {
	t.Helper()
	frame := func(as, bs []string) *ursus.LazyFrame {
		return ursus.Frame(i128Col(t, A.dt(), "a", as...), i128Col(t, B.dt(), "b", bs...)).Select(e.Alias("r"))
	}
	if op == "/" {
		df, err := frame(as, bs).Collect(t.Context())
		if err != nil {
			return len(as)
		}
		col, err := df.Column[float64]("r")
		if err != nil {
			return len(as)
		}
		wrong := 0
		for i := range as {
			x, y := ratOf(as[i], A.s), ratOf(bs[i], B.s)
			var want float64
			switch {
			case y.Sign() != 0:
				want, _ = new(big.Rat).Quo(x, y).Float64()
			case x.Sign() == 0:
				want = math.NaN()
			default:
				want = math.Inf(x.Sign())
			}
			if g, ok := col.Get(i); !ok || !(g == want || (g != g && want != want)) {
				wrong++
			}
		}
		return wrong
	}

	R, ok := decimalRule(op, A, B)
	if !ok {
		// Refused at plan: the scale of the product would pass 38.
		if _, err := frame(as[:1], bs[:1]).CollectSchema(t.Context()); errors.Is(err, ursus.ErrType) {
			return 0
		}
		return len(as)
	}
	limit := pow10(R.p)
	var okA, okB []string
	var wants []string
	wrong := 0
	for i := range as {
		x, y := ratOf(as[i], A.s), ratOf(bs[i], B.s)
		v := new(big.Rat)
		switch op {
		case "+":
			v.Add(x, y)
		case "-":
			v.Sub(x, y)
		case "*":
			v.Mul(x, y)
		}
		u := new(big.Rat).Mul(v, new(big.Rat).SetInt(pow10(R.s)))
		if !u.IsInt() {
			t.Fatalf("%s %s %s at scale %d is not an integer", as[i], op, bs[i], R.s)
		}
		if new(big.Int).Abs(u.Num()).Cmp(limit) >= 0 {
			// Needs more digits than the result's precision: refused, alone.
			_, err := frame(as[i:i+1], bs[i:i+1]).Collect(t.Context())
			if !errors.Is(err, ursus.ErrValue) {
				wrong++
			}
			continue
		}
		okA, okB, wants = append(okA, as[i]), append(okB, bs[i]), append(wants, u.Num().String())
	}
	if len(okA) == 0 {
		return wrong
	}
	df, err := frame(okA, okB).Collect(t.Context())
	if err != nil {
		return wrong + len(okA)
	}
	typ, got, err := decimalValues(df, "r")
	if err != nil || typ != R.dt() {
		return wrong + len(okA)
	}
	for i := range okA {
		if got[i] != wants[i] {
			wrong++
		}
	}
	return wrong
}
