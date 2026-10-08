package data_test

import (
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestTakeStrings: a gather by offsets and bytes gives the rows a gather by Get
// does — from a sliced column, whose offsets do not start at zero, with nulls, empty
// strings, repeated and negative indices (step 123).
func TestTakeStrings(t *testing.T) {
	rng := rand.New(rand.NewPCG(123, 3))
	for range 200 {
		n := 1 + rng.IntN(60)
		vals := make([]string, n)
		vb := bitmap.NewBuilder(n)
		for i := range vals {
			if rng.IntN(5) > 0 {
				vals[i] = string(rune('a' + rng.IntN(26)))[:1] + randString(rng)
			}
			vb.Append(rng.IntN(6) > 0)
		}
		src := data.NewString("s", vals, vb.Finish())
		off := rng.IntN(n)
		src = src.Slice(off, n-off)

		sel := make([]int32, rng.IntN(40))
		for j := range sel {
			sel[j] = int32(rng.IntN(src.Len()+1)) - 1 // -1 is a NullIndex
		}
		out := data.TakeStrings(src, sel, bitmap.View{})
		if out.Len() != len(sel) {
			t.Fatalf("%d rows, want %d", out.Len(), len(sel))
		}
		in, got := src.Strings(), out.Strings()
		for j, i := range sel {
			want := ""
			if i >= 0 {
				want = in.Get(int(i))
			}
			if g := got.Get(j); g != want {
				t.Fatalf("row %d (from %d): %q, want %q", j, i, g, want)
			}
		}
		if out.DType() != dtype.String {
			t.Fatalf("the type became %s", out.DType())
		}
	}
}

func randString(rng *rand.Rand) string {
	b := make([]byte, rng.IntN(6))
	for i := range b {
		b[i] = byte('a' + rng.IntN(26))
	}
	return string(b)
}
