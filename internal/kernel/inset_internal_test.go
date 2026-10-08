package kernel

import (
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestInStringsAgreesWithInSet: membership tested against the plain members, few or
// many, answers what membership tested through encoded keys does, row for row, with
// nulls, empty strings, and a column sliced so its offsets do not start at zero
// (step 125).
func TestInStringsAgreesWithInSet(t *testing.T) {
	rng := rand.New(rand.NewPCG(125, 1))
	words := []string{"", "a", "ab", "MAIL", "SHIP", "AIR", "REG AIR", "TRUCK", "FOB", "RAIL", "x\x00y"}
	for round := range 300 {
		n := 1 + rng.IntN(200)
		vals := make([]string, n)
		vb := bitmap.NewBuilder(n)
		for i := range vals {
			vals[i] = words[rng.IntN(len(words))]
			vb.Append(rng.IntN(7) > 0)
		}
		col := data.NewString("s", vals, vb.Finish())
		if off := rng.IntN(n); off > 0 {
			col = col.Slice(off, n-off)
		}
		// Sets of one to all the words: under and over the one-by-one limit.
		set := map[string]struct{}{}
		for range 1 + rng.IntN(len(words)) {
			one := data.NewString("m", []string{words[rng.IntN(len(words))]}, bitmap.View{})
			key, err := EncodeOne(one)
			if err != nil {
				t.Fatal(err)
			}
			set[string(key)] = struct{}{}
		}
		members, plain, ok := StringMembers(set)
		if !ok {
			t.Fatalf("round %d: the keys of a String set were not recognised", round)
		}
		want, err := InSet("r", col, set)
		if err != nil {
			t.Fatal(err)
		}
		got := InStrings("r", col, members, plain)
		wb, gb := want.Bools(), got.Bools()
		for i := range col.Len() {
			if col.IsValid(i) && wb.Get(i) != gb.Get(i) {
				t.Fatalf("round %d row %d %q: %v, InSet says %v", round, i, col.Strings().Get(i), gb.Get(i), wb.Get(i))
			}
			if got.IsValid(i) != col.IsValid(i) {
				t.Fatalf("round %d row %d: validity differs", round, i)
			}
		}
	}
	// A set keyed by another type's encoding is not taken for strings.
	ints := data.NewFixed("i", dtype.Int64, []int64{7}, bitmap.View{})
	key, _ := EncodeOne(ints)
	if _, _, ok := StringMembers(map[string]struct{}{string(key): {}}); ok {
		t.Error("an Int64 key read as a string member")
	}
}
