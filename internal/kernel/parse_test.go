package kernel_test

// Every String -> number cast is exact or refused, against math/big.
//
// The parse went through a float64 for every numeric target — integers too — and
// then through narrow, whose round-trip test compared a value that had already
// rounded with itself. So "9007199254740993" came back as …992, a Uint64 above
// MaxInt64 could not be parsed at all, and a strict cast dropped its strictness on
// the way down: "256" to Uint8 became a null.
//
// The targets are derived: every numeric type a String casts to. The strings are
// every integer type's edges in several spellings, the midpoints between adjacent
// floats of each width with a nudge either side too small for a float64 to see, the
// overflow and underflow of each float width, and text that is not a number at all.
// The oracles are big.Int and big.Rat, which share no code with the kernel.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// knownWrong counts the values a pair answers wrongly, and says how.
type knownWrong struct {
	n   int
	why string
}

// knownParseDefects is counted per target, so a partial fix changes the number
// rather than hiding behind a listed name.
var knownParseDefects = map[string]knownWrong{}

// parseTargets is every numeric type a String casts to, derived.
func parseTargets(t *testing.T) []dtype.DataType {
	t.Helper()
	var out []dtype.DataType
	for id := range int(dtype.TypeIDCount) {
		dt, ok := castTarget(dtype.TypeID(id))
		if ok && dt.IsNumeric() && dtype.CanCast(dtype.String, dt) {
			out = append(out, dt)
		}
	}
	if len(out) != 10 {
		t.Fatalf("%d numeric targets for a String, want 10: %v", len(out), out)
	}
	for _, refused := range []dtype.DataType{dtype.Int128, dtype.Decimal(10, 2)} {
		if dtype.CanCast(dtype.String, refused) {
			t.Fatalf("String -> %s is castable now; add it to this sweep", refused)
		}
	}
	return out
}

// floatMidpoints is, for each float width, points exactly halfway between two
// adjacent values of that width, where the tie decides — and where a string just
// either side of one parses through a float64 ONTO the midpoint and then ties.
func floatMidpoints() []*big.Rat {
	two := func(e int) *big.Rat {
		if e >= 0 {
			return new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), uint(e)))
		}
		return new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), uint(-e)))
	}
	add := func(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }
	sub := func(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }
	return []*big.Rat{
		add(two(0), two(-24)),                // 1 and the next float32
		add(two(24), two(0)),                 // 2^24 and 2^24+2
		sub(two(128), two(103)),              // MaxFloat32 and 2^128: the overflow threshold
		two(-150),                            // 0 and the smallest float32
		add(two(-126), two(-150)),            // the smallest normal float32 and the next
		add(two(0), two(-53)),                // 1 and the next float64
		add(two(53), two(0)),                 // 2^53 and 2^53+2
		sub(two(1024), two(970)),             // MaxFloat64 and 2^1024
		two(-1075),                           // 0 and the smallest float64
		add(two(0), add(two(-24), two(-53))), // a float32 midpoint that is not a float64 one
	}
}

// nudged renders m and a value a hair either side of it as exact decimals. The hair
// is 10^-(digits+10), far below half a float64 ulp of m.
func nudged(m *big.Rat) []string {
	digits := 0
	for d := new(big.Int).Set(m.Denom()); d.Cmp(big.NewInt(1)) > 0; d.Rsh(d, 1) {
		digits++ // a denominator of 2^k needs k decimal places
	}
	prec := digits + 10
	eps := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(prec)), nil))
	out := []string{m.FloatString(digits)}
	for _, x := range []*big.Rat{new(big.Rat).Add(m, eps), new(big.Rat).Sub(m, eps)} {
		out = append(out, x.FloatString(prec))
		out = append(out, "-"+x.FloatString(prec))
	}
	return out
}

// parseStrings is every string the sweep casts.
func parseStrings(t *testing.T) []string {
	t.Helper()
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, v := range edges(integerTypes(t)) {
		s := v.String()
		add(s)
		if v.Sign() >= 0 {
			add("+" + s)
			add("00" + s)
		}
	}
	add("-0")
	for _, m := range floatMidpoints() {
		for _, s := range nudged(m) {
			add(s)
		}
	}
	for _, s := range []string{"0.1", "-0.1", "1e400", "-1e400", "3.4e39", "-3.4e39",
		"1e-50", "-1e-50", "1e-400", "3.4028235e38", "3.4028236e38", "1.5", "1e3"} {
		add(s)
	}
	for s := range floatSpecials {
		add(s)
	}
	for _, s := range malformed {
		add(s)
	}
	return out
}

