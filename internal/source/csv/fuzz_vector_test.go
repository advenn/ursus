//go:build goexperiment.simd && amd64

package csv

import (
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/fuzzcheck"
)

// FuzzVectorFields: any bytes, split with any separator through a buffer of 63 to 300
// bytes, whole, and a few bytes a read, are the same records and the same error with
// an unquoted field's end found in a mask of sixty-four bytes (vectorFields) as with
// it found eight bytes at a time: both paths, in one binary (step 509, step 446 of
// context_files/v0.7.2-plan.md). It sets vectorFields, so no test of this package runs
// in parallel with it; on a CPU without 256-bit vectors, or at GODEBUG=simd=128 or 0,
// there is one path, and it is skipped.
func FuzzVectorFields(f *testing.F) {
	for i, s := range scannerSeeds {
		f.Add([]byte(s), uint16(i), byte(','))
	}
	f.Add([]byte(strings.Repeat("abcdefghij,", 30)+"\n"+strings.Repeat("\"x,y\",z;", 20)), uint16(1), byte(','))
	f.Add([]byte(strings.Repeat("a;bb;ccc\n", 40)), uint16(0), byte(';'))
	f.Add([]byte(strings.Repeat("0123456789", 13)+"\n"), uint16(1), byte('5'))
	f.Fuzz(func(t *testing.T, data []byte, size uint16, sep byte) {
		if !vectorFields {
			t.Skip("one path: no 256-bit vectors on this CPU or at this width")
		}
		if sep == '\n' {
			sep = ','
		}
		o := DefaultOptions()
		o.Separator = sep
		o = o.normalise()
		n := 63 + int(size)%238
		fuzzcheck.Run(t, fuzzLimits(len(data)), func() error {
			defer func() { vectorFields = true }()
			vec := scanEveryWay(t, data, o, n)
			vectorFields = false
			word := scanEveryWay(t, data, o, n)
			if !equal(vec.recs, word.recs) || vec.err != word.err {
				t.Fatalf("buffer %d, separator %q: by mask %q, %q; eight bytes at a time %q, %q\ninput %q",
					n, sep, vec.recs, vec.err, word.recs, word.err, data)
			}
			return nil
		})
	})
}
