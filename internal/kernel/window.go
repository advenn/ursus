package kernel

import (
	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// Ordered window kernels: rank, running reductions, and shift.
//
// # A second family, not more Accumulators
//
// kernel.Accumulator's Finish(name, nGroups) returns ONE ROW PER GROUP. These
// return one row per input row, so they cannot be accumulators however the
// interface is stretched. They take a permutation instead: the rows of the frame,
// reordered partition-major and ordered within each partition, plus where each
// partition starts and ends.
//
// # Everything here writes into ORIGINAL row positions
//
// The permutation is read, never applied to the output. `out[perm[j]] = …` is the
// shape of every loop below, which is what makes MapGroupsToRows free — there is no
// inverse permutation to compute, because the answer is written where it belongs the
// first time.

// Segments is a partitioned ordering over a frame.
//
// Perm holds original row indices, arranged partition-major and ordered within each
// partition. Partition p occupies Perm[Bounds[p]:Bounds[p+1]], so Bounds has one
// more entry than there are partitions.
type Segments struct {
	Perm   []int32
	Bounds []int32
}

// WinCall applies an ordered window function.
//
// col is the function's input in ORIGINAL row order — Perm indexes into it. fill is
// Shift's replacement for vacated rows, or nil for null.
func WinCall(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, fill *data.Column, seg Segments) (*data.Column, error) {

	switch {
	case fn == expr.WinRank:
		return winRank(params, name, out, col, seg)
	case fn == expr.WinCumCount:
		return winCumCount(params, name, seg, col.Len())
	case fn == expr.WinShift:
		return winShift(params, name, col, fill, seg)
	case fn == expr.WinForwardFill || fn == expr.WinBackwardFill:
		return winFill(fn, params, name, col, seg)
	case fn.IsCumulative():
		return winCumulative(fn, params, name, out, col, seg)
	default:
		return nil, uerr.Internalf("kernel: no window kernel for %s", fn)
	}
}

// winCumCount numbers the rows of each partition from 1.
//
// One-based, not zero-based, because it is a COUNT — "how many rows up to and
// including this one" — and because IsFirstDistinct reads it as `== 1`, which says
// what it means.
func winCumCount(params expr.WinParams, name string, seg Segments, n int) (*data.Column, error) {
	vals := make([]uint32, n)
	for p := 0; p+1 < len(seg.Bounds); p++ {
		lo, hi := seg.Bounds[p], seg.Bounds[p+1]
		for j := lo; j < hi; j++ {
			if params.Reverse {
				vals[seg.Perm[j]] = uint32(hi - j)
			} else {
				vals[seg.Perm[j]] = uint32(j - lo + 1)
			}
		}
	}
	// Never null: every row occupies a position whether or not it holds a value.
	return data.NewFixed(name, dtype.Uint32, vals, bitmap.AllSet(n)), nil
}

// winRank numbers rows by VALUE within each partition.
//
// The ordering is the input column's own, not the window's OrderBy: `Col("speed").
// Rank(...).Over(Col("type"))` ranks by speed. The window's OrderBy governs the
// cumulative functions and Shift; rank brings its own, which is why the caller sorts
// by the child column for this function and by OrderBy for the others.
//
// Ties are peer groups: consecutive rows in the sorted partition that compare equal.
// Which rank a peer group receives is the whole content of RankMethod.
func winRank(params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments) (*data.Column, error) {

	cmp, err := valueComparator(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	n := col.Len()

	// peers reports whether two ORIGINAL row indices tie. Two nulls tie with each
	// other and with nothing else — the grouping equality the rest of the engine
	// uses, rather than IEEE's, so rank agrees with sort about what is equal.
	peers := func(a, b int32) bool {
		av, bv := valid.Get(int(a)), valid.Get(int(b))
		if !av || !bv {
			return av == bv
		}
		return cmp(int(a), int(b)) == 0
	}

	var iv []uint32
	var fv []float64
	if out.ID() == dtype.TypeFloat64 {
		fv = make([]float64, n)
	} else {
		iv = make([]uint32, n)
	}

	for p := 0; p+1 < len(seg.Bounds); p++ {
		lo, hi := int(seg.Bounds[p]), int(seg.Bounds[p+1])
		dense := uint32(0)
		j := lo
		for j < hi {
			// Extend the peer group.
			k := j + 1
			for k < hi && peers(seg.Perm[k], seg.Perm[j]) {
				k++
			}
			dense++
			first := uint32(j - lo + 1) // 1-based position of the group's first row
			last := uint32(k - lo)      // 1-based position of its last row

			for t := j; t < k; t++ {
				row := seg.Perm[t]
				switch params.Method {
				case expr.RankOrdinal:
					iv[row] = uint32(t - lo + 1)
				case expr.RankDense:
					iv[row] = dense
				case expr.RankMin:
					iv[row] = first
				case expr.RankMax:
					iv[row] = last
				default: // RankAverage
					fv[row] = float64(first+last) / 2
				}
			}
			j = k
		}
	}

	// Never null: a null value still has a rank, because it still has a position.
	if out.ID() == dtype.TypeFloat64 {
		return data.NewFixed(name, dtype.Float64, fv, bitmap.AllSet(n)), nil
	}
	return data.NewFixed(name, dtype.Uint32, iv, bitmap.AllSet(n)), nil
}

// winShift moves values within each partition, filling from outside with null.
//
// It is a gather, so it works for every type Take handles — including strings and
// Int128 — with no per-type code. NullIndex is what makes the fill free: Take
// already turns it into a null.
func winShift(params expr.WinParams, name string, col, fill *data.Column,
	seg Segments) (*data.Column, error) {

	n := col.Len()
	sel := make([]int32, n)
	off := int(params.N)

	for p := 0; p+1 < len(seg.Bounds); p++ {
		lo, hi := int(seg.Bounds[p]), int(seg.Bounds[p+1])
		for j := lo; j < hi; j++ {
			src := j - off
			if src < lo || src >= hi {
				// Outside this partition. NOT the neighbouring partition's value:
				// a window never reads across a partition boundary.
				sel[seg.Perm[j]] = NullIndex
				continue
			}
			sel[seg.Perm[j]] = seg.Perm[src]
		}
	}

	shifted, err := Take(col, sel)
	if err != nil {
		return nil, err
	}
	shifted = shifted.Rename(name)
	if fill == nil {
		return shifted, nil
	}

	// A fill value replaces exactly the rows that came from outside the partition —
	// not rows that were null to begin with. The mask is built from the gather, not
	// from the result's validity, which is what keeps those two cases apart.
	bits := bitmap.NewBuilder(n)
	for i := range n {
		bits.Append(sel[i] == NullIndex)
	}
	fromOutside := data.NewBool("__shifted_in", bits.Finish(), bitmap.AllSet(n))
	return Select(name, fromOutside, fill, shifted)
}

// winCumulative is the running-reduction family.
//
// # Null handling, which is the decision worth reading
//
// A null is SKIPPED by the running value and PRESERVED in the output: cum_sum of
// [1, null, 3] is [1, null, 4]. Not [1, 1, 4] — that would claim a value where there
// was none — and not [1, null, null], which is what treating null as absorbing would
// give and would make one missing reading destroy the rest of the series.
func winCumulative(fn expr.WinFnOp, params expr.WinParams, name string,
	out dtype.DataType, col *data.Column, seg Segments) (*data.Column, error) {

	switch fn {
	case expr.WinCumMin, expr.WinCumMax:
		return winCumExtremum(fn, params, name, col, seg)
	}

	// Sum and product accumulate arithmetically, so they need a value type rather
	// than a row index. Int128 for integer sums — the width Sum already uses, so the
	// last row of cum_sum equals sum — and Float64 for everything else.
	if out.ID() == dtype.TypeInt128 {
		return winCumInt128(params, name, out, col, seg)
	}
	return winCumFloat(fn, params, name, out, col, seg)
}

func winCumInt128(params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments) (*data.Column, error) {

	src, err := widenToInt128(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	n := col.Len()
	vals := make([]i128.Int128, n)
	ok := make([]bool, n)

	forEachOrdered(seg, params.Reverse, func(rows []int32) {
		var acc i128.Int128
		for _, row := range rows {
			if !valid.Get(int(row)) {
				continue // skipped by the running total, still null in the output
			}
			acc = acc.Add(src[row])
			vals[row] = acc
			ok[row] = true
		}
	})
	return data.NewFixed(name, out, vals, seenBitmap(ok, n)), nil
}

func winCumFloat(fn expr.WinFnOp, params expr.WinParams, name string,
	out dtype.DataType, col *data.Column, seg Segments) (*data.Column, error) {

	src, err := widenFloat(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	n := col.Len()
	vals := make([]float64, n)
	ok := make([]bool, n)
	prod := fn == expr.WinCumProd

	forEachOrdered(seg, params.Reverse, func(rows []int32) {
		acc := 0.0
		if prod {
			acc = 1
		}
		for _, row := range rows {
			if !valid.Get(int(row)) {
				continue
			}
			if prod {
				acc *= src[row]
			} else {
				acc += src[row]
			}
			vals[row] = acc
			ok[row] = true
		}
	})

	valid2 := seenBitmap(ok, n)
	if out.ID() == dtype.TypeFloat32 {
		narrow := make([]float32, n)
		for i, v := range vals {
			narrow[i] = float32(v)
		}
		return data.NewFixed(name, out, narrow, valid2), nil
	}
	return data.NewFixed(name, out, vals, valid2), nil
}

// winFill carries the last non-null value forward (or the next one backward) over
// each partition's nulls.
//
// # It is winCumExtremum with the reset removed
//
// That function carries a ROW INDEX rather than a value, which is the trick that
// makes one implementation serve every type — strings and Int128 included — with
// no identity value to pick wrong. A fill is the same loop with `best` updated on
// every VALID row and NOT reset to NullIndex on an invalid one, so it inherits
// Take's whole type coverage for free, and NullIndex gives the leading-nulls case
// for free too.
//
// Backward fill is the same walk in the other direction: forEachOrdered flips the
// direction rather than the permutation, so rows are still written into their
// original positions and no second permutation is built.
//
// params.N is the limit — how many consecutive nulls one value may fill. Zero or
// negative means unlimited, because Go has no None and a limit of zero would
// otherwise be a fill that never fills.
func winFill(fn expr.WinFnOp, params expr.WinParams, name string,
	col *data.Column, seg Segments) (*data.Column, error) {

	valid := col.Validity()
	n := col.Len()
	sel := make([]int32, n)
	limit := params.N

	forEachOrdered(seg, fn == expr.WinBackwardFill, func(rows []int32) {
		best := int32(NullIndex)
		run := int64(0) // consecutive nulls filled from best so far
		for _, row := range rows {
			if valid.Get(int(row)) {
				best, run = row, 0
				sel[row] = row
				continue
			}
			if best == NullIndex || (limit > 0 && run >= limit) {
				sel[row] = NullIndex // nothing to carry, or the limit is spent
				continue
			}
			run++
			sel[row] = best
		}
	})

	out, err := Take(col, sel)
	if err != nil {
		return nil, err
	}
	return out.Rename(name), nil
}

// winCumExtremum is the running min or max.
//
// It carries a ROW INDEX rather than a value, which is the trick extremumAcc uses
// for the same reason: one implementation then serves every ordered type, strings
// included, and there is no identity value to pick wrong.
func winCumExtremum(fn expr.WinFnOp, params expr.WinParams, name string,
	col *data.Column, seg Segments) (*data.Column, error) {

	cmp, err := valueComparator(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	n := col.Len()
	sel := make([]int32, n)
	max := fn == expr.WinCumMax

	forEachOrdered(seg, params.Reverse, func(rows []int32) {
		best := int32(NullIndex)
		for _, row := range rows {
			if !valid.Get(int(row)) {
				sel[row] = NullIndex
				continue
			}
			if best == NullIndex {
				best = row
			} else if c := cmp(int(row), int(best)); (max && c > 0) || (!max && c < 0) {
				best = row
			}
			sel[row] = best
		}
	})

	out, err := Take(col, sel)
	if err != nil {
		return nil, err
	}
	return out.Rename(name), nil
}

// forEachOrdered calls fn once per partition with its rows in scan order.
//
// Reverse flips the direction rather than the permutation, so a reversed cumulative
// still writes into original row positions and no second permutation is built.
func forEachOrdered(seg Segments, reverse bool, fn func(rows []int32)) {
	for p := 0; p+1 < len(seg.Bounds); p++ {
		lo, hi := seg.Bounds[p], seg.Bounds[p+1]
		part := seg.Perm[lo:hi]
		if !reverse {
			fn(part)
			continue
		}
		rev := make([]int32, len(part))
		for i, v := range part {
			rev[len(part)-1-i] = v
		}
		fn(rev)
	}
}

// widenToInt128 reads any integer column as []i128.Int128.
func widenToInt128(c *data.Column) ([]i128.Int128, error) {
	if c.DType().Physical().ID() == dtype.TypeInt128 {
		return data.Values[i128.Int128](c)
	}
	if c.DType().IsUnsignedInteger() {
		u, err := widenUnsigned(c)
		if err != nil {
			return nil, err
		}
		out := make([]i128.Int128, len(u))
		for i, v := range u {
			out[i] = i128.FromUint64(v)
		}
		return out, nil
	}
	s, err := widenSigned(c)
	if err != nil {
		return nil, err
	}
	out := make([]i128.Int128, len(s))
	for i, v := range s {
		out[i] = i128.FromInt64(v)
	}
	return out, nil
}
