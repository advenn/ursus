package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// A rolling function over a window of rows, walked a batch at a time (step 429).
//
// rollingFloat, rollingMoment and rollingCovCorr carry their state from one row to
// the next: a float window's compensated sum and Welford state, refreshed from the
// window every N removals; the moments' queue of two stacks, which flips when its
// front empties; a pair's three float states. A window over one huge partition was
// answered from the N rows before the batch and the batch, each batch afresh, so
// the refreshes and the flips fell at other rows than in memory, and 35 to 52% of a
// var's rows differed in their last bits (feature-gaps.md row 58). RollingNext walks
// each batch on from where the last left off, holding the window's last rows, so
// every add, removal, refresh and flip is the in-memory kernel's, in its order, and
// the answers are the same to the bit.

// RollingSteps reports whether fn over a window of rows ending at each row is
// RollingNext's: the functions whose state is a float's.
func RollingSteps(fn expr.WinFnOp, params expr.WinParams, out dtype.DataType) bool {
	if params.By || params.Center {
		return false
	}
	switch fn {
	case expr.WinRollingSum:
		return out.Physical().ID() != dtype.TypeInt128
	case expr.WinRollingMean, expr.WinRollingVar, expr.WinRollingStd,
		expr.WinRollingSkew, expr.WinRollingKurtosis, expr.WinRollingCov, expr.WinRollingCorr:
		return true
	}
	return false
}

// RollingState is a window of rows' walk, carried between batches. Its zero value is
// a partition's start.
type RollingState struct {
	next           int64     // rows walked
	xs, ys         []float64 // the last N+1 rows' values, a ring by position: a pair's two
	valid          []bool
	count, removed int
	s, sy, sxy     rollingState // a float window's, or a pair's three
	q              momentQueue  // the moments'
	fn             expr.WinFnOp
}

// NBytes is what the state holds: a window's rows, and the moments' queue.
func (st *RollingState) NBytes() int64 {
	return int64(cap(st.xs)+cap(st.ys))*8 + int64(cap(st.valid)) +
		int64(cap(st.q.front))*40 + int64(cap(st.q.back))*8
}

// RollingNext answers fn over the window of params.N rows ending at each of col's
// rows, the next rows of a partition, continuing st. A pair's col is its struct.
func RollingNext(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, st *RollingState) (*data.Column, error) {

	size, minN := int(params.N), int(params.MinSamples)
	if minN == 0 {
		minN = size
	}
	if size < 1 || minN < 1 || minN > size || !RollingSteps(fn, params, out) {
		return nil, uerr.Internalf("kernel: %s walked over %d rows needing %d", fn, size, minN)
	}
	if st.next == 0 {
		st.fn = fn
		st.xs, st.valid = make([]float64, size+1), make([]bool, size+1)
	} else if st.fn != fn || len(st.xs) != size+1 {
		return nil, uerr.Internalf("kernel: a rolling walk changed its function")
	}
	pair := fn == expr.WinRollingCov || fn == expr.WinRollingCorr
	var xs, ys []float64
	var valid func(i int) bool
	if pair {
		x, y, err := pairFields(col)
		if err != nil {
			return nil, err
		}
		if x, err = readable(x); err != nil {
			return nil, err
		}
		if y, err = readable(y); err != nil {
			return nil, err
		}
		if xs, err = toFloat64(x); err != nil {
			return nil, err
		}
		if ys, err = toFloat64(y); err != nil {
			return nil, err
		}
		if st.ys == nil {
			st.ys = make([]float64, size+1)
		}
		pv, xv, yv := col.Validity(), x.Validity(), y.Validity()
		valid = func(i int) bool { return pv.Get(i) && xv.Get(i) && yv.Get(i) }
	} else {
		c, err := readable(col)
		if err != nil {
			return nil, err
		}
		if xs, err = toFloat64(c); err != nil {
			return nil, err
		}
		v := c.Validity()
		valid = v.Get
	}

	ring := int64(size + 1)
	n := col.Len()
	res := make([]float64, n)
	ok := make([]bool, n)
	for i := range n {
		p := st.next
		st.next++
		at := int(p % ring)
		st.xs[at], st.valid[at] = xs[i], valid(i)
		if pair {
			st.ys[at] = ys[i]
		}
		switch fn {
		case expr.WinRollingSkew, expr.WinRollingKurtosis:
			res[i], ok[i] = st.moment(fn, params, p, size, minN)
		case expr.WinRollingCov, expr.WinRollingCorr:
			res[i], ok[i] = st.pairStep(fn, params, p, size, minN)
		default:
			res[i], ok[i] = st.float(fn, int(params.Ddof), p, size, minN)
		}
	}
	return floatsOut(name, out, res, ok), nil
}

// float is rollingFloat's step at row p, its value in the ring: it enters, then the
// row N before it leaves, and every N removals the state is the window's afresh.
func (st *RollingState) float(fn expr.WinFnOp, ddof int, p int64, size, minN int) (float64, bool) {
	ring := int64(size + 1)
	if at := int(p % ring); st.valid[at] {
		st.s.add(st.xs[at])
		st.count++
	}
	if old := p - int64(size); old >= 0 {
		if at := int(old % ring); st.valid[at] {
			st.s.remove(st.xs[at])
			st.count--
			if st.removed++; st.removed >= size {
				st.s = rollingState{}
				for q := old + 1; q <= p; q++ {
					if at := int(q % ring); st.valid[at] {
						st.s.add(st.xs[at])
					}
				}
				st.removed = 0
			}
		}
	}
	if st.count < minN {
		return 0, false
	}
	return rollingAnswer(fn, ddof, &st.s)
}

// moment is rollingMoment's step at row p: the row N before it pops, then it pushes;
// a window of one row starts afresh each row.
func (st *RollingState) moment(fn expr.WinFnOp, params expr.WinParams, p int64, size, minN int) (float64, bool) {
	ring := int64(size + 1)
	if size == 1 {
		st.q.reset()
	}
	if old := p - int64(size); old >= 0 && size > 1 {
		if at := int(old % ring); st.valid[at] {
			st.q.pop(st.xs[at])
		}
	}
	if at := int(p % ring); st.valid[at] {
		st.q.push(st.xs[at])
	}
	if st.q.len() < minN {
		return 0, false
	}
	return momentAnswer(fn, params, &st.q)
}

// pairStep is rollingCovCorr's step at row p, as float's is with its three states.
func (st *RollingState) pairStep(fn expr.WinFnOp, params expr.WinParams, p int64, size, minN int) (float64, bool) {
	ring := int64(size + 1)
	add := func(at int) {
		st.s.add(st.xs[at])
		st.sy.add(st.ys[at])
		st.sxy.add(float64(st.xs[at] * st.ys[at]))
	}
	if at := int(p % ring); st.valid[at] {
		add(at)
		st.count++
	}
	if old := p - int64(size); old >= 0 {
		if at := int(old % ring); st.valid[at] {
			st.s.remove(st.xs[at])
			st.sy.remove(st.ys[at])
			st.sxy.remove(float64(st.xs[at] * st.ys[at]))
			st.count--
			if st.removed++; st.removed >= size {
				st.s, st.sy, st.sxy = rollingState{}, rollingState{}, rollingState{}
				for q := old + 1; q <= p; q++ {
					if at := int(q % ring); st.valid[at] {
						add(at)
					}
				}
				st.removed = 0
			}
		}
	}
	if st.count < minN {
		return 0, false
	}
	return covCorrAnswer(fn, params, st.count, &st.s, &st.sy, &st.sxy)
}
