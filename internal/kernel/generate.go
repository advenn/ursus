package kernel

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Generators, a list a row (step 263, G1 of v0.6.6-scope.md): Polars' int_ranges,
// linear_spaces and repeat_by. The scalar forms, int_range and the rest, are these
// over one row of their arguments, exploded. Each operand is a column of the batch's
// height or a literal one row long, and a null operand gives a null list.

// maxListElems is the most elements a batch of lists holds: its offsets are int32.
const maxListElems = math.MaxInt32

// tooLong refuses a generated batch past what a List's offsets hold.
func tooLong(op string, row int) error {
	return uerr.New(uerr.KindResource, op,
		"%s generates more than %d elements by row %d, more than a List holds", op, maxListElems, row).
		Hint("a range is built whole, as Polars builds it; for a row index, WithRowIndex streams")
}

// asInt64 is c as Int64 values, or a Null column's nothing.
func asInt64(c *data.Column) (*data.Column, error) {
	if c.DType() == dtype.Int64 || c.DType().IsNull() {
		return c, nil
	}
	return Cast(c.Name(), dtype.Int64, true, c)
}

// int64At is row i of c, an Int64 column or one of type Null, and whether it holds
// a value.
func int64At(c *data.Column, i int) (int64, bool) {
	r := rowOf(c, i)
	if c.DType().IsNull() || !c.IsValid(r) {
		return 0, false
	}
	return data.MustValues[int64](c)[r], true
}

// intRanges is int_ranges(start, end, step): the integers from start towards end,
// end excluded, by step; empty where step points away from end. A step of zero is
// refused, where Polars panics.
func intRanges(name string, ops []*data.Column, n int) (*data.Column, error) {
	cols := make([]*data.Column, 3)
	for i, c := range ops[:3] {
		var err error
		if cols[i], err = asInt64(c); err != nil {
			return nil, err
		}
	}
	var vals []int64
	offs := make([]int32, 1, n+1)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		s, ok1 := int64At(cols[0], i)
		e, ok2 := int64At(cols[1], i)
		st, ok3 := int64At(cols[2], i)
		ok := ok1 && ok2 && ok3
		if ok && st == 0 {
			return nil, uerr.New(uerr.KindValue, "int_ranges",
				"int_ranges' step is zero in row %d", i).
				Hint("a range moves by a step that is not zero")
		}
		if ok {
			// The count in unsigned arithmetic, which holds any span of int64.
			var cnt uint64
			switch {
			case st > 0 && e > s:
				cnt = (uint64(e) - uint64(s) + uint64(st) - 1) / uint64(st)
			case st < 0 && e < s:
				cnt = (uint64(s) - uint64(e) + uint64(-st) - 1) / uint64(-st)
			}
			if cnt > uint64(maxListElems-len(vals)) {
				return nil, tooLong("int_ranges", i)
			}
			for k := range cnt {
				vals = append(vals, s+int64(k)*st)
			}
		}
		offs = append(offs, int32(len(vals)))
		valid.Append(ok)
	}
	child := data.NewFixed(name, dtype.Int64, vals, bitmap.AllSet(len(vals)))
	return data.NewList(name, offs, child, valid.Finish()), nil
}

// linearSpace is n values evenly spaced from start to end, closed at the ends
// closed says: Polars' formula, so the answers agree to the bit. Each value but the
// last is first + i*step; the last is the far end, exactly.
func linearSpace[F float32 | float64](s, e F, n int, closed expr.Closed, out []F) []F {
	switch n {
	case 0:
		return out
	case 1:
		switch closed {
		case expr.ClosedNone:
			return append(out, (e+s)/2)
		case expr.ClosedRight:
			return append(out, e)
		}
		return append(out, s)
	}
	span := e - s
	var first, step, last F
	switch closed {
	case expr.ClosedNone:
		step = span / F(n+1)
		first, last = s+step, e-step
	case expr.ClosedLeft:
		step = span / F(n)
		first, last = s, e-span/F(n)
	case expr.ClosedRight:
		step = span / F(n)
		first, last = s+span/F(n), e
	default:
		step = span / F(n-1)
		first, last = s, e
	}
	for i := range n - 1 {
		// The conversion rounds the product before the sum, as Polars does: without
		// it the compiler may fuse the two into one multiply-add, rounded once, and
		// a value can differ in its last bit (Go spec, "Floating-point operators").
		out = append(out, F(F(i)*step)+first)
	}
	return append(out, last)
}

