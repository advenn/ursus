package dtype_test

import (
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
)

// TestParseFloatIsStrconvsToTheBit: ParseFloat, which parses a plain decimal in one
// pass since step 373, answers as strconv.ParseFloat does, to the bit, over every
// shape of decimal its grammar takes, and refuses what it refused: a million random
// decimals of 1 to 22 digits, a point anywhere or none, leading and trailing zeros,
// an exponent or none, a sign or none, and the grammar's edges.
func TestParseFloatIsStrconvsToTheBit(t *testing.T) {
	check := func(s string) {
		t.Helper()
		got, gerr := dtype.ParseFloat(s, 64)
		want, werr := strconv.ParseFloat(s, 64)
		if werr != nil {
			// A syntax strconv refuses, or a value past a float64's range.
			if gerr == nil {
				t.Fatalf("%q: %v, where strconv refuses it: %v", s, got, werr)
			}
			return
		}
		if gerr != nil {
			if strings.ContainsAny(s, "_xXpP") {
				return // Go's own spellings, refused here on purpose
			}
			t.Fatalf("%q: refused, where strconv reads %v: %v", s, want, gerr)
		}
		if math.Float64bits(got) != math.Float64bits(want) && !(math.IsNaN(got) && math.IsNaN(want)) {
			t.Fatalf("%q: %v (%#x), strconv %v (%#x)", s, got, math.Float64bits(got), want, math.Float64bits(want))
		}
	}
	for _, s := range []string{
		"0", "-0", "+0", "0.0", "-0.0", "00012.5000", "1.", ".5", "-.5", "+.5e-3", "1e22", "1e23",
		"1e-22", "1e-23", "9007199254740992", "9007199254740993", "9007199254740993.0",
		"1234567890123456789", "12345678901234567890", "0.000000000000000000001234",
		"123456789012345678.9", "1.7976931348623157e308", "4.9e-324", "1e400", "1e-400",
		"inf", "-Infinity", "NaN", "nan", "1e", "1e+", "e5", "-", "+", "", ".", "-.", "1..2",
		"1.2.3", "1e5.5", " 1", "1 ", "1_000", "0x1p3", "1e00005", "1e0000005", "3.3e-5",
		"-1e16", "9999999999999999e6", "0.1", "0.2", "0.3", "30186.8946", "-26862.2166",
	} {
		check(s)
	}
	r := rand.New(rand.NewPCG(3, 73))
	digits := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('0' + r.IntN(10))
		}
		return string(b)
	}
	for range 1_000_000 {
		var b strings.Builder
		switch r.IntN(3) {
		case 0:
			b.WriteByte('-')
		case 1:
			if r.IntN(4) == 0 {
				b.WriteByte('+')
			}
		}
		whole := digits(r.IntN(12))
		frac := digits(r.IntN(12))
		b.WriteString(whole)
		if frac != "" || r.IntN(5) == 0 {
			b.WriteByte('.')
			b.WriteString(frac)
		}
		if r.IntN(3) == 0 {
			b.WriteByte("eE"[r.IntN(2)])
			if r.IntN(2) == 0 {
				b.WriteByte("+-"[r.IntN(2)])
			}
			b.WriteString(strconv.Itoa(r.IntN(40)))
		}
		check(b.String())
	}
}
