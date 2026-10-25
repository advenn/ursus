package kernel

import (
	"slices"
	"sync"

	"github.com/advenn/ursus/internal/data"
)

// Parallel sorting (step 361). Each piece gives exactly the permutation its serial
// twin gives, ties included: the radix passes distribute a chunk at a time into
// ranges that keep the chunks' order, and the merge sort breaks a tie towards the
// left run, which holds the lower rows. A sort of a million distinct integers ran
// on one core, 100 ms where Polars, on all of them, takes 10.

// parallelMin is the fewest rows a sort is split across workers for: below it the
// goroutines cost more than they save. A variable, so a test can put a split at
// every size.
var parallelMin = 1 << 16

// ranges runs fn on workers contiguous pieces of [0, n), w numbering each, on a
// goroutine apiece, and waits; on one worker or few rows it runs in the caller.
func ranges(n, workers int, fn func(w, lo, hi int)) {
	if workers <= 1 || n < 2*workers {
		fn(0, 0, n)
		return
	}
	var wg sync.WaitGroup
	for w := range workers {
		lo, hi := w*n/workers, (w+1)*n/workers
		wg.Go(func() { fn(w, lo, hi) })
	}
	wg.Wait()
}

// ArgSortColumnsN is ArgSortColumns on up to workers goroutines: the same
// permutation, ties included, the radix passes and the comparator sort each split.
func ArgSortColumnsN(cols []*data.Column, specs []SortSpec, n, workers int) ([]int32, error) {
	if workers <= 1 || n < parallelMin {
		return ArgSortColumns(cols, specs, n)
	}
	if len(cols) == len(specs) && radixable(cols) {
		idx, err := argSortRadixN(cols, specs, n, workers)
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
	return ArgSortN(n, cmp, workers), nil
}

// argSortRadixN is argSortRadix with each byte pass split across workers.
func argSortRadixN(cols []*data.Column, specs []SortSpec, n, workers int) ([]int32, error) {
	r := &radixSorter{scratch: make([]int32, n), workers: workers}
	return r.argSort(cols, specs, n)
}

// radixSortedN is radixSorted on r.workers goroutines. The keys are gathered into
// src's order, and each round distributes chunk w of the positions into the
// buckets' ranges after chunks 0 to w-1's, so a bucket holds its rows in the order
// they were read, as one goroutine's pass leaves them.
func (r *radixSorter) radixSortedN(src, dst []int32, keys []uint64) ([]int32, []uint64) {
	n, workers := len(src), r.workers
	ka, kb := r.keyBuf(n), keys[:n]
	ranges(n, workers, func(_, lo, hi int) {
		for i := lo; i < hi; i++ {
			ka[i] = keys[src[i]]
		}
	})

	// Every byte's histogram of every chunk, in one pass: the first round's chunk
	// histograms are these, and their sum says which bytes take one value.
	hists := make([][8][256]int32, workers)
	ranges(n, workers, func(w, lo, hi int) {
		h := &hists[w]
		for _, k := range ka[lo:hi] {
			for b := range 8 {
				h[b][byte(k>>(8*b))]++
			}
		}
	})
	var total [8][256]int32
	for w := range hists {
		for b := range 8 {
			for v := range 256 {
				total[b][v] += hists[w][b][v]
			}
		}
	}

	first := true
	chunk := make([][256]int32, workers)
	for b := range 8 {
		if total[b][byte(ka[0]>>(8*b))] == int32(n) {
			continue // a byte of one value sorts nothing, as in radixSorted
		}
		// The chunks' histograms of this byte in the present order: the first
		// round's are the first pass's, and a later round's are counted again.
		if first {
			for w := range workers {
				chunk[w] = hists[w][b]
			}
		} else {
			ranges(n, workers, func(w, lo, hi int) {
				h := &chunk[w]
				*h = [256]int32{}
				for _, k := range ka[lo:hi] {
					h[byte(k>>(8*b))]++
				}
			})
		}
		first = false
		// Chunk w's first place in bucket v: the buckets below v, then chunks before
		// w in v.
		sum := int32(0)
		for v := range 256 {
			for w := range workers {
				c := chunk[w][v]
				chunk[w][v] = sum
				sum += c
			}
		}
		ranges(n, workers, func(w, lo, hi int) {
			off := &chunk[w]
			for i := lo; i < hi; i++ {
				d := byte(ka[i] >> (8 * b))
				dst[off[d]] = src[i]
				kb[off[d]] = ka[i]
				off[d]++
			}
		})
		src, dst = dst, src
		ka, kb = kb, ka
	}
	return src, ka
}

// ArgSortN is ArgSort on up to workers goroutines: each worker stably sorts a run
// of the rows, and runs are merged in pairs, each merge split across workers by
// merge path, a tie taken from the left run, whose rows come first. The permutation
// is ArgSort's exactly. cmp must be safe for concurrent use, as a comparator over
// columns, which only reads them, is.
func ArgSortN(n int, cmp Comparator, workers int) []int32 {
	if workers <= 1 || n < parallelMin {
		return ArgSort(n, cmp)
	}
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = int32(i)
	}
	less := func(a, b int32) int { return cmp(int(a), int(b)) }
	bounds := make([]int, workers+1)
	for w := range bounds {
		bounds[w] = w * n / workers
	}
	ranges(n, workers, func(w, lo, hi int) { slices.SortStableFunc(idx[lo:hi], less) })

	tmp := make([]int32, n)
	for len(bounds) > 2 {
		next := []int{0}
		for i := 0; i+2 < len(bounds); i += 2 {
			lo, mid, hi := bounds[i], bounds[i+1], bounds[i+2]
			mergeN(tmp[lo:hi], idx[lo:mid], idx[mid:hi], less, workers)
			next = append(next, hi)
		}
		if len(bounds)%2 == 0 {
			// An odd run out: it is copied across as it is.
			lo := bounds[len(bounds)-2]
			copy(tmp[lo:], idx[lo:])
			next = append(next, n)
		}
		idx, tmp, bounds = tmp, idx, next
	}
	return idx
}