// linearSpaces is linear_spaces(start, end, n, closed), a list a row of out's
// element type: Float32 for two Float32 ends, a temporal type for temporal ends, and
// Float64 otherwise. Temporal ends are spaced over their ticks as float64 and each
// value truncated toward zero, as Polars spaces them; a Date's ticks are first
// microseconds, out being Datetime(us).
func linearSpaces(name string, out dtype.DataType, ops []*data.Column, n int) (*data.Column, error) {
	elem := out.Inner()
	closed := expr.Closed(data.MustValues[int64](ops[3])[0])
	cnt, err := asInt64(ops[2])
	if err != nil {
		return nil, err
	}
	var read func(c *data.Column, r int) (float64, bool)
	ends := make([]*data.Column, 2)
	for k, c := range ops[:2] {
		switch {
		case c.DType().IsNull():
		case elem.IsTemporal():
			ticks, err := readTicks(c)
			if err != nil {
				return nil, err
			}
			scale := 1.0
			if c.DType().ID() == dtype.TypeDate {
				scale = 86_400e6
			}
			c = data.NewFixed(c.Name(), dtype.Float64, floatTicks(ticks, scale), c.Validity())
		case c.DType() != dtype.Float64:
			if c, err = Cast(c.Name(), dtype.Float64, true, c); err != nil {
				return nil, err
			}
		}
		ends[k] = c
	}
	read = func(c *data.Column, i int) (float64, bool) {
		r := rowOf(c, i)
		if c.DType().IsNull() || !c.IsValid(r) {
			return 0, false
		}
		return data.MustValues[float64](c)[r], true
	}
	var f64 []float64
	var f32 []float32
	offs := make([]int32, 1, n+1)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		s, ok1 := read(ends[0], i)
		e, ok2 := read(ends[1], i)
		k, ok3 := int64At(cnt, i)
		ok := ok1 && ok2 && ok3
		if ok && k < 0 {
			return nil, uerr.New(uerr.KindValue, "linear_space",
				"linear_space takes %d samples in row %d", k, i).
				Hint("the number of samples is not negative")
		}
		if ok {
			if k > int64(maxListElems-len(f64)-len(f32)) {
				return nil, tooLong("linear_space", i)
			}
			if elem == dtype.Float32 {
				f32 = linearSpace(float32(s), float32(e), int(k), closed, f32)
			} else {
				f64 = linearSpace(s, e, int(k), closed, f64)
			}
		}
		offs = append(offs, int32(len(f64)+len(f32)))
		valid.Append(ok)
	}
	var child *data.Column
	switch {
	case elem == dtype.Float32:
		child = data.NewFixed(name, elem, f32, bitmap.AllSet(len(f32)))
	case elem.IsTemporal():
		ticks := make([]int64, len(f64))
		for j, v := range f64 {
			ticks[j] = int64(v)
		}
		child = packTicks(name, elem, ticks, bitmap.AllSet(len(ticks)))
	default:
		child = data.NewFixed(name, elem, f64, bitmap.AllSet(len(f64)))
	}
	return data.NewList(name, offs, child, valid.Finish()), nil
}

// floatTicks is ticks as float64, each times scale.
func floatTicks(ticks []int64, scale float64) []float64 {
	out := make([]float64, len(ticks))
	for i, t := range ticks {
		out[i] = float64(t) * scale
	}
	return out
}

// repeatBy is value.repeat_by(n): each row's value n times, a null value n nulls. A
// null n gives a null list, and a negative one is refused, as Polars refuses it.
func repeatBy(name string, value, by *data.Column, n int) (*data.Column, error) {
	cnt, err := asInt64(by)
	if err != nil {
		return nil, err
	}
	var pick []int32
	offs := make([]int32, 1, n+1)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		k, ok := int64At(cnt, i)
		if ok && k < 0 {
			return nil, uerr.New(uerr.KindValue, "repeat_by",
				"repeat_by repeats a value %d times in row %d", k, i).
				Hint("a count is not negative")
		}
		if ok {
			if k > int64(maxListElems-len(pick)) {
				return nil, tooLong("repeat_by", i)
			}
			r := int32(rowOf(value, i))
			for range k {
				pick = append(pick, r)
			}
		}
		offs = append(offs, int32(len(pick)))
		valid.Append(ok)
	}
	child, err := Take(value, pick)
	if err != nil {
		return nil, err
	}
	return data.NewList(name, offs, child, valid.Finish()), nil
}
