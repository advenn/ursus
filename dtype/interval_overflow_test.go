package dtype

// Every bounds one count and nothing else.
//
// strconv.ParseInt(s[:n], 10, 32) bounds a single number to +-(2^31-1). Nothing
// bounds the PRODUCT of that count with its unit's multiplier, and nothing bounds
// the running SUM across components — so "357913942y" is eight months, with
// Err() nil, IsZero() false and Negative() false. Every downstream gate then
// inspects the wrapped value and finds it well formed.
//
// # The oracle is a magnitude, and that is the point
//
// The obvious invariant — "either it errors or String() round-trips" — is necessary
// and NOT sufficient: "357913942y" round-trips perfectly, to the wrong value. Only
// re-deriving the intended magnitude catches it, so this sweep computes each
// component in math/big from the digits and the unit and compares.
//
// It is an internal test because the unit table is unexported and deriving the axis
// from it is the whole point: a unit added to intervalUnits is swept the day it is
// named, rather than the day someone remembers to copy it.

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"testing"
)

// unitAxis describes one unit's single non-zero multiplier and the width it must fit.
type unitAxis struct {
	suffix string
	mult   int64 // the multiplier, whichever component it lands in
	limit  int64 // that component's maximum
	field  string
}

// axes derives one axis per declared unit, and refuses a unit that touches more than
// one component — the sweep's arithmetic assumes exactly one, and a unit that broke
// that would be swept wrongly rather than loudly.
func axes(t *testing.T) []unitAxis {
	t.Helper()
	var out []unitAxis
	for _, u := range intervalUnits {
		n := 0
		a := unitAxis{suffix: u.suffix}
		if u.months != 0 {
			n, a.mult, a.limit, a.field = n+1, int64(u.months), math.MaxInt32, "months"
		}
		if u.days != 0 {
			n, a.mult, a.limit, a.field = n+1, int64(u.days), math.MaxInt32, "days"
		}
		if u.nanos != 0 {
			n, a.mult, a.limit, a.field = n+1, u.nanos, math.MaxInt64, "nanos"
		}
		if n != 1 {
			t.Fatalf("unit %q sets %d components; this sweep assumes exactly one",
				u.suffix, n)
		}
		out = append(out, a)
	}
	if len(out) != len(intervalUnits) {
		t.Fatalf("%d axes for %d units", len(out), len(intervalUnits))
	}
	return out
}

// counts walks one unit from ordinary values up through and past its first wrap.
//
// Derived from the multiplier rather than written down: bound is the largest count
// that still fits, so bound+1 is the first input that cannot be represented and the
// multiples past it are where the wrap comes back round to a plausible positive
// number — which is the regime with no diagnostic at all.
func counts(a unitAxis) []int64 {
	bound := a.limit / a.mult
	cand := []int64{1, 2, 1000, bound - 1, bound, bound + 1, bound + 2,
		2 * bound, 2*bound + 2, 3 * bound, math.MaxInt32}
	var out []int64
	seen := map[int64]bool{}
	for _, v := range cand {
		if v >= 1 && v <= math.MaxInt32 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// wantComponents is the oracle: the intended value, in big.Int, and whether it fits.
func wantComponents(a unitAxis, v int64) (*big.Int, bool) {
	got := new(big.Int).Mul(big.NewInt(v), big.NewInt(a.mult))
	return got, got.CmpAbs(big.NewInt(a.limit)) <= 0
}

func componentOf(iv Interval, field string) int64 {
	switch field {
	case "months":
		return int64(iv.months)
	case "days":
		return int64(iv.days)
	}
	return iv.nanos
}

// knownEveryOverflows names every input Every answers wrongly today, measured before
// any repair. Emptied by the commit that bounds the arithmetic; an unlisted
// disagreement fails, and a listed one that now agrees is stale.
// Thirty entries, and they are EVERY unrepresentable input the sweep reaches — not
// one of them is refused today. That is the measurement: the bound does not exist,
// rather than existing and leaking.
var knownEveryOverflows = map[string]bool{
	"1431655764q": true,
	"1431655766q": true,
	"153722868m":  true,
	"153722869m":  true,
	"178956971y":  true,
	"178956972y":  true,
	"2147483646q": true,
	"2147483647h": true,
	"2147483647m": true,
	"2147483647q": true,
	"2147483647w": true,
	"2147483647y": true,
	"2562048h":    true,
	"2562049h":    true,
	"306783379w":  true,
	"306783380w":  true,
	"307445734m":  true,
	"307445736m":  true,
	"357913940y":  true,
	"357913942y":  true,
	"461168601m":  true,
	"5124094h":    true,
	"5124096h":    true,
	"536870910y":  true,
	"613566756w":  true,
	"613566758w":  true,
	"715827883q":  true,
	"715827884q":  true,
	"7686141h":    true,
	"920350134w":  true,
}

func TestEveryAgreesWithTheMagnitudeOracle(t *testing.T) {
	var checked, wrapped int
	observed := map[string]bool{}
	var wrong []string

	for _, a := range axes(t) {
		for _, v := range counts(a) {
			in := strconv.FormatInt(v, 10) + a.suffix
			want, fits := wantComponents(a, v)
			iv := Every(in)
			checked++
			if !fits {
				wrapped++
			}

			ok := true
			switch {
			case !fits:
				// Not representable: the only correct answer is a refusal.
				ok = iv.Err() != nil
			case iv.Err() != nil:
				ok = false
			default:
				ok = big.NewInt(componentOf(iv, a.field)).Cmp(want) == 0
			}
			if ok {
				continue
			}
			observed[in] = true
			if !knownEveryOverflows[in] && len(wrong) < 12 {
				wrong = append(wrong, fmt.Sprintf(
					"%s: %s = %d, want %s (err=%v)",
					in, a.field, componentOf(iv, a.field), want, iv.Err()))
			}
		}
	}

	if len(wrong) > 0 {
		t.Errorf("%d unlisted disagreements with the oracle:\n  %s",
			len(wrong), join(wrong))
	}
	for in := range knownEveryOverflows {
		if !observed[in] {
			t.Errorf("%q no longer disagrees — delete the entry", in)
		}
	}

	// Anti-vacuity. A sweep that never reaches an unrepresentable input proves
	// nothing about the bound, and one that reaches only those proves nothing about
	// ordinary parsing.
	if wrapped == 0 || wrapped == checked {
		t.Errorf("%d of %d inputs were unrepresentable; the sweep needs both",
			wrapped, checked)
	}
	t.Logf("units=%d inputs=%d unrepresentable=%d", len(intervalUnits), checked, wrapped)
}

// TestEverySumsAreBoundedToo is the case a single count cannot reach: two terms each
// individually legal whose SUM is not. The multiply and the add are separate
// operations and a fix that checks only one leaves this behind.
func TestEverySumsAreBoundedToo(t *testing.T) {
	for _, in := range []string{
		"2000000000ms2000000000ms", // nanos: 2e18 + 2e18 exceeds int64
		"178956970y178956970y",     // months: each fits, the sum does not
		"306783378w306783378w",     // days: likewise
	} {
		// Asserting the DEFECT, as the ratchet above does: each of these parses
		// today and must not. The commit that bounds the arithmetic flips this.
		if iv := Every(in); iv.Err() != nil {
			t.Errorf("%s now refuses (%v) — flip this test to assert the refusal",
				in, iv.Err())
		}
	}
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "\n  "
		}
		out += x
	}
	return out
}