// mergeN merges the sorted runs l and r into dst, a tie taken from l, on up to
// workers goroutines: dst is cut into pieces, and each piece's start is found in l
// and r by binary search, merge path's co-rank.
func mergeN(dst, l, r []int32, less func(a, b int32) int, workers int) {
	n := len(dst)
	ranges(n, workers, func(_, lo, hi int) {
		i, j := coRank(lo, l, r, less)
		k, _ := coRank(hi, l, r, less)
		merge(dst[lo:hi], l[i:k], r[j:hi-k], less)
	})
}

// coRank is how many of the first p elements of l and r merged, a tie taken from l,
// come from l, and from r.
func coRank(p int, l, r []int32, less func(a, b int32) int) (int, int) {
	lo, hi := max(0, p-len(r)), min(p, len(l))
	for lo < hi {
		i := (lo + hi) / 2 // l[:i] and r[:p-i] are a candidate prefix
		j := p - i
		// l[i] belongs in the prefix if it comes before r[j-1]: strictly less, since
		// a tie goes to l.
		if j > 0 && less(l[i], r[j-1]) <= 0 {
			lo = i + 1
		} else {
			hi = i
		}
	}
	return lo, p - lo
}

// merge merges l and r, sorted, into dst, of their lengths' sum, a tie taken from l.
func merge(dst, l, r []int32, less func(a, b int32) int) {
	i, j, k := 0, 0, 0
	for i < len(l) && j < len(r) {
		if less(r[j], l[i]) < 0 {
			dst[k] = r[j]
			j++
		} else {
			dst[k] = l[i]
			i++
		}
		k++
	}
	k += copy(dst[k:], l[i:])
	copy(dst[k:], r[j:])
}
