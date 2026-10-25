package kernel

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestValidUTF8MatchesTheStandardLibrary is step 357's vector UTF-8 check against
// utf8.Valid: random bytes, rich in lead and continuation bytes; every invalid kind
// at every place about the vectors' boundaries; and valid characters across them.
func TestValidUTF8MatchesTheStandardLibrary(t *testing.T) {
	check := func(b []byte) {
		t.Helper()
		want := utf8.Valid(b)
		if got := ValidUTF8(b); got != want {
			t.Fatalf("ValidUTF8(%q) = %v, want %v", b, got, want)
		}
		if utf8Vector {
			if got := validUTF8Vector(b); got != want {
				t.Fatalf("validUTF8Vector(%q) = %v, want %v", b, got, want)
			}
		}
	}

	// Bytes of every kind a decoder tells apart, ASCII the most.
	kinds := []byte{'a', 'z', '0', ' ', 0, 0x7F, 0x80, 0x8F, 0x90, 0x9F, 0xA0, 0xBF,
		0xC0, 0xC1, 0xC2, 0xDF, 0xE0, 0xE1, 0xEC, 0xED, 0xEE, 0xEF, 0xF0, 0xF1, 0xF3, 0xF4,
		0xF5, 0xF7, 0xF8, 0xFF}
	rng := rand.New(rand.NewPCG(357, 1))
	for range 200_000 {
		b := make([]byte, rng.IntN(100))
		for i := range b {
			if rng.IntN(3) == 0 {
				b[i] = 'a' + byte(rng.IntN(26))
			} else {
				b[i] = kinds[rng.IntN(len(kinds))]
			}
		}
		check(b)
	}

	// Valid text, then valid characters of one to four bytes, cut at every byte.
	text := []byte(strings.Repeat("ab€𝄞é東京 Zürich ", 12))
	for i := range len(text) {
		check(text[:i])
		check(text[i:])
	}

	// Each invalid sequence and each valid edge after every length of ASCII, so it
	// falls at every place in a vector and across two.
	seqs := [][]byte{
		{0x80}, {0xBF}, {0xC2}, {0xE2, 0x82}, {0xF0, 0x9D, 0x84}, // a continuation alone, characters cut
		{0xC0, 0x80}, {0xC1, 0xBF}, {0xE0, 0x80, 0x80}, {0xE0, 0x9F, 0xBF}, {0xF0, 0x80, 0x80, 0x80}, {0xF0, 0x8F, 0xBF, 0xBF}, // overlong
		{0xED, 0xA0, 0x80}, {0xED, 0xBF, 0xBF}, // surrogates
		{0xF4, 0x90, 0x80, 0x80}, {0xF5, 0x80, 0x80, 0x80}, {0xF7, 0xBF, 0xBF, 0xBF}, {0xF8, 0x88, 0x80, 0x80, 0x80}, {0xFF}, // too large
		{0xE2, 0x82, 0xAC, 0xAC}, {0xC3, 0xA9, 0xA9}, {0xE2, 0x41}, {0xC3, 0xC3, 0xA9}, // too many, too few
		{0xC2, 0x80}, {0xDF, 0xBF}, {0xE0, 0xA0, 0x80}, {0xED, 0x9F, 0xBF}, {0xEE, 0x80, 0x80}, {0xF0, 0x90, 0x80, 0x80}, {0xF4, 0x8F, 0xBF, 0xBF}, // valid edges
	}
	for _, seq := range seqs {
		for n := range 100 {
			pre := bytes.Repeat([]byte{'x'}, n)
			check(append(pre[:n:n], seq...))
			check(append(append(pre[:n:n], seq...), strings.Repeat("ASCII past two vectors' ends. ", 5)...))
			check(append(append(pre[:n:n], seq...), "é東"...))
			check(append(append([]byte("東京é"), pre...), seq...))
		}
	}
}

func BenchmarkValidUTF8(b *testing.B) {
	for _, tc := range []struct{ name, text string }{
		{"ascii", strings.Repeat("id,name-123456,alpha beta,0.123456\n", 1000)},
		{"mixed", strings.Repeat("id,Zürich-123456,東京 naïve,0.123456\n", 1000)},
	} {
		in := []byte(tc.text)
		b.Run(tc.name+"/std", func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			for b.Loop() {
				utf8.Valid(in)
			}
		})
		b.Run(tc.name+"/kernel", func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			for b.Loop() {
				ValidUTF8(in)
			}
		})
	}
}