// floatSpecials are the texts a float parse accepts that big.Rat does not.
var floatSpecials = map[string]float64{
	"inf": math.Inf(1), "-Inf": math.Inf(-1), "+Infinity": math.Inf(1), "NaN": math.NaN(),
}

// malformed is text that is no number at all, for any target.
var malformed = []string{"", " 1", "1 ", "abc", "1e", "--1", "+-1", ".", "-", "+", "1/2", "0x10", "1_000", "0x1p3"}

// parseWant is the oracle's answer for s cast to dt: a value, or unrepresentable
// (an integer out of range, a float that overflows), or malformed.
type parseWant struct {
	malformed, unrepresentable bool
	i                          *big.Int
	f                          float64
}

func oracle(s string, dt dtype.DataType) parseWant {
	if dt.IsInteger() {
		v, ok := new(big.Int).SetString(s, 10)
		switch {
		case !ok || strings.Contains(s, "_"):
			return parseWant{malformed: true}
		case !inRange(v, dt):
			return parseWant{unrepresentable: true}
		}
		return parseWant{i: v}
	}
	if f, ok := floatSpecials[s]; ok {
		return parseWant{f: f}
	}
	// Go's float syntax takes "1_000" and "0x1p3"; the one float grammar does not
	// (step 77, dtype.ParseFloat), and until then this oracle skipped them.
	if strings.ContainsAny(s, "_/xX") { // big.Rat reads fractions and prefixes; a float parse does not
		return parseWant{malformed: true}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return parseWant{malformed: true}
	}
	var f float64
	if dt.ID() == dtype.TypeFloat32 {
		f32, _ := r.Float32()
		f = float64(f32)
	} else {
		f, _ = r.Float64()
	}
	if math.IsInf(f, 0) {
		return parseWant{unrepresentable: true}
	}
	return parseWant{f: f}
}

// parsedVal is one value of a parsed column: an integer as a big.Int, a float as a
// float64, or a null.
type parsedVal struct {
	i    *big.Int
	f    float64
	null bool
}

func (p parsedVal) String() string {
	switch {
	case p.null:
		return "null"
	case p.i != nil:
		return p.i.String()
	}
	return fmt.Sprint(p.f)
}

// readParsed reads a parsed column back, once.
func readParsed(t *testing.T, c *data.Column) []parsedVal {
	t.Helper()
	out := make([]parsedVal, c.Len())
	valid := c.Validity()
	switch c.DType().ID() {
	case dtype.TypeFloat32:
		v, err := data.Values[float32](c)
		if err != nil {
			t.Fatal(err)
		}
		for i := range out {
			out[i] = parsedVal{f: float64(v[i]), null: !valid.Get(i)}
		}
	case dtype.TypeFloat64:
		v, err := data.Values[float64](c)
		if err != nil {
			t.Fatal(err)
		}
		for i := range out {
			out[i] = parsedVal{f: v[i], null: !valid.Get(i)}
		}
	default:
		for i, v := range intValues(t, c) {
			out[i] = parsedVal{i: v, null: v == nil}
		}
	}
	return out
}

// matches reports whether a non-null parsed value is the oracle's.
func (p parsedVal) matches(w parseWant) bool {
	if p.null {
		return false
	}
	if w.i != nil {
		return p.i != nil && p.i.Cmp(w.i) == 0
	}
	return sameFloat(p.f, w.f)
}

func sameFloat(a, b float64) bool { return a == b || (a != a && b != b) }

