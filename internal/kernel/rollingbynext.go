package kernel

import (
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// A rolling function by time over one partition, walked a batch at a time (v0.6.4
// step 246).
//
// rollingBy sorts a partition's rows by their by value and walks them once, each
// kernel carrying its state from one row to the next: a float window's compensated
// sum and Welford state, refreshed from the window every so many removals; an
// integer one's Int128 sum; min and max's deque; a quantile's sorted values. Over one
// huge partition the rows come already sorted, a batch at a time, from a sorted pass,
// and RollingByNext walks each batch on from where the last left off, holding the
// rows still in reach. So every add, removal and refresh is the one rollingBy makes,
// in its order, and the answers are the same to the bit.
//
// A window ends at its row's own by and takes the rows that tie with it, so a batch
// must hold the whole of every run of equal by values it holds part of: the caller
// cuts its batches between runs, and a run split between two is refused.

// RollingByState is a rolling function by time's walk, carried between batches. Its
// zero value is a partition's start. Positions count the rows walked, those whose by
// and pair are not null.
type RollingByState struct {
	next           int64 // rows walked so far
	pos            int64 // the position of the first row held
	from, to, l, h int64 // the kernels' from and to, and the window's ends
	count, removed int
	lastTick       int64
	walked         bool
	ticks          []int64 // each row held: its by, and whether its value is valid
	valid          []bool
	vals           []float64     // a float or quantile window's values
	ivals          []i128.Int128 // an integer sum's
	s              rollingState  // a float window's
	isum           i128.Int128   // an integer sum's
	sorted         []float64     // a quantile's
	deque          []int64       // min and max's, positions
	dat            []int         // min and max's: each deque row's place in vals0
	vals0          *data.Column  // min and max's values: the deque's, then from toBase on
	toBase, pendAt int64         // min and max: the position at pendAt in vals0
	kind           rollingByKind
}

type rollingByKind uint8

const (
	rbFloat rollingByKind = iota
	rbIntSum
	rbExtremum
	rbQuantile
)

// NBytes is what the state holds: a period's rows.
func (st *RollingByState) NBytes() int64 {
	n := int64(cap(st.ticks))*8 + int64(cap(st.valid)) + int64(cap(st.vals))*8 +
		int64(cap(st.ivals))*16 + int64(cap(st.sorted))*8 + int64(cap(st.deque))*8 + int64(cap(st.dat))*8
	if st.vals0 != nil {
		n += st.vals0.NBytes()
	}
	return n
}

// Held is how many rows the state holds.
func (st *RollingByState) Held() int { return len(st.ticks) }

// RollingByNext answers fn by time over pair, the next rows of a partition in their
// by order, continuing st. See RollingByState.
func RollingByNext(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	pair *data.Column, st *RollingByState) (*data.Column, error) {

	value, by, err := pairFields(pair)
	if err != nil {
		return nil, err
	}
	if value, err = readable(value); err != nil {
		return nil, err
	}
	ticks, err := readTicks(by)
	if err != nil {
		return nil, err
	}
	minN := int(params.MinSamples)
	if minN < 1 {
		return nil, uerr.Internalf("kernel: %s by time needing %d values", fn, minN)
	}
	kind := rbFloat
	switch {
	case fn == expr.WinRollingMin || fn == expr.WinRollingMax:
		kind = rbExtremum
	case fn == expr.WinRollingSum && out.Physical().ID() == dtype.TypeInt128:
		kind = rbIntSum
	case fn == expr.WinRollingMedian || fn == expr.WinRollingQuantile:
		kind = rbQuantile
	}
	if st.next == 0 {
		st.kind = kind
	} else if st.kind != kind {
		return nil, uerr.Internalf("kernel: a rolling walk by time changed its function")
	}

	// The rows a window walk takes: those whose pair and by are not null, as rollingBy.
	n := pair.Len()
	pv, bv, vv := pair.Validity(), by.Validity(), value.Validity()
	rows := make([]int32, 0, n)
	for i := range n {
		if !pv.Get(i) || !bv.Get(i) {
			continue
		}
		t := ticks[i]
		if st.walked && t < st.lastTick {
			return nil, uerr.Internalf("kernel: a rolling walk by time came out of its by order")
		}
		if st.walked && len(rows) == 0 && t == st.lastTick {
			return nil, uerr.Internalf("kernel: a run of equal by values was split between batches")
		}
		st.lastTick, st.walked = t, true
		rows = append(rows, int32(i))
	}
	starts, err := windowStarts(by.DType(), params.Period, ticks)
	if err != nil {
		return nil, err
	}

	// The batch's rows join those held.
	base := st.next
	for _, r := range rows {
		st.ticks = append(st.ticks, ticks[r])
		st.valid = append(st.valid, vv.Get(int(r)))
	}
	var cmp Comparator
	switch kind {
	case rbFloat, rbQuantile:
		f, err := toFloat64(value)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			st.vals = append(st.vals, f[r])
		}
	case rbIntSum:
		v, err := widenToInt128(value)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			st.ivals = append(st.ivals, v[r])
		}
	case rbExtremum:
		mine, err := Take(value, rows)
		if err != nil {
			return nil, err
		}
		if st.vals0 == nil {
			st.vals0, st.toBase, st.pendAt = mine, base, 0
		} else if st.vals0, err = ConcatColumns(value.Name(), []*data.Column{st.vals0, mine}); err != nil {
			return nil, err
		}
		if cmp, err = valueComparator(st.vals0); err != nil {
			return nil, err
		}
	}
	end := base + int64(len(rows))
	at := func(p int64) int { return int(p - st.pos) }
	// place is position p's row in vals0: a deque row's, or one from toBase on.
	place := func(p int64, k int) int {
		if k >= 0 {
			return st.dat[k]
		}
		return int(st.pendAt + p - st.toBase)
	}

	closed := params.Closed.Or(expr.ClosedRight)
	fres := make([]float64, n)
	ires := make([]i128.Int128, n)
	ok := make([]bool, n)
	sel := make([]int32, n)
	for i := range sel {
		sel[i] = NullIndex
	}
	q, interp := params.Q, params.Interp
	if fn == expr.WinRollingMedian {
		q, interp = 0.5, expr.InterpLinear
	}
	var failed error
	for _, r := range rows {
		// The window's ends, as rollingBy's win moves them.
		start, stop := starts[r], ticks[r]
		for st.l < end && (st.ticks[at(st.l)] < start || closed.LowerOpen() && st.ticks[at(st.l)] == start) {
			st.l++
		}
		st.h = max(st.h, st.l)
		for st.h < end && (st.ticks[at(st.h)] < stop || closed.UpperClosed() && st.ticks[at(st.h)] == stop) {
			st.h++
		}
		lo, hi := st.l, max(st.h, st.l)

		switch kind {
		case rbFloat:
			for ; st.to < hi; st.to++ {
				if k := at(st.to); st.valid[k] {
					st.s.add(st.vals[k])
					st.count++
				}
			}
			for ; st.from < lo; st.from++ {
				k := at(st.from)
				if !st.valid[k] {
					continue
				}
				st.s.remove(st.vals[k])
				st.count--
				// Afresh from the window, as rollingFloat does.
				if st.removed++; st.removed >= max(int(st.to-st.from-1), 1) {
					st.s = rollingState{}
					for p := st.from + 1; p < st.to; p++ {
						if k := at(p); st.valid[k] {
							st.s.add(st.vals[k])
						}
					}
					st.removed = 0
				}
			}
			if st.count < minN {
				continue
			}
			fres[r], ok[r] = rollingAnswer(fn, int(params.Ddof), &st.s)
		case rbIntSum:
			for ; st.to < hi; st.to++ {
				if k := at(st.to); st.valid[k] {
					st.isum, st.count = st.isum.Add(st.ivals[k]), st.count+1
				}
			}
			for ; st.from < lo; st.from++ {
				if k := at(st.from); st.valid[k] {
					st.isum, st.count = st.isum.Sub(st.ivals[k]), st.count-1
				}
			}
			if st.count >= minN {
				ires[r], ok[r] = st.isum, true
			}
		case rbQuantile:
			for ; st.to < hi; st.to++ {
				if k := at(st.to); st.valid[k] {
					v := st.vals[k]
					p, _ := slices.BinarySearchFunc(st.sorted, v, compareTotalF64)
					st.sorted = slices.Insert(st.sorted, p, v)
				}
			}
			for ; st.from < lo; st.from++ {
				if k := at(st.from); st.valid[k] {
					p, found := slices.BinarySearchFunc(st.sorted, st.vals[k], compareTotalF64)
					if !found {
						failed = uerr.Internalf("kernel: %s lost a value of its window", fn)
						break
					}
					st.sorted = slices.Delete(st.sorted, p, p+1)
				}
			}
			if failed != nil {
				return nil, failed
			}
			if m := len(st.sorted); m > 0 && m >= minN {
				l, h, frac := ranks(m, q)
				fres[r], ok[r] = interpolate(st.sorted[l], st.sorted[h], frac, interp), true
			}
		case rbExtremum:
			isMax := fn == expr.WinRollingMax
			for ; st.to < hi; st.to++ {
				if !st.valid[at(st.to)] {
					continue
				}
				st.count++
				cur := place(st.to, -1)
				for len(st.deque) > 0 {
					c := cmp(place(st.deque[len(st.deque)-1], len(st.deque)-1), cur)
					if (isMax && c > 0) || (!isMax && c < 0) {
						break
					}
					st.deque, st.dat = st.deque[:len(st.deque)-1], st.dat[:len(st.dat)-1]
				}
				st.deque, st.dat = append(st.deque, st.to), append(st.dat, cur)
			}
			for ; st.from < lo; st.from++ {
				if st.valid[at(st.from)] {
					st.count--
				}
			}
			for len(st.deque) > 0 && st.deque[0] < st.from {
				st.deque, st.dat = st.deque[1:], st.dat[1:]
			}
			if st.count >= minN {
				sel[r] = int32(st.dat[0])
			}
		}
	}
	st.next = end

	// Let go of the rows behind the window: none is reached again.
	if d := at(st.from); d > 0 {
		st.ticks, st.valid = shed(st.ticks, d), shed(st.valid, d)
		switch kind {
		case rbFloat, rbQuantile:
			st.vals = shed(st.vals, d)
		case rbIntSum:
			st.ivals = shed(st.ivals, d)
		}
		st.pos = st.from
	}

	switch kind {
	case rbIntSum:
		return data.NewFixed(name, out, ires, seenBitmap(ok, n)), nil
	case rbExtremum:
		res, err := Take(st.vals0, sel)
		if err != nil {
			return nil, err
		}
		// What is kept of the values: the deque's, then the rows from to on, not yet
		// in a window.
		keep := make([]int32, 0, len(st.deque)+int(end-st.to))
		for k := range st.deque {
			keep = append(keep, int32(st.dat[k]))
			st.dat[k] = k
		}
		for p := st.to; p < end; p++ {
			keep = append(keep, int32(place(p, -1)))
		}
		if st.vals0, err = Take(st.vals0, keep); err != nil {
			return nil, err
		}
		st.toBase, st.pendAt = st.to, int64(len(st.deque))
		return res.Rename(name), nil
	}
	return floatsAs(name, out, fres, seenBitmap(ok, n)), nil
}

// shed drops the first d of s, copying into a slice of its own size once most of the
// backing array is behind it.
func shed[T any](s []T, d int) []T {
	s = s[d:]
	if cap(s) > 2*len(s)+64 {
		s = append(make([]T, 0, len(s)), s...)
	}
	return s
}