// FuzzEvery is this repository's first fuzz target, and it checks the STRUCTURAL
// invariants rather than the magnitude.
//
// The division is deliberate. A magnitude oracle needs to know what the input meant,
// which means re-deriving the digits and units — a second parser, and two parsers
// are two chances to disagree. The sweep above CONSTRUCTS its inputs, so it knows
// the intended value without parsing anything; the fuzzer is handed arbitrary bytes
// and can only check properties that hold for every interval:
//
//  1. it parses, or it carries an error — never a panic
//  2. what parses renders, and what renders re-parses to the same value
//  3. what parses can be negated twice back to itself
//
// The third is what catches the MinInt32 fixed point, which the round-trip alone
// would miss on a value whose String() happens to re-parse. The second is what
// catches the "--178956970y-8mo" rendering.
// knownBadRenderings names the seeds whose rendering does not round-trip today.
var knownBadRenderings = map[string]bool{
	"536870912y": true,
}

func FuzzEvery(f *testing.F) {
	for _, s := range []string{
		"1h", "1y6mo", "-1d", "3mo", "1500ms", "0s", "", "-", "banana", "1h30",
		"--1h", "1.5h", "357913942y", "536870912y", "5124096h", "613566757w",
		"2000000000ms2000000000ms", "1h1d1227133513w",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		iv := Every(in) // must not panic
		if iv.Err() != nil {
			return
		}
		if knownBadRenderings[in] {
			// Listed because it fails TODAY, for the reason this step exists: the
			// count wraps to MinInt32 months, where negation is a fixed point, so
			// String() writes a leading '-' and then negates into itself. Emptied
			// by the commit that bounds the arithmetic.
			return
		}
		rendered := iv.String()
		back := Every(rendered)
		if back.Err() != nil {
			t.Fatalf("Every(%q) rendered %q, which does not parse: %v",
				in, rendered, back.Err())
		}
		if back != iv {
			t.Fatalf("Every(%q) -> %q -> {%d,%d,%d}, want {%d,%d,%d}",
				in, rendered, back.months, back.days, back.nanos,
				iv.months, iv.days, iv.nanos)
		}
		if twice := iv.Neg().Neg(); twice != iv {
			t.Fatalf("Every(%q).Neg().Neg() = {%d,%d,%d}, want {%d,%d,%d} — a "+
				"component is at its type's minimum, where negation is a fixed point",
				in, twice.months, twice.days, twice.nanos,
				iv.months, iv.days, iv.nanos)
		}
	})
}
