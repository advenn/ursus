package kernel

import (
	"math"
	"math/bits"
	"sort"

	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/uerr"
)

// Spearman's pairs past its sink's budget (step 339).
//
// A Spearman's correlation keeps every pair of a group, as a quantile keeps every
// value, so over a few huge groups it refused. Its answer is Pearson's of the pairs'
// ranks, a tie the mean of the ranks it spans: Σ dx·dy over √(Σ dx²·Σ dy²), each d a
// rank less the mean rank. Doubled, a rank is an integer, 2·lo + t + 1 for a tie of
// t from lo, and so is each d, so the three sums are integers, summed exactly at 128
// bits: the answer does not depend on the order the pairs come in, and a spilled
// group's is a held one's to the bit.
//
// Spilled, the pairs go to a PairSpill as (y, x), sorted by group and y. Read back in
// that order, each tie of y is a run, ranked at its end: Σ dy² gets its share, and
// its pairs go on to a second spill as (x, y's doubled rank), which sorts them by
// group and x. Read back in that order, each tie of x is a run, and it adds its d
// times its pairs' Σ dy to Σ dx·dy, and its length times d² to Σ dx².
//
// A tie's pairs wait for its end in a buffer of spillChunk. Past that the tie is
// numbered instead, its pairs go on with the number, negated, and its rank is kept
// in a table of one entry for every spillChunk pairs at most.

// PairSpill holds pairs past a sink's budget, in runs, and gives them back ordered
// by group and then by the first of each pair, as compareTotalF64 orders it. The
// kernel touches no file.
type PairSpill interface {
	// Put adds pairs to the open run, opening one if none is. Within a run they come
	// in order of group, then of a; to a spill from Sorting, in any order.
	Put(gids []int32, a, b []float64) error
	// EndRun closes the open run, if there is one.
	EndRun() error
	// Ordered gives back every pair put, merged across the runs, and lets them go.
	Ordered(fn func(gid int32, a, b float64) error) error
	// Sorting is a second spill in the same place, which takes its pairs in any
	// order and sorts them into runs itself.
	Sorting() (PairSpill, error)
}

// PairSpiller is an accumulator whose pairs can go to a PairSpill.
type PairSpiller interface {
	// SpillPairs hands every pair held to sp and lets them go, and reports whether
	// it held any.
	SpillPairs(sp PairSpill) (bool, error)
}

// SpillPairs hands every pair held to sp, as (y, x) by group and y: see PairSpiller.
func (a *spearmanAcc) SpillPairs(sp PairSpill) (bool, error) {
	if a.sp != nil && a.sp != sp {
		return false, uerr.Internalf("kernel: a spearman's pairs spilled to two places")
	}
	a.cnt = Extend(a.cnt, len(a.xs), 0)
	gids := make([]int32, 0, spillChunk)
	ys := make([]float64, 0, spillChunk)
	xs := make([]float64, 0, spillChunk)
	flush := func() error {
		if len(gids) == 0 {
			return nil
		}
		err := sp.Put(gids, ys, xs)
		gids, ys, xs = gids[:0], ys[:0], xs[:0]
		return err
	}
	held := false
	for g := range a.xs {
		x, y := a.xs[g], a.ys[g]
		if len(x) == 0 {
			continue
		}
		sort.Sort(byFirst{y, x})
		for i := range y {
			gids, ys, xs = append(gids, int32(g)), append(ys, y[i]), append(xs, x[i])
			if len(gids) == spillChunk {
				if err := flush(); err != nil {
					return false, err
				}
			}
		}
		held = true
		a.cnt[g] += len(x)
		a.xs[g], a.ys[g] = nil, nil
	}
	if err := flush(); err != nil {
		return false, err
	}
	if !held {
		return false, nil
	}
	a.sp = sp
	return true, sp.EndRun()
}

// byFirst sorts two slices in step by the first, in place.
type byFirst struct{ k, v []float64 }

func (p byFirst) Len() int           { return len(p.k) }
func (p byFirst) Less(i, j int) bool { return compareTotalF64(p.k[i], p.k[j]) < 0 }
func (p byFirst) Swap(i, j int) {
	p.k[i], p.k[j] = p.k[j], p.k[i]
	p.v[i], p.v[j] = p.v[j], p.v[i]
}

