package kernel

import (
	"container/heap"
	"slices"
	"sort"

	"ursus/internal/data"
)

// ArgSort returns the permutation that orders n rows under cmp.
//
// The sort is STABLE, and the stability is load-bearing rather than a nicety:
//
//   - It makes a multi-pass sort (`Sort(a)` then `Sort(b)`) mean what people
//     expect — ties on b keep their a order.
//   - It makes a query's output reproducible when keys contain ties, which is what
//     `dataframe-features.md`'s determinism commitment requires.
//   - It is what lets ArgTopK be checked against ArgSort at all: without a defined
//     tie order there is no single right answer to compare against.
//
// It uses slices.SortStableFunc rather than sort.SliceStable, which is a typed
// sort over []int32 rather than a reflective one: sort.SliceStable swaps through
// reflectlite.Swapper, which the profile put at 5.4% of the engine's hot-path CPU
// on its own. Prefer ArgSortColumns where the key COLUMNS are available — it
// avoids comparisons entirely.
func ArgSort(n int, cmp Comparator) []int32 {
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = int32(i)
	}
	slices.SortStableFunc(idx, func(a, b int32) int {
		return cmp(int(a), int(b))
	})
	return idx
}

// ArgSortColumns is ArgSort given the key COLUMNS rather than a comparator, and
// is what every sort in the engine should use when it has them.
//
// Having the columns is what lets the sort key be computed ONCE PER ROW instead
// of once per comparison, which turns an O(n log n) problem with a four-closure
// inner loop into a handful of linear passes. See radix.go for the argument; the
// short version is that OrderKeyF64 was already producing exactly the key a radix
// sort wants, O(n log n) times.
//
// Falls back to the comparator for key types with no order-preserving fixed-width
// encoding — String, Int128 and so Decimal — and for any mixed set containing
// one. The result is identical either way, including tie order, which is what
// TestRadixMatchesComparatorSort exists to hold true.
func ArgSortColumns(cols []*data.Column, specs []SortSpec, n int) ([]int32, error) {
	if len(cols) == len(specs) && radixable(cols) {
		idx, err := argSortRadix(cols, specs, n)
		if err != nil {
			return nil, err
		}
		if idx != nil {
			return idx, nil
		}
	}
	cmp, err := NewComparator(cols, specs)
	if err != nil {
		return nil, err
	}
	return ArgSort(n, cmp), nil
}

// ArgTopK returns the indices of the k smallest rows under cmp, in order.
//
// # The contract
//
//	ArgTopK(n, k, cmp)  ==  ArgSort(n, cmp)[:min(k, n)]
//
// EXACTLY — the same indices, in the same order, including for ties. That
// equivalence is the whole point: it is what makes limit-pushdown
// (`Sort(...).Head(k)` → top-k) a legal rewrite rather than a silent change of
// which rows come back.
//
// # Why ties need explicit handling
//
// A bounded heap is not naturally stable, and getting this wrong is invisible on
// data without ties. Two independent bugs had to be fixed here:
//
//  1. EVICTION. Among equal elements the heap root is whichever element the heap
//     happened to sift there, not the one that should lose. With values
//     [5,5,1,5] and k=2 the naive version evicts index 0 and returns {2,1},
//     where ArgSort[:2] is {2,0} — a different row entirely.
//  2. THE FINAL SORT. Sorting the surviving indices with a stable sort is
//     meaningless, because they are in heap order rather than original order;
//     "stable" preserves an arbitrary permutation. [7,7,7,3] with k=3 returned
//     {3,1,2} against ArgSort[:3] = {3,0,1}.
//
// Both are fixed by making the comparator TOTAL: ties break by original index, in
// the heap (so the largest index sits at the root and is evicted first) and in the
// final sort (so the output order is the original order among equals).
//
// Cost is O(n log k) time and O(k) memory rather than O(n log n) and O(n). For
// `Sort(...).Head(10)` over ten million rows that is the difference between a sort
// and a scan.
func ArgTopK(n, k int, cmp Comparator) []int32 {
	if k <= 0 || n <= 0 {
		return nil
	}
	if k >= n {
		return ArgSort(n, cmp)
	}

	// total orders by cmp, then by original index. Making it total is what buys
	// the ArgSort equivalence.
	total := func(a, b int32) int {
		if r := cmp(int(a), int(b)); r != 0 {
			return r
		}
		return int(a) - int(b)
	}

	h := &topKHeap{less: total, idx: make([]int32, 0, k)}
	for i := range k {
		h.idx = append(h.idx, int32(i))
	}
	heap.Init(h) // O(k), against O(k log k) for k pushes

	for i := k; i < n; i++ {
		// h.idx[0] is the WORST of the current best k under the total order, so a
		// candidate that is not strictly better cannot belong. Most rows cost one
		// comparison.
		if total(int32(i), h.idx[0]) < 0 {
			h.idx[0] = int32(i)
			heap.Fix(h, 0)
		}
	}

	out := h.idx
	sort.Slice(out, func(a, b int) bool { return total(out[a], out[b]) < 0 })
	return out
}

// topKHeap is a MAX-heap under `less`: the root is the worst of the current best
// k, which is the element a better candidate displaces.
type topKHeap struct {
	less func(a, b int32) int
	idx  []int32
}

func (h *topKHeap) Len() int { return len(h.idx) }

func (h *topKHeap) Less(a, b int) bool {
	return h.less(h.idx[a], h.idx[b]) > 0 // reversed: max-heap
}

func (h *topKHeap) Swap(a, b int) { h.idx[a], h.idx[b] = h.idx[b], h.idx[a] }

func (h *topKHeap) Push(x any) { h.idx = append(h.idx, x.(int32)) }

func (h *topKHeap) Pop() any {
	old := h.idx
	n := len(old)
	v := old[n-1]
	h.idx = old[:n-1]
	return v
}
