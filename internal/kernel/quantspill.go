package kernel

import (
	"math"
	"slices"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// A quantile's values past its sink's budget (v0.6.3 step 220).
//
// A quantile or a median keeps every value of a group, and a group-by partitions
// keys, not the values of one, so past the budget it refused. Its answer needs two
// order statistics a group, which values sorted by (group, value) give in one pass.
// So when its sink would refuse, the accumulator hands its values to a ValueSpill,
// which the physical layer writes to disk and sorts, and keeps a count a group. At
// Finish it puts the rest, reads every value back in order, and takes each group's
// two values at their ranks, interpolated as quantileOf interpolates them: the order
// statistics of a total order do not depend on how they are found.
//
// Each spill is a run already in order. The accumulator sorts each group's values in
// place, which costs no memory, and puts the groups in turn, so the physical layer
// writes a run as it is given one and only merges them. A spill happens when the sink
// is over its budget, so each run is about a budget of values, as an external sort's
// are.

// ValueSpill holds values past a sink's budget, in runs, and gives them back ordered
// by group and then by compareTotalF64. The kernel touches no file.
type ValueSpill interface {
	// Put adds values to the open run, opening one if none is. Within a run they come
	// in order of group, then of compareTotalF64.
	Put(gids []int32, vals []float64) error
	// EndRun closes the open run, if there is one.
	EndRun() error
	// Ordered gives back every value put, merged across the runs.
	Ordered(fn func(gid int32, v float64) error) error
}

// ValueSpiller is an accumulator whose values can go to a ValueSpill.
type ValueSpiller interface {
	// SpillValues hands every value held to sp and lets them go, and reports whether
	// it held any.
	SpillValues(sp ValueSpill) (bool, error)
}

// spillChunk is how many values SpillValues puts at a time.
const spillChunk = 8192

// SpillValues hands every value held to sp and lets them go: see ValueSpiller.
func (a *quantileAcc) SpillValues(sp ValueSpill) (bool, error) {
	if a.sp != nil && a.sp != sp {
		return false, uerr.Internalf("kernel: a quantile's values spilled to two places")
	}
	a.cnt = Extend(a.cnt, len(a.vals), 0)
	gids := make([]int32, 0, spillChunk)
	vals := make([]float64, 0, spillChunk)
	flush := func() error {
		if len(gids) == 0 {
			return nil
		}
		err := sp.Put(gids, vals)
		gids, vals = gids[:0], vals[:0]
		return err
	}
	held := false
	for g, v := range a.vals {
		slices.SortFunc(v, compareTotalF64)
		for _, x := range v {
			gids, vals = append(gids, int32(g)), append(vals, x)
			if len(gids) == spillChunk {
				if err := flush(); err != nil {
					return false, err
				}
			}
		}
		if len(v) > 0 {
			held = true
			a.cnt[g] += len(v)
			a.vals[g] = nil
		}
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

// finishSpilled is Finish for a quantile whose values spilled: the rest are put too,
// and each group's answer taken from the values given back in order.
func (a *quantileAcc) finishSpilled(out []float64, seen []bool) error {
	if _, err := a.SpillValues(a.sp); err != nil {
		return err
	}
	a.cnt = Extend(a.cnt, len(out), 0)
	cur, at := int32(-1), 0
	var lo, hi int
	var frac, lv, hv float64
	done := func() {
		if cur < 0 {
			return
		}
		out[cur], seen[cur] = interpolate(lv, hv, frac, a.interp), true
	}
	err := a.sp.Ordered(func(g int32, v float64) error {
		if g != cur {
			done()
			if int(g) >= len(out) {
				return uerr.Internalf("kernel: a spilled quantile's group %d of %d", g, len(out))
			}
			cur, at = g, 0
			lo, hi, frac = ranks(a.cnt[g], a.q)
		}
		if at == lo {
			lv = v
		}
		if at == hi {
			hv = v
		}
		at++
		return nil
	})
	if err != nil {
		return err
	}
	done()
	return nil
}

// ranks are the two ranks a q-quantile of n values reads, and how far past the lower
// it lies: q·(n−1), the convention numpy, Polars and DuckDB share. quantileOf reads
// them too, so a spilled quantile and a held one take the same values.
func ranks(n int, q float64) (lo, hi int, frac float64) {
	if n <= 1 {
		return 0, 0, 0
	}
	pos := q * float64(n-1)
	lo = int(math.Floor(pos))
	hi = int(math.Ceil(pos))
	// Guard the ends against a float rank landing a hair outside the values.
	lo = max(0, min(lo, n-1))
	hi = max(0, min(hi, n-1))
	return lo, hi, pos - float64(lo)
}

// interpolate takes the quantile between a and b, the values at the two ranks.
func interpolate(a, b, frac float64, interp expr.Interpolation) float64 {
	switch interp {
	case expr.InterpLower:
		return a
	case expr.InterpHigher:
		return b
	case expr.InterpNearest:
		if frac < 0.5 {
			return a
		}
		return b
	case expr.InterpMidpoint:
		return midpoint(a, b)
	case expr.InterpLinear:
		return lerp(a, b, frac)
	}
	// Agg.Field refuses any other interpolation, and this has no error to return:
	// an assertion, recovered into ErrInternal. It was the linear arm, silently.
	panic(uerr.Internalf("kernel: interpolation %d reached the kernel", uint8(interp)))
}
