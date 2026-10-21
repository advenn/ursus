package kernel

import (
	"math/bits"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Interpolating instants, durations and times (step 301): on their ticks, in
// integers, as Polars' linear_itp does on a temporal column's physical values. A
// point is a + step·(b−a)/span with the quotient truncated toward zero, so between
// day −2 and day −1 the middle is day −2, where a float's line truncated would give
// day −1. The product is taken in 128 bits, where Polars' wraps past its width.

// interpTick is the point step of span along the line from a to b.
func interpTick(a, b, step, span int64) int64 {
	d := b - a
	neg := d < 0
	ud := uint64(d)
	if neg {
		ud = -ud
	}
	// step < span, so ud·step/span < ud and the quotient fits: hi < span.
	hi, lo := bits.Mul64(ud, uint64(step))
	q, _ := bits.Div64(hi, lo, uint64(span))
	if neg {
		return a - int64(q)
	}
	return a + int64(q)
}

// ticksOf is a temporal column's ticks: a Date's days, and a Datetime's, a
// Duration's and a Time's units.
func ticksOf(c *data.Column) ([]int64, error) {
	c, err := readable(c)
	if err != nil {
		return nil, err
	}
	switch c.DType().Physical().ID() {
	case dtype.TypeInt32:
		v, err := data.Values[int32](c)
		if err != nil {
			return nil, err
		}
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = int64(x)
		}
		return out, nil
	case dtype.TypeInt64:
		return data.Values[int64](c)
	}
	return nil, uerr.Internalf("kernel: interpolate ticks of %s", c.DType())
}

// ticksAs stores ticks as out, a temporal type.
func ticksAs(name string, out dtype.DataType, ticks []int64, valid bitmap.View) *data.Column {
	if out.Physical().ID() == dtype.TypeInt32 {
		t32 := make([]int32, len(ticks))
		for i, v := range ticks {
			t32[i] = int32(v) // between two of the column's own days, so in range
		}
		return data.NewFixed(name, out, t32, valid)
	}
	return data.NewFixed(name, out, ticks, valid)
}

// winInterpolateTicks is winInterpolate of a temporal column.
func winInterpolateTicks(name string, out dtype.DataType, col *data.Column, seg Segments) (*data.Column, error) {
	ticks, err := ticksOf(col)
	if err != nil {
		return nil, err
	}
	valid := col.Validity()
	res := make([]int64, col.Len())
	ok := make([]bool, col.Len())
	forEachOrdered(seg, false, func(rows []int32) {
		last := -1 // the position of the last value seen
		for p, row := range rows {
			if !valid.Get(int(row)) {
				continue
			}
			res[row], ok[row] = ticks[row], true
			if last >= 0 && p-last > 1 {
				a, b := ticks[rows[last]], ticks[row]
				span := int64(p - last)
				for q := last + 1; q < p; q++ {
					res[rows[q]], ok[rows[q]] = interpTick(a, b, int64(q-last), span), true
				}
			}
			last = p
		}
	})
	return ticksAs(name, out, res, seenBitmap(ok, len(ok))), nil
}
