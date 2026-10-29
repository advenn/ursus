package kernel

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestParallelSortMatchesSerial is step 361's: ArgSortColumnsN on 2 to 7 workers
// gives ArgSortColumns' permutation exactly, ties included, by the radix passes and
// by the comparator's merge sort alike: integers of few values and of many, floats
// with NaN, infinities and both zeros, nulls first and last, ascending and
// descending, one key and three, strings, at sizes about every split.
func TestParallelSortMatchesSerial(t *testing.T) {
	defer func(m int) { parallelMin = m }(parallelMin)
	parallelMin = 64
	rng := rand.New(rand.NewPCG(361, 1))
	valid := func(n, stride int) bitmap.View {
		b := bitmap.NewBuilder(n)
		for i := range n {
			b.Append(stride == 0 || i%stride != 1)
		}
		return b.Finish()
	}
	for _, n := range []int{64, 65, 127, 1000, 4099, 70_000} {
		few := make([]int64, n)
		many := make([]int64, n)
		fs := make([]float64, n)
		ss := make([]string, n)
		u8 := make([]uint8, n)
		for i := range n {
			few[i] = int64(rng.IntN(9)) - 4
			many[i] = rng.Int64() >> uint(rng.IntN(40))
			if rng.IntN(2) == 0 {
				many[i] = -many[i]
			}
			fs[i] = float64(rng.IntN(13)) - 6
			ss[i] = fmt.Sprintf("k%03d", rng.IntN(150))
			u8[i] = uint8(rng.IntN(4))
		}
		fs[0], fs[1], fs[2], fs[n/2] = math.NaN(), math.Inf(-1), math.Inf(1), math.Copysign(0, -1)
		cols := map[string]*data.Column{
			"few":            data.NewFixed("few", dtype.Int64, few, bitmap.AllSet(n)),
			"many":           data.NewFixed("many", dtype.Int64, many, bitmap.AllSet(n)),
			"floats":         data.NewFixed("fs", dtype.Float64, fs, bitmap.AllSet(n)),
			"few with nulls": data.NewFixed("fewn", dtype.Int64, few, valid(n, 5)),
			"floats nulls":   data.NewFixed("fsn", dtype.Float64, fs, valid(n, 3)),
			"uint8":          data.NewFixed("u8", dtype.Uint8, u8, bitmap.AllSet(n)),
			"strings":        data.NewString("ss", ss, bitmap.AllSet(n)),
			"strings nulls":  data.NewString("ssn", ss, valid(n, 4)),
		}
		cases := [][]string{
			{"few"}, {"many"}, {"floats"}, {"few with nulls"}, {"floats nulls"}, {"uint8"},
			{"strings"}, {"strings nulls"},
			{"uint8", "floats nulls", "many"}, {"strings", "few"}, {"few with nulls", "strings nulls"},
		}
		for _, names := range cases {
			for _, sp := range []SortSpec{{}, {Descending: true}, {NullsLast: true}, {Descending: true, NullsLast: true}} {
				keys := make([]*data.Column, len(names))
				specs := make([]SortSpec, len(names))
				for i, name := range names {
					keys[i], specs[i] = cols[name], sp
					if i == 1 {
						specs[i].Descending = !sp.Descending
					}
				}
				want, err := ArgSortColumns(keys, specs, n)
				if err != nil {
					t.Fatal(err)
				}
				for _, w := range []int{2, 3, 4, 7} {
					got, err := ArgSortColumnsN(t.Context(), keys, specs, n, w)
					if err != nil {
						t.Fatal(err)
					}
					if !slices.Equal(got, want) {
						t.Fatalf("%v %+v, %d rows on %d workers: the permutation differs at %d",
							names, sp, n, w, firstDiff(got, want))
					}
				}
			}
		}
	}
}

// TestCoRankSplitsAMergeAsTheMergeDoes is the merge path's: every prefix of a merge
// of two sorted runs, ties among them, is the first co-rank's elements.
func TestCoRankSplitsAMergeAsTheMergeDoes(t *testing.T) {
	rng := rand.New(rand.NewPCG(361, 2))
	for range 300 {
		vals := make([]int, rng.IntN(40))
		for i := range vals {
			vals[i] = rng.IntN(6)
		}
		less := func(a, b int32) int { return vals[a] - vals[b] }
		idx := make([]int32, len(vals))
		for i := range idx {
			idx[i] = int32(i)
		}
		cut := 0
		if len(idx) > 0 {
			cut = rng.IntN(len(idx) + 1)
		}
		l, r := slices.Clone(idx[:cut]), slices.Clone(idx[cut:])
		slices.SortStableFunc(l, less)
		slices.SortStableFunc(r, less)
		whole := make([]int32, len(idx))
		merge(whole, l, r, less)
		for p := range len(whole) + 1 {
			i, j := coRank(p, l, r, less)
			got := make([]int32, p)
			merge(got, l[:i], r[:j], less)
			if !slices.Equal(got, whole[:p]) {
				t.Fatalf("%v and %v, a prefix of %d: %v, want %v", l, r, p, got, whole[:p])
			}
		}
	}
}

func firstDiff(a, b []int32) int {
	for i := range a {
		if a[i] != b[i] {
			return i
		}
	}
	return len(a)
}
