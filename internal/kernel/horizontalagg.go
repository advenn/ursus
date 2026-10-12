package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// The horizontal reductions (step 225): max, min, sum and mean of each row's
// values across the operands, and Kleene's any and all.
//
// # Each is the column aggregate, over a row
//
// The operands, cast to the type they meet at, are fed to the aggregate's own
// accumulator one at a time, each row its own group. So a row's max is what Max
// over the same values answers: nulls skipped, NaN the largest, null when every
// value is; its sum widens an integer to Int128 and is null when every value is;
// its mean is the nearest double to the exact one. Polars' differ in three places,
// which the public docs give: its max and min skip NaN, its integer sum keeps the
// operands' type and wraps, and its sum of nulls is 0.

// horizontalReduce is max, min, sum or mean across ops, n rows.
func horizontalReduce(fn expr.CallFn, name string, ops []*data.Column, n int) (*data.Column, error) {
	fields := make([]dtype.Field, len(ops))
	for i, c := range ops {
		fields[i] = dtype.Field{Name: c.Name(), Type: c.DType()}
	}
	in, err := expr.HorizontalInput(fn, fields)
	if err != nil {
		return nil, err
	}
	op := expr.HorizontalAgg(fn)
	bind, err := expr.ResolveAggBinding(op, in)
	if err != nil {
		return nil, err
	}
	acc, err := NewAccumulator(op, in, bind, expr.AggParams{})
	if err != nil {
		return nil, err
	}
	rows := make([]int32, n)
	for i := range rows {
		rows[i] = int32(i)
	}
	acc.Reserve(n)
	for _, c := range ops {
		if c, err = horizontalOperand(c, in, n); err != nil {
			return nil, err
		}
		if err := acc.AddBatch(rows, c); err != nil {
			return nil, err
		}
	}
	return acc.Finish(name, n)
}

// horizontalKleene is any or all across ops, n rows, as Or and And fold: a value
// that decides the row decides it, and a null where none does makes it null.
func horizontalKleene(all bool, name string, ops []*data.Column, n int) (*data.Column, error) {
	cols := make([]*data.Column, len(ops))
	for i, c := range ops {
		var err error
		if cols[i], err = horizontalOperand(c, dtype.Bool, n); err != nil {
			return nil, err
		}
	}
	vals, valid := bitmap.NewBuilder(n), bitmap.NewBuilder(n)
	for i := range n {
		decided, sawNull := false, false
		for _, c := range cols {
			if !c.IsValid(i) {
				sawNull = true
				continue
			}
			if c.Bools().Get(i) != all {
				decided = true
				break
			}
		}
		switch {
		case decided:
			vals.Append(!all)
			valid.Append(true)
		case sawNull:
			vals.Append(false)
			valid.Append(false)
		default:
			vals.Append(all)
			valid.Append(true)
		}
	}
	return data.NewBool(name, vals.Finish(), valid.Finish()), nil
}

// horizontalOperand is c cast to in and n rows long: a literal, one row, is
// repeated.
func horizontalOperand(c *data.Column, in dtype.DataType, n int) (*data.Column, error) {
	var err error
	if c.DType() != in {
		if c, err = Cast(c.Name(), in, true, c); err != nil {
			return nil, err
		}
	}
	if c.Len() == n {
		return c, nil
	}
	return Take(c, make([]int32, n))
}
