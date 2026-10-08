package bitmap_test

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
)

// TestAppendSetPositions: the positions set in both views, a word at a time, are the
// ones a bit-by-bit loop finds — over sliced views at every bit offset, lengths that
// are and are not multiples of 64, a b with no storage, and sparse and dense bits
// (step 123).
func TestAppendSetPositions(t *testing.T) {
	rng := rand.New(rand.NewPCG(123, 1))
	for range 400 {
		n := rng.IntN(300)
		offA, offB := rng.IntN(70), rng.IntN(70)
		dense := rng.Float64()
		gen := func(off int) (ref, bitmap.View) {
			r := make(ref, off+n)
			for i := range r {
				r[i] = rng.Float64() < dense
			}
			return r[off:], r.view().Slice(off, n)
		}
		ra, a := gen(offA)
		rb, b := gen(offB)
		if rng.IntN(4) == 0 {
			rb, b = make(ref, n), bitmap.AllSet(n)
			for i := range rb {
				rb[i] = true
			}
		}
		var want []int32
		for i := range n {
			if ra[i] && rb[i] {
				want = append(want, int32(i))
			}
		}
		got := bitmap.AppendSetPositions(nil, a, b)
		if !slices.Equal(got, want) {
			t.Fatalf("n=%d offsets %d,%d: %v, want %v", n, offA, offB, got, want)
		}
	}
}

// TestWordsAcrossEveryOffset: Words, whose word read now takes eight bytes at once
// where the buffer allows, still yields the bits a bit-by-bit read does, at every
// offset, including the last words of a buffer, where it cannot.
func TestWordsAcrossEveryOffset(t *testing.T) {
	rng := rand.New(rand.NewPCG(123, 2))
	full := randRef(rng, 1000)
	v := full.view()
	for off := range 130 {
		for _, n := range []int{0, 1, 63, 64, 65, 200, 1000 - off} {
			if off+n > len(full) {
				continue
			}
			s := v.Slice(off, n)
			pos := 0
			s.Words(func(w uint64, nbits int) bool {
				for k := range nbits {
					if got := w>>uint(k)&1 == 1; got != full[off+pos+k] {
						t.Fatalf("offset %d length %d: bit %d is %v", off, n, pos+k, got)
					}
				}
				if nbits < 64 && w>>uint(nbits) != 0 {
					t.Fatalf("offset %d length %d: bits past the end are set", off, n)
				}
				pos += nbits
				return true
			})
			if pos != n {
				t.Fatalf("offset %d length %d: %d bits yielded", off, n, pos)
			}
		}
	}
}
