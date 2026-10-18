package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Integers as bits (step 278, M2 of v0.6.6-scope.md): &, |, ^ and NOT of integers,
// as Polars' operators are. A null is null, as in arithmetic, not Kleene: an
// integer's bits have no absorbing value a null could take.

// bitwise is l op r, both already cast to out, an integer type.
func bitwise(op expr.BinaryOp, name string, out dtype.DataType, l, r *data.Column, n int) (*data.Column, error) {
	// An all-null operand may carry no values; its bits are read as zero.
	var err error
	if l, err = readable(l); err != nil {
		return nil, err
	}
	if r, err = readable(r); err != nil {
		return nil, err
	}
	switch out.Physical().ID() {
	case dtype.TypeInt8:
		return bitwiseOf[int8](op, name, out, l, r, n)
	case dtype.TypeInt16:
		return bitwiseOf[int16](op, name, out, l, r, n)
	case dtype.TypeInt32:
		return bitwiseOf[int32](op, name, out, l, r, n)
	case dtype.TypeInt64:
		return bitwiseOf[int64](op, name, out, l, r, n)
	case dtype.TypeUint8:
		return bitwiseOf[uint8](op, name, out, l, r, n)
	case dtype.TypeUint16:
		return bitwiseOf[uint16](op, name, out, l, r, n)
	case dtype.TypeUint32:
		return bitwiseOf[uint32](op, name, out, l, r, n)
	case dtype.TypeUint64:
		return bitwiseOf[uint64](op, name, out, l, r, n)
	case dtype.TypeInt128:
		return bitwiseInt128(op, name, out, l, r, n)
	}
	return nil, uerr.Internalf("kernel: %s of %s", op, out)
}

func bitwiseOf[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64](op expr.BinaryOp, name string,
	out dtype.DataType, l, r *data.Column, n int) (*data.Column, error) {

	a, err := data.Values[T](l)
	if err != nil {
		return nil, err
	}
	b, err := data.Values[T](r)
	if err != nil {
		return nil, err
	}
	res := make([]T, n)
	at := func(v []T, i int) T {
		if len(v) == 1 {
			return v[0]
		}
		return v[i]
	}
	for i := range res {
		x, y := at(a, i), at(b, i)
		switch op {
		case expr.OpAnd:
			res[i] = x & y
		case expr.OpOr:
			res[i] = x | y
		default:
			res[i] = x ^ y
		}
	}
	return data.NewFixed(name, out, res, bothValid(l, r, n)), nil
}

func bitwiseInt128(op expr.BinaryOp, name string, out dtype.DataType, l, r *data.Column, n int) (*data.Column, error) {
	a, err := data.Values[i128.Int128](l)
	if err != nil {
		return nil, err
	}
	b, err := data.Values[i128.Int128](r)
	if err != nil {
		return nil, err
	}
	res := make([]i128.Int128, n)
	for i := range res {
		x, y := a[min(i, len(a)-1)], b[min(i, len(b)-1)]
		switch op {
		case expr.OpAnd:
			res[i] = i128.Int128{Hi: x.Hi & y.Hi, Lo: x.Lo & y.Lo}
		case expr.OpOr:
			res[i] = i128.Int128{Hi: x.Hi | y.Hi, Lo: x.Lo | y.Lo}
		default:
			res[i] = i128.Int128{Hi: x.Hi ^ y.Hi, Lo: x.Lo ^ y.Lo}
		}
	}
	return data.NewFixed(name, out, res, bothValid(l, r, n)), nil
}

// bothValid is the rows where l and r are each valid, either broadcast.
func bothValid(l, r *data.Column, n int) bitmap.View {
	return bitmap.And(spread(l.Validity(), l.Len(), n), spread(r.Validity(), r.Len(), n))
}

// bitwiseNot is each integer's bits flipped: ~x, -x-1 for a signed one and its
// maximum less x for an unsigned one.
func bitwiseNot(name string, c *data.Column) (*data.Column, error) {
	c, err := readable(c)
	if err != nil {
		return nil, err
	}
	dt := c.DType()
	switch dt.Physical().ID() {
	case dtype.TypeInt8:
		return notOf[int8](name, c)
	case dtype.TypeInt16:
		return notOf[int16](name, c)
	case dtype.TypeInt32:
		return notOf[int32](name, c)
	case dtype.TypeInt64:
		return notOf[int64](name, c)
	case dtype.TypeUint8:
		return notOf[uint8](name, c)
	case dtype.TypeUint16:
		return notOf[uint16](name, c)
	case dtype.TypeUint32:
		return notOf[uint32](name, c)
	case dtype.TypeUint64:
		return notOf[uint64](name, c)
	case dtype.TypeInt128:
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		res := make([]i128.Int128, len(v))
		for i, x := range v {
			res[i] = i128.Int128{Hi: ^x.Hi, Lo: ^x.Lo}
		}
		return data.NewFixed(name, dt, res, c.Validity()), nil
	}
	return nil, uerr.Internalf("kernel: not of %s", dt)
}

func notOf[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64](name string, c *data.Column) (*data.Column, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	res := make([]T, len(v))
	for i, x := range v {
		res[i] = ^x
	}
	return data.NewFixed(name, c.DType(), res, c.Validity()), nil
}
