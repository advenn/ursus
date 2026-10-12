package kernel

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestJSONNumbersAndStringsReadBack: every float json_encode writes is a JSON number
// that reads back to the same float, bit for bit, for 64 and 32 bits, across
// magnitudes on both sides of the switch to an exponent; and every valid string
// reads back to itself, control characters, quotes, backslashes and every plane
// included (step 231). An invalid byte is written as U+FFFD.
func TestJSONNumbersAndStringsReadBack(t *testing.T) {
	rng := rand.New(rand.NewPCG(231, 1))
	floats := []float64{0, math.Copysign(0, -1), 1, -1, 0.1, 1e-5, 9.99e-6, 1e15, 1e16, 9.99e15,
		123456789012345678, math.MaxFloat64, math.SmallestNonzeroFloat64, 5e-324, 0.30000000000000004}
	for range 2000 {
		floats = append(floats, math.Ldexp(rng.Float64()-0.5, rng.IntN(2100)-1050))
	}
	for _, f := range floats {
		for _, bits := range []int{64, 32} {
			want := f
			if math.IsInf(f, 0) {
				continue // written as null, below
			}
			if bits == 32 {
				want = float64(float32(f))
				if math.IsInf(want, 0) {
					continue
				}
			}
			text := string(appendJSONFloat(nil, want, bits))
			var got float64
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatalf("%v (%d bits) wrote %q, not JSON: %v", want, bits, text, err)
			}
			if bits == 32 {
				got = float64(float32(got))
			}
			if math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("%v (%d bits) wrote %q, which reads back as %v", want, bits, text, got)
			}
			if !strings.ContainsAny(text, ".e") {
				t.Fatalf("%v wrote %q, which reads as an integer", want, text)
			}
		}
	}
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if text := string(appendJSONFloat(nil, f, 64)); text != "null" {
			t.Errorf("%v wrote %q, want null", f, text)
		}
	}

	for range 2000 {
		var b strings.Builder
		for range rng.IntN(20) {
			switch rng.IntN(4) {
			case 0:
				b.WriteRune(rune(rng.IntN(0x20)))
			case 1:
				b.WriteByte(`"\/`[rng.IntN(3)])
			default:
				r := rune(rng.IntN(0x10FFFF))
				if utf8.ValidRune(r) {
					b.WriteRune(r)
				}
			}
		}
		s := b.String()
		text := appendJSONString(nil, s)
		var got string
		if err := json.Unmarshal(text, &got); err != nil {
			t.Fatalf("%q wrote %q, not JSON: %v", s, text, err)
		}
		if got != s {
			t.Fatalf("%q wrote %q, which reads back as %q", s, text, got)
		}
	}
	// Written as U+FFFD, so the text is UTF-8, as JSON must be. Go's decoder reads
	// the byte itself back as U+FFFD too, so the bytes are what is checked.
	if text := appendJSONString(nil, "a\xffb"); string(text) != "\"a�b\"" || !utf8.Valid(text) {
		t.Errorf("an invalid byte wrote %q, want U+FFFD in its place", text)
	}
}
