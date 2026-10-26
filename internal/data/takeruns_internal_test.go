package data

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
)

// TestTakeStringsByRuns: since step 379 a gather copies a run of at least four
// consecutive rows at once and shifts its offsets, and a lone row of at most sixteen
// bytes as two words where both buffers have room past it. Runs of one row to twenty,
// and a pair whose fourth row looks like a run's; from the source's first rows and to
// its last, between negative indices; of null rows, empty rows and rows shorter and
// longer than sixteen bytes; from a column and from a slice of it whose offsets do not
// start at zero; of 32-bit offsets and 64-bit, into either.
func TestTakeStringsByRuns(t *testing.T) {
	defer func(old int64) { MaxStringBytes = old }(MaxStringBytes)
	limit := MaxStringBytes
	rng := rand.New(rand.NewPCG(379, 1))
	seen := map[[2]bool]int{}
	for round := range 400 {
		n := 1 + rng.IntN(80)
		vals, valid := make([]string, n), make([]bool, n)
		for i := range n {
			valid[i] = rng.IntN(6) > 0
			if valid[i] {
				vals[i] = strings.Repeat(string(rune('a'+i%26)), rng.IntN(41))
			}
		}
		MaxStringBytes = limit
		if round%2 == 1 {
			MaxStringBytes = 16
		}
		src := NewString("s", vals, viewOf(valid))
		if rng.IntN(2) == 0 {
			off := rng.IntN(n)
			src = src.Slice(off, n-off)
		}
		m := src.Len()
		if m == 0 {
			continue
		}
		in := src.Strings()
		var sel []int32
		if m >= 8 {
			// The fourth row is the first's plus three, the third is not its plus two.
			sel = append(sel, 2, 3, 7, 5, 6)
		}
		for len(sel) < 60 {
			if rng.IntN(5) == 0 {
				sel = append(sel, -1)
				continue
			}
			start := rng.IntN(m)
			if rng.IntN(4) == 0 {
				start = max(0, m-1-rng.IntN(3)) // a run to the source's last row
			}
			for i := start; i < min(m, start+1+rng.IntN(20)); i++ {
				sel = append(sel, int32(i))
			}
		}
		want, wantValid := make([]string, len(sel)), make([]bool, len(sel))
		chars := 0
		for j, i := range sel {
			if i >= 0 {
				want[j], wantValid[j] = in.Get(int(i)), src.IsValid(int(i))
				chars += len(in.Bytes(int(i)))
			}
		}
		MaxStringBytes = limit
		if round%4 >= 2 {
			MaxStringBytes = 16
		}
		out := TakeStrings(src, sel, viewOf(wantValid))
		wide := out.offs64 != nil
		seen[[2]bool{src.offs64 != nil, wide}]++
		wantRows(t, "a gather by runs", out, want, wantValid, wide)
		if got := len(out.RawChars()); got != chars {
			t.Fatalf("round %d: %d characters, want %d", round, got, chars)
		}
	}
	for _, k := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		if seen[k] == 0 {
			t.Errorf("no gather from 64-bit offsets %v into 64-bit %v", k[0], k[1])
		}
	}

	// A column of no payload gathers empty rows.
	nulls := NewNull("s", dtype.String, 5)
	sel := []int32{0, 1, 2, -1, 4, 4, 3}
	out := TakeStrings(nulls, sel, viewOf(make([]bool, len(sel))))
	wantRows(t, "from no payload", out, make([]string, len(sel)), make([]bool, len(sel)), false)
}
