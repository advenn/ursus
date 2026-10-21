package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// InterpolateFrom answers interpolation for rows of a partition from what a walk
// each way over it in its order found (step 238), as winInterpolate and
// winInterpolateNearest answer them over the partition whole:
//
//   - prevVal and prevAt are the value of the last row at or before each row that
//     holds one, and that row's position, both null before the first;
//   - own is each row's 1-based position, cum_count's;
//   - nextVal and nextBack are the value of the first row at or after it that holds
//     one, and that row's position counted from the end, both null after the last;
//   - rows is the partition's.
//
// A row holding a value is its value. A row with no value on one side stays null.
// Linear takes the point on the line between the two, by position, with the kernel's
// arithmetic; nearest the nearer of the two, the later on a tie, of the column's own
// type.
func InterpolateFrom(nearest bool, name string, out dtype.DataType, rows int64,
	prevVal, prevAt, own, nextVal, nextBack *data.Column) (*data.Column, error) {

	n := prevVal.Len()
	at := data.MustValues[int64](prevAt)
	pos := data.MustValues[uint32](own)
	back := data.MustValues[int64](nextBack)
	// For row i: q its position, last the previous value's, p the next one's.
	spans := func(i int) (q, last, p int64, ok bool) {
		if !prevAt.IsValid(i) || !nextBack.IsValid(i) {
			return 0, 0, 0, false
		}
		return int64(pos[i]) - 1, at[i], rows - 1 - back[i], true
	}
	if nearest {
		both, err := ConcatColumns(name, []*data.Column{prevVal, nextVal})
		if err != nil {
			return nil, err
		}
		sel := make([]int32, n)
		for i := range sel {
			q, last, p, ok := spans(i)
			switch {
			case !ok:
				sel[i] = NullIndex
			case last == q || q-last < p-q:
				sel[i] = int32(i) // its own value, or the earlier, nearer one
			default:
				sel[i] = int32(n + i) // the later one: nearer, or as near
			}
		}
		c, err := Take(both, sel)
		if err != nil {
			return nil, err
		}
		return c.Rename(name), nil
	}
	if out.IsTemporal() {
		// Ticks, in integers, as winInterpolateTicks interpolates them (step 301).
		a, err := ticksOf(prevVal)
		if err != nil {
			return nil, err
		}
		b, err := ticksOf(nextVal)
		if err != nil {
			return nil, err
		}
		res := make([]int64, n)
		ok := make([]bool, n)
		for i := range res {
			q, last, p, has := spans(i)
			if !has {
				continue
			}
			if last == q {
				res[i], ok[i] = a[i], true
				continue
			}
			res[i], ok[i] = interpTick(a[i], b[i], q-last, p-last), true
		}
		return ticksAs(name, out, res, seenBitmap(ok, n)), nil
	}
	a, err := floatsOf(prevVal)
	if err != nil {
		return nil, err
	}
	b, err := floatsOf(nextVal)
	if err != nil {
		return nil, err
	}
	res := make([]float64, n)
	ok := make([]bool, n)
	for i := range res {
		q, last, p, has := spans(i)
		if !has {
			continue
		}
		if last == q {
			res[i], ok[i] = a[i], true
			continue
		}
		span := float64(p - last)
		t := float64(q-last) / span
		res[i], ok[i] = a[i]+(b[i]-a[i])*t, true
	}
	return floatsAs(name, out, res, seenBitmap(ok, n)), nil
}

// floatsOf reads a column's values as floats, as winInterpolate reads its column.
func floatsOf(c *data.Column) ([]float64, error) {
	r, err := readable(c)
	if err != nil {
		return nil, err
	}
	return toFloat64(r)
}
