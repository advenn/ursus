package kernel

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestTextSortMatchesTheComparator is step 363's: a sort by text, seven bytes a
// round, gives the comparator's stable sort exactly, on one worker and on four:
//   - strings that are prefixes of one another, an empty one, zero bytes, bytes
//     past 0x7f, lengths about every multiple of seven;
//   - a long prefix that many share, so a run of one key is sorted again and again;
//   - distinct strings, and strings of few values, so runs are long and short;
//   - nulls first and last, ascending and descending;
//   - text beside an integer key, before and after it, and two of text.
func TestTextSortMatchesTheComparator(t *testing.T) {
	defer func(m int) { parallelMin = m }(parallelMin)
	parallelMin = 64
	rng := rand.New(rand.NewPCG(363, 1))
	words := []string{"", "a", "a\x00", "a\x00\x00", "ab", "abcdefg", "abcdefg\x00",
		"abcdefgh", "abcdefgh\x00", "abcdefghi", "abcdefghijklmn", "abcdefghijklmno", "é", "\xff", "\xff\xfe"}
	corpora := map[string]func(i int) string{
		"words": func(int) string { return words[rng.IntN(len(words))] },
		"bytes": func(int) string {
			b := make([]byte, rng.IntN(23))
			for j := range b {
				b[j] = []byte{0, 1, 'a', 0xff}[rng.IntN(4)]
			}
			return string(b)
		},
		"a long shared prefix": func(int) string {
			return "https://example.com/a/b/" + strings.Repeat("x", rng.IntN(3)) + fmt.Sprint(rng.IntN(300))
		},
		"distinct": func(i int) string { return fmt.Sprintf("s%07d", (i*7919)%1_000_003) },
	}
	for _, n := range []int{5, 33, 200, 5000, 70_000} {
		few := make([]int64, n)
		for i := range few {
			few[i] = int64(rng.IntN(5))
		}
		ints := data.NewFixed("i", dtype.Int64, few, bitmap.AllSet(n))
		for name, gen := range corpora {
			vals := make([]string, n)
			for i := range vals {
				vals[i] = gen(i)
			}
			b := bitmap.NewBuilder(n)
			for i := range n {
				b.Append(i%9 != 4)
			}
			text := data.NewString("s", vals, bitmap.AllSet(n))
			nullable := data.NewString("sn", vals, b.Finish())
			// The same strings rotated by half, a second key of text.
			other := data.NewString("o", append(slices.Clone(vals[n/2:]), vals[:n/2]...), bitmap.AllSet(n))
			cases := [][]*data.Column{{text}, {nullable}, {nullable, ints}, {ints, nullable}, {text, other}}
			for _, cols := range cases {
				for _, sp := range []SortSpec{{}, {Descending: true}, {NullsLast: true}, {Descending: true, NullsLast: true}} {
					specs := make([]SortSpec, len(cols))
					for i := range specs {
						specs[i] = sp
					}
					cmp, err := NewComparator(cols, specs)
					if err != nil {
						t.Fatal(err)
					}
					want := ArgSort(n, cmp)
					for _, w := range []int{1, 4} {
						got, err := ArgSortColumnsN(cols, specs, n, w)
						if err != nil {
							t.Fatal(err)
						}
						if !slices.Equal(got, want) {
							i := firstDiff(got, want)
							t.Fatalf("%s, %d rows, %d keys, %+v on %d workers: row %d is %d (%q), want %d (%q)",
								name, n, len(cols), sp, w, i, got[i], vals[got[i]], want[i], vals[want[i]])
						}
					}
				}
			}
		}
	}
}

// TestTextKeysOrderAsTheirStrings: a key from any offset orders two strings that
// share the bytes before it as bytes.Compare orders them, or ties them, with a low
// byte of eight, only when both have more past it.
func TestTextKeysOrderAsTheirStrings(t *testing.T) {
	rng := rand.New(rand.NewPCG(363, 2))
	gen := func() []byte {
		b := make([]byte, rng.IntN(20))
		for j := range b {
			b[j] = []byte{0, 1, 'a', 0xff}[rng.IntN(4)]
		}
		return b
	}
	for range 20_000 {
		a, b := gen(), gen()
		off := rng.IntN(min(len(a), len(b)) + 1)
		copy(b[:off], a[:off]) // they share their first off bytes
		ka, kb := textKey(a, off), textKey(b, off)
		c := strings.Compare(string(a), string(b))
		switch {
		case ka < kb && c >= 0, ka > kb && c <= 0:
			t.Fatalf("%q and %q from %d: keys %x and %x, compared %d", a, b, off, ka, kb, c)
		case ka == kb && ka&0xff != 8 && c != 0:
			t.Fatalf("%q and %q from %d: equal keys %x of nothing more, compared %d", a, b, off, ka, c)
		}
	}
}
