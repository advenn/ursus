package kernel

import (
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// rollingQuantile is a rolling median or quantile (step 227): each row's answer is
// the quantile of its window's values, as Quantile answers a group's.
//
// The window's values are kept sorted, under compareTotalF64, the order the
// quantile reads: a row entering is inserted where a binary search puts it, and one
// leaving is removed from where one finds it. Each row then reads its two ranks and
// interpolates them as quantileOf does, so NaN is the largest value, as it is in
// Polars' rolling quantile too. A row costs a search and a move of the window, not a
// sort.
func rollingQuantile(fn expr.WinFnOp, params expr.WinParams, name string, out dtype.DataType,
	col *data.Column, seg Segments, win windowsOf, minN int) (*data.Column, error) {

	q, interp := params.Q, params.Interp
	if fn == expr.WinRollingMedian {
		q, interp = 0.5, expr.InterpLinear
	}
	vals, err := toFloat64(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]float64, col.Len())
	ok := make([]bool, col.Len())
	var failed error
	forEachOrdered(seg, false, func(rows []int32) {
		lo, hi := win(rows)
		var sorted []float64
		from, to := 0, 0
		for i, row := range rows {
			for ; to < int(hi[i]); to++ {
				if r := rows[to]; valid.Get(int(r)) {
					v := vals[r]
					at, _ := slices.BinarySearchFunc(sorted, v, compareTotalF64)
					sorted = slices.Insert(sorted, at, v)
				}
			}
			for ; from < int(lo[i]); from++ {
				if r := rows[from]; valid.Get(int(r)) {
					at, found := slices.BinarySearchFunc(sorted, vals[r], compareTotalF64)
					if !found {
						failed = uerr.Internalf("kernel: %s lost a value of its window", fn)
						return
					}
					sorted = slices.Delete(sorted, at, at+1)
				}
			}
			if n := len(sorted); n > 0 && n >= minN {
				l, h, frac := ranks(n, q)
				res[row], ok[row] = interpolate(sorted[l], sorted[h], frac, interp), true
			}
		}
	})
	if failed != nil {
		return nil, failed
	}
	return floatsAs(name, out, res, seenBitmap(ok, len(ok))), nil
}