func TestStringParsesAreExactOrRefused(t *testing.T) {
	strs := parseStrings(t)
	for _, s := range malformed {
		if w := oracle(s, dtype.Int64); !w.malformed {
			t.Fatalf("the integer oracle accepts %q", s)
		}
		if w := oracle(s, dtype.Float64); !w.malformed {
			t.Fatalf("the float oracle accepts %q", s)
		}
	}
	col := data.NewString("s", strs, bitmap.AllSet(len(strs)))
	floors := map[string]bool{}
	var values int

	for _, dt := range parseTargets(t) {
		name := dt.String()
		var wrong []string
		lossyCol, err := kernel.Cast("s", dt, false, col)
		if err != nil {
			t.Fatalf("%s: CastLossy refused: %v", name, err)
		}
		lossy := readParsed(t, lossyCol)
		for i, s := range strs {
			w := oracle(s, dt)
			values++
			switch g := lossy[i]; {
			case w.malformed || w.unrepresentable:
				if !g.null {
					wrong = append(wrong, fmt.Sprintf("lossy %q -> %s, want null", s, g))
				}
			case !g.matches(w):
				wrong = append(wrong, fmt.Sprintf("lossy %q -> %s", s, g))
			}

			// Strict, one value at a time.
			one := data.NewString("s", []string{s}, bitmap.AllSet(1))
			got, err := kernel.Cast("s", dt, true, one)
			switch {
			case w.malformed || w.unrepresentable:
				switch {
				case err == nil:
					wrong = append(wrong, fmt.Sprintf("strict %q -> %s, want a refusal", s, readParsed(t, got)[0]))
				case !errors.Is(err, uerr.ErrValue):
					t.Errorf("%s: %q refused with the wrong kind: %v", name, s, err)
				case strings.Contains(err.Error(), "cannot parse") != w.malformed:
					wrong = append(wrong, fmt.Sprintf("strict %q refused as %q", s, firstLine(err)))
				}
			case err != nil:
				wrong = append(wrong, fmt.Sprintf("strict %q refused: %s", s, firstLine(err)))
			default:
				if g := readParsed(t, got)[0]; !g.matches(w) {
					wrong = append(wrong, fmt.Sprintf("strict %q -> %s", s, g))
				}
			}

			// What the sweep must reach, per target.
			switch {
			case dt.IsInteger() && w.unrepresentable && strings.HasPrefix(s, "-"):
				floors[name+" below"] = true
			case dt.IsInteger() && w.unrepresentable:
				floors[name+" above"] = true
			case dt.IsFloat() && w.unrepresentable:
				floors[name+" overflow"] = true
			case dt.IsFloat() && !w.malformed && w.f == 0 && strings.ContainsAny(s, "123456789"):
				floors[name+" underflow"] = true
			}
			if dt.ID() == dtype.TypeFloat32 && !w.malformed && !w.unrepresentable {
				if via, err := parseViaFloat64(s); err == nil && !sameFloat(via, w.f) {
					floors["Float32 via a float64 is wrong"] = true
				}
			}
		}
		judgeCounted(t, knownParseDefects, name, wrong)
	}

	for _, dt := range parseTargets(t) {
		if dt.IsInteger() {
			for _, side := range []string{" below", " above"} {
				if !floors[dt.String()+side] {
					t.Errorf("no string out of range%s %s", side, dt)
				}
			}
		} else {
			for _, k := range []string{" overflow", " underflow"} {
				if !floors[dt.String()+k] {
					t.Errorf("no %s string for %s", strings.TrimSpace(k), dt)
				}
			}
		}
	}
	if !floors["Float32 via a float64 is wrong"] {
		t.Error("no string that a parse through a float64 gets wrong for Float32; the midpoints are not doing their job")
	}
	if values < 1500 {
		t.Fatalf("only %d values", values)
	}
}

// parseViaFloat64 is the double rounding the kernel used to do.
func parseViaFloat64(s string) (float64, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, fmt.Errorf("no")
	}
	f, _ := r.Float64()
	return float64(float32(f)), nil
}

func firstLine(err error) string { return strings.SplitN(err.Error(), "\n", 2)[0] }

// judgeCounted is the two-way counted ratchet: a known pair must still be wrong in
// exactly the counted number of values, and an unknown one must not be wrong at all.
func judgeCounted(t *testing.T, known map[string]knownWrong, name string, wrong []string) {
	t.Helper()
	k, listed := known[name]
	switch {
	case listed && len(wrong) == 0:
		t.Errorf("%s answers correctly now; delete it from the ratchet (%s)", name, k.why)
	case listed && len(wrong) != k.n:
		t.Errorf("%s: %d wrong, the ratchet says %d (%s); first: %v", name, len(wrong), k.n, k.why, first(wrong, 3))
	case listed:
		t.Logf("%s: %d known wrong (%s)", name, len(wrong), k.why)
	case len(wrong) > 0:
		t.Errorf("%s: %d wrong: %v", name, len(wrong), first(wrong, 8))
	}
}

func first(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}
