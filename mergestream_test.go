package ursus_test

// Step 182, scope item R1's first half: MergeSorted streams, a batch of each side at a
// time, where it materialised both.

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestMergeSortedStreamingAgreesWithAStableSort: random sorted runs with ties and
// nulls, merged at batch sizes that split them anywhere, against the stable sort of
// the left rows then the right: ties keep the left first, and nulls sort first.
func TestMergeSortedStreamingAgreesWithAStableSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(182, 1))
	run := func(n int, side string) ([]int64, []bool, []string) {
		k, ok, tag := make([]int64, n), make([]bool, n), make([]string, n)
		v := int64(0)
		nulls := 1 + rng.IntN(3) // one at least, so both sides' schemas are nullable alike
		for i := range n {
			tag[i] = side + string(rune('a'+i%26))
			if i < nulls {
				continue // a sorted run's nulls come first
			}
			v += rng.Int64N(3)
			k[i], ok[i] = v, true
		}
		return k, ok, tag
	}
	for _, sizes := range [][2]int{{0, 5}, {5, 0}, {1, 1}, {40, 33}, {200, 7}} {
		// An empty side is a non-empty one's Head(0), so its schema matches the other's.
		lk, lok, ltag := run(max(sizes[0], 1), "L")
		rk, rok, rtag := run(max(sizes[1], 1), "R")
		lk, lok, ltag = lk[:sizes[0]], lok[:sizes[0]], ltag[:sizes[0]]
		rk, rok, rtag = rk[:sizes[1]], rok[:sizes[1]], rtag[:sizes[1]]
		// The reference: left rows then right, stably sorted, nulls first.
		type row struct {
			k   int64
			ok  bool
			tag string
		}
		var want []row
		for i := range lk {
			want = append(want, row{lk[i], lok[i], ltag[i]})
		}
		for i := range rk {
			want = append(want, row{rk[i], rok[i], rtag[i]})
		}
		slices.SortStableFunc(want, func(a, b row) int {
			switch {
			case a.ok != b.ok:
				if !a.ok {
					return -1
				}
				return 1
			case a.k < b.k:
				return -1
			case a.k > b.k:
				return 1
			}
			return 0
		})
		wantTags := make([]string, len(want))
		for i, w := range want {
			wantTags[i] = w.tag
		}
		frame := func(k []int64, ok []bool, tag []string, n int) *ursus.LazyFrame {
			if n == 0 {
				return ursus.Frame(ursus.ValuesNullable("k", []int64{0}, []bool{false}), ursus.Values("tag", []string{"x"})).Head(0)
			}
			return ursus.Frame(ursus.ValuesNullable("k", k, ok), ursus.Values("tag", tag))
		}
		left, right := frame(lk, lok, ltag, sizes[0]), frame(rk, rok, rtag, sizes[1])
		for _, bs := range []int{1, 3, 7, 64, 4096} {
			df, err := left.MergeSorted(right, "k").Collect(t.Context(), ursus.WithBatchSize(bs))
			if err != nil {
				t.Fatal(err)
			}
			tags, err := df.Column[string]("tag")
			if err != nil {
				t.Fatal(err)
			}
			if got := tags.Values(); !slices.Equal(got, wantTags) {
				t.Fatalf("sizes %v, batch %d:\n got %v\nwant %v", sizes, bs, got, wantTags)
			}
		}
	}
}

// TestMergeSortedRefusesARowBackwardsAcrossBatches: the left side's second batch
// starts below where its first ended, which only a check across batches sees.
func TestMergeSortedRefusesARowBackwardsAcrossBatches(t *testing.T) {
	left := ursus.Frame(ursus.Values("k", []int64{1, 2, 3, 2, 4}))
	right := ursus.Frame(ursus.Values("k", []int64{0, 5}))
	_, err := left.MergeSorted(right, "k").Collect(t.Context(), ursus.WithBatchSize(3))
	if err == nil || !strings.Contains(err.Error(), "the left side is not sorted") {
		t.Fatalf("a run going back at a batch boundary: %v", err)
	}
}

// TestMergeSortedHoldsNoInput: it held both inputs, charged to the budget; now a
// batch a side, uncharged, so its peak is nothing beside the inputs' megabytes.
func TestMergeSortedHoldsNoInput(t *testing.T) {
	const n = 200_000
	a, b := make([]int64, n), make([]int64, n)
	for i := range n {
		a[i], b[i] = int64(2*i), int64(2*i+1)
	}
	var st ursus.MemoryStats
	df, err := ursus.Frame(ursus.Values("k", a)).MergeSorted(ursus.Frame(ursus.Values("k", b)), "k").
		Collect(t.Context(), ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2*n {
		t.Fatalf("%d rows", df.Height())
	}
	if st.Peak > 256<<10 {
		t.Errorf("a peak of %d bytes, where both inputs are %d", st.Peak, 2*n*8)
	}
}