// finishSpilled is Finish for a Spearman whose pairs spilled: the rest are put too,
// read back by y to rank y, then by x to rank x and sum.
func (a *spearmanAcc) finishSpilled(out []float64) error {
	if _, err := a.SpillPairs(a.sp); err != nil {
		return err
	}
	a.cnt = Extend(a.cnt, len(out), 0)
	byX, err := a.sp.Sorting()
	if err != nil {
		return err
	}
	syy := make([]wide, len(out))
	side, err := a.rankY(byX, syy)
	if err != nil {
		return err
	}
	for g := range out {
		out[g] = math.NaN()
	}
	var (
		cur      = int32(-1)
		pos, lo  int64
		key      uint64
		t, sdy   int64
		mean     int64 // the group's doubled mean rank, n + 1
		sxy, sxx wide
	)
	closeTie := func() {
		if t == 0 {
			return
		}
		d := 2*lo + t + 1 - mean
		sxy.addMul(d, sdy)
		sxx.addMul(t, d*d)
		t, sdy = 0, 0
	}
	closeGroup := func() {
		if cur < 0 {
			return
		}
		closeTie()
		if a.cnt[cur] >= 2 {
			out[cur] = correlation(sxy, sxx, syy[cur])
		}
		sxy, sxx = wide{}, wide{}
	}
	err = byX.Ordered(func(g int32, x, r float64) error {
		if int(g) >= len(out) {
			return uerr.Internalf("kernel: a spilled spearman's group %d of %d", g, len(out))
		}
		ry := int64(r)
		if ry < 0 {
			ry = side[-1-ry]
		}
		k := OrderKeyF64(x)
		switch {
		case g != cur:
			closeGroup()
			cur, pos, mean = g, 0, int64(a.cnt[g])+1
			lo, key = 0, k
		case k != key:
			closeTie()
			lo, key = pos, k
		}
		pos++
		t++
		sdy += ry - mean
		return nil
	})
	if err != nil {
		return err
	}
	closeGroup()
	return nil
}

// rankY reads the pairs back by group and y, sums each group's Σ dy² into syy, and
// puts each pair on to byX as (x, y's doubled rank), or a long tie's number, negated
// and less one: the table it returns holds those ties' ranks.
func (a *spearmanAcc) rankY(byX PairSpill, syy []wide) ([]int64, error) {
	var (
		cur     = int32(-1)
		pos, lo int64
		key     uint64
		buf     []float64 // the open tie's xs, while it is short
		long    = -1      // the open tie's number, once it is not
		side    []int64
		gids    = make([]int32, 0, spillChunk)
		xs, rs  = make([]float64, 0, spillChunk), make([]float64, 0, spillChunk)
	)
	put := func(g int32, x, r float64) error {
		gids, xs, rs = append(gids, g), append(xs, x), append(rs, r)
		if len(gids) < spillChunk {
			return nil
		}
		err := byX.Put(gids, xs, rs)
		gids, xs, rs = gids[:0], xs[:0], rs[:0]
		return err
	}
	closeTie := func() error {
		if cur < 0 || pos == lo {
			return nil
		}
		t := pos - lo
		r := 2*lo + t + 1
		d := r - (int64(a.cnt[cur]) + 1)
		syy[cur].addMul(t, d*d)
		if long >= 0 {
			side[long], long = r, -1
			return nil
		}
		for _, x := range buf {
			if err := put(cur, x, float64(r)); err != nil {
				return err
			}
		}
		buf = buf[:0]
		return nil
	}
	err := a.sp.Ordered(func(g int32, y, x float64) error {
		if int(g) >= len(syy) {
			return uerr.Internalf("kernel: a spilled spearman's group %d of %d", g, len(syy))
		}
		if k := OrderKeyF64(y); g != cur || k != key {
			if err := closeTie(); err != nil {
				return err
			}
			if g != cur {
				cur, pos = g, 0
			}
			lo, key = pos, k
		}
		pos++
		if long < 0 && len(buf) < spillChunk {
			buf = append(buf, x)
			return nil
		}
		if long < 0 {
			// The tie is long: numbered, and its pairs go on with the number.
			long = len(side)
			side = append(side, 0)
			for _, bx := range buf {
				if err := put(g, bx, float64(-1-long)); err != nil {
					return err
				}
			}
			buf = buf[:0]
		}
		return put(g, x, float64(-1-long))
	})
	if err != nil {
		return nil, err
	}
	if err := closeTie(); err != nil {
		return nil, err
	}
	if len(gids) > 0 {
		if err := byX.Put(gids, xs, rs); err != nil {
			return nil, err
		}
	}
	return side, byX.EndRun()
}

// wide is an exact sum of products of int64s, at 128 bits.
type wide i128.Int128

// addMul adds a·b.
func (w *wide) addMul(a, b int64) {
	ua, ub := uint64(a), uint64(b)
	if a < 0 {
		ua = -ua
	}
	if b < 0 {
		ub = -ub
	}
	hi, lo := bits.Mul64(ua, ub)
	if (a < 0) != (b < 0) {
		// Negated, two's complement across both halves.
		lo = ^lo + 1
		hi = ^hi
		if lo == 0 {
			hi++
		}
	}
	var c uint64
	w.Lo, c = bits.Add64(w.Lo, lo, 0)
	w.Hi += int64(hi + c)
}

// correlation is Σ dx·dy over √(Σ dx²·Σ dy²), each sum exact until its one rounding;
// NaN where either variable has a single value.
func correlation(sxy, sxx, syy wide) float64 {
	return i128.Int128(sxy).Float64() / math.Sqrt(i128.Int128(sxx).Float64()*i128.Int128(syy).Float64())
}
