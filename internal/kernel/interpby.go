package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// interpolate_by (step 329): Polars' interpolate_by. In by's order, each run of nulls
// between two values is filled on the line between them, at each row's by:
//
//	y0 + (y1 − y0)/(x1 − x0) · (x − x0)
//
// the slope first, as Polars computes it: that order matched it to the bit on 2,000
// random points, where the same arithmetic in other orders missed a tenth to nearly
// half of them. The by distances are taken in by's own type, an integer's exactly, a
// Date's in days, and a Float32 is computed in float32, as Polars computes both. A run
// before the first value or after the last stays null, and a NaN is a value.

// nullBy refuses a by holding a null, as Polars refuses it.
func nullBy() error {
	return uerr.New(uerr.KindValue, "interpolate_by",
		"interpolate_by's by holds a null, which has no place on the line, as Polars refuses it").
		Hint("fill the nulls in by, or drop their rows, first")
}

// winInterpolateBy answers interpolate_by over pair, the value and its by: each
// partition's rows sorted stably by by, so rows of one by are taken in the window's
// order, and the answers written back to the rows.
func winInterpolateBy(name string, out dtype.DataType, pair *data.Column, seg Segments) (*data.Column, error) {
	value, by, err := pairFields(pair)
	if err != nil {
		return nil, err
	}
	if value, err = readable(value); err != nil {
		return nil, err
	}
	if by, err = readable(by); err != nil {
		return nil, err
	}
	if by.NullCount() > 0 {
		return nil, nullBy()
	}
	vals, err := toFloat64(value)
	if err != nil {
		return nil, err
	}
	var (
		ticks []int64
		xs    []float64
	)
	if by.DType().IsFloat() {
		xs, err = toFloat64(by)
	} else {
		ticks, err = byTicks(by)
	}
	if err != nil {
		return nil, err
	}
	n := value.Len()
	valid := value.Validity()
	res, ok := make([]float64, n), make([]bool, n)
	f32 := out == dtype.Float32
	var fail error
	forEachOrdered(seg, false, func(rows []int32) {
		if fail != nil {
			return
		}
		// In by's order, stably: a partition already in it, as a series in time
		// mostly is, as it is; another by a radix sort of its by.
		sorted := rows
		if !inByOrder(rows, ticks, xs) {
			// The partition's bys: by itself when the partition is every row in order,
			// a window over the frame, else gathered.
			keys, err := by, error(nil)
			if !isIdentity(rows, by.Len()) {
				keys, err = Take(by, rows)
			}
			if err == nil {
				var idx []int32
				if idx, err = ArgSortColumns([]*data.Column{keys}, []SortSpec{{}}, len(rows)); err == nil {
					sorted = make([]int32, len(rows))
					for i, j := range idx {
						sorted[i] = rows[j]
					}
				}
			}
			if err != nil {
				fail = err
				return
			}
		}
		if xs != nil {
			fillGaps(sorted, xs, vals, valid, f32, res, ok)
		} else {
			fillGaps(sorted, ticks, vals, valid, f32, res, ok)
		}
	})
	if fail != nil {
		return nil, fail
	}
	return floatsAs(name, out, res, seenBitmap(ok, n)), nil
}

// isIdentity reports whether rows is 0 to n-1 in order.
func isIdentity(rows []int32, n int) bool {
	if len(rows) != n {
		return false
	}
	for i, r := range rows {
		if r != int32(i) {
			return false
		}
	}
	return true
}

// inByOrder reports whether rows are in by's order already, ursus's for floats: -0 as
// 0, and NaN above every number.
func inByOrder(rows []int32, ticks []int64, xs []float64) bool {
	if xs != nil {
		for i := 1; i < len(rows); i++ {
			if OrderKeyF64(xs[rows[i]]) < OrderKeyF64(xs[rows[i-1]]) {
				return false
			}
		}
		return true
	}
	for i := 1; i < len(rows); i++ {
		if ticks[rows[i]] < ticks[rows[i-1]] {
			return false
		}
	}
	return true
}

