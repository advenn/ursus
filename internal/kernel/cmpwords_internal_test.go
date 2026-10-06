package kernel

import (
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/expr"
)

// TestCompareWordsBothScalar: two broadcast scalars over n rows give one answer n
// times. Binary never asks for this — two one-element operands make a one-row
// answer there — but cmpWords must not swap the operands of a both-scalar call
// back and forth for ever if anything does.
func TestCompareWordsBothScalar(t *testing.T) {
	for _, n := range []int{2, 64, 130} {
		out := bitmap.NewBuilder(n)
		if err := cmpWords(out, expr.OpLt, []int64{1}, []int64{2}, n); err != nil {
			t.Fatal(err)
		}
		v := out.Finish()
		if v.Len() != n {
			t.Fatalf("n=%d: %d bits", n, v.Len())
		}
		for i := range n {
			if !v.Get(i) {
				t.Fatalf("n=%d: bit %d of 1 < 2 is false", n, i)
			}
		}
	}
}
