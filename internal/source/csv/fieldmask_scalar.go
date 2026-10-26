//go:build !(goexperiment.simd && amd64)

package csv

// vectorFields is false where the simd experiment is off or the CPU is not amd64:
// an unquoted field's end is found eight bytes at a time.
const vectorFields = false

// endsMask has bit k set where b[k] is sep or a newline, for b's first sixty-four
// bytes, a byte at a time.
func endsMask(b []byte, sep byte) uint64 {
	var m uint64
	for k, c := range b[:64] {
		if c == sep || c == '\n' {
			m |= 1 << k
		}
	}
	return m
}
