package kernel

import (
	"encoding/binary"
	"slices"
	"sync"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// A sort of text by its bytes, seven at a time (step 363). A String key went to the
// comparator: Go's stable sort, whose in-place merge rotates, and two closures and
// two offsets read a comparison. A million distinct strings of eight bytes took a
// second and more where Polars takes 42 to 64 ms.
//
// Each string's key is seven of its bytes, big-endian and padded with zeros, over
// how many bytes it has there, at most eight, in the low byte. Keys order as their
// strings do: two strings of equal bytes there differ by how many they have, and the
// shorter, a prefix of the longer, comes first. Two equal keys whose low byte is
// below eight are equal strings. Only strings of equal keys whose low byte is eight
// have more to compare, and those are sorted again by their next seven bytes.
//
// Each round is a stable sort of the keys, a radix sort or, of a few strings, an
// insertion sort, and a run of equal keys keeps the order it came in, so the sort is
// the comparator's stable sort exactly, ties in their order. No two strings are ever
// compared.

// textSmall is the most strings whose keys are sorted by insertion, not by radix.
const textSmall = 32

// textKey is s's key from off, which len(s) is at least.
func textKey(s []byte, off int) uint64 {
	t := s[off:]
	if len(t) >= 8 {
		return binary.BigEndian.Uint64(t)&^0xff | 8
	}
	// A byte at a time: a copy of so few bytes is a call to memmove, which was a
	// fifth of a sort of a million strings of eight bytes, their last byte this.
	k := uint64(len(t))
	for i, c := range t {
		k |= uint64(c) << (56 - 8*i)
	}
	return k
}

// textOrder is a column's text, and the direction it is sorted in.
type textOrder struct {
	acc  data.StringAccessor
	desc bool
}

// key is row's key from off, inverted for a descending sort, which reverses its
// order and leaves equal keys equal.
func (t textOrder) key(row int32, off int) uint64 {
	k := textKey(t.acc.Bytes(int(row)), off)
	if t.desc {
		return ^k
	}
	return k
}

// more says whether strings of key k have bytes past it to compare.
func (t textOrder) more(k uint64) bool {
	if t.desc {
		k = ^k
	}
	return k&0xff == 8
}

// sortFewTexts is sortText for at most textSmall rows: their keys sorted by
// insertion, which is stable, and the rows of each key with more to compare sorted
// again from their next seven bytes.
func (r *radixSorter) sortFewTexts(t textOrder, rows []int32, off int) {
	var keys [textSmall]uint64
	m := len(rows)
	for i, row := range rows {
		keys[i] = t.key(row, off)
	}
	for i := 1; i < m; i++ {
		k, row := keys[i], rows[i]
		j := i
		for ; j > 0 && keys[j-1] > k; j-- {
			keys[j], rows[j] = keys[j-1], rows[j-1]
		}
		keys[j], rows[j] = k, row
	}
	for lo := 0; lo < m; {
		hi := lo + 1
		for hi < m && keys[hi] == keys[lo] {
			hi++
		}
		if hi-lo > 1 && t.more(keys[lo]) {
			r.sortFewTexts(t, rows[lo:hi], off+7)
		}
		lo = hi
	}
}

// textPass is sortPass for a column of text, a String's, a Binary's or a
// Categorical's, compared as bytes, as the comparator compares them.
func (r *radixSorter) textPass(idx []int32, c *data.Column, spec SortSpec) {
	t := textOrder{acc: c.Strings(), desc: spec.Descending}
	if c.NullCount() == 0 {
		r.sortText(t, idx, 0)
		return
	}
	r.needNullBuffers(len(idx))
	valid := c.Validity()
	r.nonNull, r.nulls = r.nonNull[:0], r.nulls[:0]
	for _, row := range idx {
		if valid.Get(int(row)) {
			r.nonNull = append(r.nonNull, row)
		} else {
			r.nulls = append(r.nulls, row)
		}
	}
	r.sortText(t, r.nonNull, 0)
	out := r.out[:0]
	if spec.NullsLast {
		out = append(append(out, r.nonNull...), r.nulls...)
	} else {
		out = append(append(out, r.nulls...), r.nonNull...)
	}
	r.out = out[:0]
	copy(idx, out)
}

// sortText sorts rows in place, stably, by their strings past the first off bytes,
// which they share, on r.workers past parallelMin rows.
func (r *radixSorter) sortText(t textOrder, rows []int32, off int) {
	m := len(rows)
	if m <= textSmall {
		r.sortFewTexts(t, rows, off)
		return
	}
	workers := r.workers
	if m < parallelMin {
		workers = 1
	}
	keys := make([]uint64, m)
	ranges(m, workers, func(_, lo, hi int) {
		for i := lo; i < hi; i++ {
			keys[i] = t.key(rows[i], off)
		}
	})
	if !slices.ContainsFunc(keys, func(k uint64) bool { return k != keys[0] }) {
		// One key, as a prefix every string shares gives: nothing to move, and only
		// the next bytes to sort by, if there are any.
		if t.more(keys[0]) {
			r.sortText(t, rows, off+7)
		}
		return
	}
	pos, spare := make([]int32, m), make([]int32, m)
	for i := range pos {
		pos[i] = int32(i)
	}
	sub := &radixSorter{workers: workers}
	sorted, sk := sub.radixSorted(pos, spare, keys)
	// Each place of the order is read before it is written, so the rows take their
	// new order in the order's own buffer.
	ranges(m, workers, func(_, lo, hi int) {
		for i := lo; i < hi; i++ {
			sorted[i] = rows[sorted[i]]
		}
	})
	copy(rows, sorted)

	// The runs of one key with more to compare, sorted from the next seven bytes.
	var runs [][2]int
	for lo := 0; lo < m; {
		hi := lo + 1
		for hi < m && sk[hi] == sk[lo] {
			hi++
		}
		if hi-lo > 1 && t.more(sk[lo]) {
			runs = append(runs, [2]int{lo, hi})
		}
		lo = hi
	}
	if workers <= 1 {
		for _, run := range runs {
			r.sortText(t, rows[run[0]:run[1]], off+7)
		}
		return
	}
	// A run of parallelMin strings or more is split across the workers itself; the
	// rest are shared out, a piece of them a worker.
	var small [][2]int
	for _, run := range runs {
		if run[1]-run[0] >= parallelMin {
			r.sortText(t, rows[run[0]:run[1]], off+7)
		} else {
			small = append(small, run)
		}
	}
	var (
		wg    sync.WaitGroup
		relay uerr.Relay
	)
	for w := range workers {
		lo, hi := w*len(small)/workers, (w+1)*len(small)/workers
		relay.Go(&wg, func() {
			serial := &radixSorter{}
			for _, run := range small[lo:hi] {
				serial.sortText(t, rows[run[0]:run[1]], off+7)
			}
		})
	}
	wg.Wait()
	relay.Raise()
}
