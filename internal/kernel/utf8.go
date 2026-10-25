package kernel

import "unicode/utf8"

// ValidUTF8 reports whether b is UTF-8, as utf8.Valid does, thirty-two bytes a step
// where the CPU has AVX2 (step 357). utf8.Valid reads eight bytes of ASCII a step
// until the first character past it, and a byte a step from there, so that text of
// a non-ASCII character a word, 東京 or Zürich, was checked at 1.6 GB/s.
func ValidUTF8(b []byte) bool {
	if utf8Vector && len(b) >= 32 {
		return validUTF8Vector(b)
	}
	return utf8.Valid(b)
}