// fillGaps answers sorted, rows in by's order: each its value, and each run of nulls
// between two values on the line between them, at the rows' by, xs, whose distances
// are taken in by's own type.
func fillGaps[X int64 | float64](sorted []int32, xs []X, vals []float64, valid bitmap.View, f32 bool,
	res []float64, ok []bool) {
	last := -1 // the place of the last value seen
	for p, row := range sorted {
		if !valid.Get(int(row)) {
			continue
		}
		res[row], ok[row] = vals[row], true
		if last >= 0 && p-last > 1 {
			r0 := sorted[last]
			y0, y1, x0 := vals[r0], vals[row], xs[r0]
			span := float64(xs[row] - x0)
			if f32 {
				slope := float32(float32(y1)-float32(y0)) / float32(span)
				for _, r := range sorted[last+1 : p] {
					res[r], ok[r] = float64(float32(y0)+float32(slope*float32(float64(xs[r]-x0)))), true
				}
			} else {
				slope := (y1 - y0) / span
				for _, r := range sorted[last+1 : p] {
					res[r], ok[r] = y0+float64(slope*float64(xs[r]-x0)), true
				}
			}
		}
		last = p
	}
}

// InterpolateByFrom is interpolate_by's answer in a sorted pass (step 329), from its
// steps' columns in by's order: the last value, its place, each row's own place, the
// next value and its place counted from the end, as InterpolateFrom reads them; and
// the by of the last value, of the next one, and of the row. Its arithmetic is
// fillGaps'.
func InterpolateByFrom(name string, out dtype.DataType, rows int64,
	prevVal, prevAt, own, nextVal, nextBack, prevBy, nextBy, ownBy *data.Column) (*data.Column, error) {

	if ownBy.NullCount() > 0 {
		return nil, nullBy()
	}
	at, pos := data.MustValues[int64](prevAt), data.MustValues[uint32](own)
	y0s, err := toFloat64(prevVal)
	if err != nil {
		return nil, err
	}
	y1s, err := toFloat64(nextVal)
	if err != nil {
		return nil, err
	}
	n := prevVal.Len()
	res, ok := make([]float64, n), make([]bool, n)
	// own is a row's own value: one before the first value or after the last has
	// none, and a row holding one is its own last.
	own2 := func(i int) (has, held bool) {
		if !prevAt.IsValid(i) || !nextBack.IsValid(i) {
			return false, false
		}
		return true, at[i] == int64(pos[i])-1
	}
	f32 := out == dtype.Float32
	if ownBy.DType().IsFloat() {
		x0, err := toFloat64(prevBy)
		if err != nil {
			return nil, err
		}
		x1, err := toFloat64(nextBy)
		if err != nil {
			return nil, err
		}
		x, err := toFloat64(ownBy)
		if err != nil {
			return nil, err
		}
		lineFrom(n, own2, y0s, y1s, x0, x1, x, f32, res, ok)
	} else {
		x0, err := byTicks(prevBy)
		if err != nil {
			return nil, err
		}
		x1, err := byTicks(nextBy)
		if err != nil {
			return nil, err
		}
		x, err := byTicks(ownBy)
		if err != nil {
			return nil, err
		}
		lineFrom(n, own2, y0s, y1s, x0, x1, x, f32, res, ok)
	}
	return floatsAs(name, out, res, seenBitmap(ok, n)), nil
}

// lineFrom answers each row of a sorted pass from its neighbours' values and bys, as
// fillGaps answers a partition.
func lineFrom[X int64 | float64](n int, own func(i int) (has, held bool), y0s, y1s []float64, x0, x1, x []X,
	f32 bool, res []float64, ok []bool) {
	for i := range n {
		has, held := own(i)
		switch {
		case !has:
			continue // before the first value, or after the last
		case held:
			res[i], ok[i] = y0s[i], true // the row's own value
			continue
		}
		y0, y1, span, d := y0s[i], y1s[i], float64(x1[i]-x0[i]), float64(x[i]-x0[i])
		if f32 {
			slope := float32(float32(y1)-float32(y0)) / float32(span)
			res[i] = float64(float32(y0) + float32(slope*float32(d)))
		} else {
			res[i] = y0 + float64((y1-y0)/span*d)
		}
		ok[i] = true
	}
}
