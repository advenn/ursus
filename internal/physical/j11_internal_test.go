package physical

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// TestJ11KeysShareFiveLevels keeps the root package's j11KeyA and j11KeyB what its
// test needs: one non-zero spill bucket at each of levels 0 to 4, so a hybrid split
// spills both, and two at level 5. Should the hashing change, find another pair.
func TestJ11KeysShareFiveLevels(t *testing.T) {
	col := data.NewFixed("k", dtype.Int64, []int64{1, 248238}, bitmap.AllSet(2))
	enc, err := kernel.NewGroupKeyEncoder("join", []*data.Column{col})
	if err != nil {
		t.Fatal(err)
	}
	// Encode's answer is overwritten by the next call, so the first is copied.
	a := append([]byte(nil), enc.Encode(0)...)
	b := enc.Encode(1)
	for level := range 5 {
		pa, pb := partitionOf(a, level), partitionOf(b, level)
		if pa != pb || pa == 0 {
			t.Fatalf("level %d: buckets %d and %d, want one, not 0", level, pa, pb)
		}
	}
	if partitionOf(a, 5) == partitionOf(b, 5) {
		t.Fatal("the keys share a bucket at level 5 too")
	}
}
