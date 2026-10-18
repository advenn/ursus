package kernel

import (
	"cmp"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// The total order as values (step 279, M3 of v0.6.6-scope.md): order_cmp compares
// two operands a row, and order_sign a value with its type's zero, each in the order
// Sort uses: NaN above every number, -0 with 0. Polars' search_sorted, index_of and
// peaks compare so, where ursus's Lt and Eq are IEEE's.

// orderCmp is -1, 0 or 1 a row as ops[0] sorts before, with or after ops[1], both
// read in their common type; null where either is.
func orderCmp(name string, ops []*data.Column, n int) (*data.Column, error) {
	common, err := expr.OrderCmpType(ops[0].DType(), ops[1].DType())
	if err != nil {
		return nil, err
	}
	if common.IsNull() {
		return NullColumn(name, dtype.Int8, n)
	}
	cols := make([]*data.Column, 2)
	for k, c := range ops {
		if c.DType() != common {
			if c, err = Cast(c.Name(), common, true, c); err != nil {
				return nil, err
			}
		}
		if cols[k], err = readable(c); err != nil {
			return nil, err
		}
	}
	res := make([]int8, n)
	ok := bitmap.NewBuilder(n)
	for i := range n {
		ra, rb := rowOf(cols[0], i), rowOf(cols[1], i)
		valid := cols[0].IsValid(ra) && cols[1].IsValid(rb)
		if valid {
			res[i] = int8(compareRows(cols[0], cols[1], ra, rb))
		}
		ok.Append(valid)
	}
	return data.NewFixed(name, dtype.Int8, res, ok.Finish()), nil
}

// compareRows compares row ra of a with row rb of b, of one type, in the total order.
func compareRows(a, b *data.Column, ra, rb int) int {
	dt := a.DType()
	switch {
	case dt.IsBool():
		x, y := a.Bools().Get(ra), b.Bools().Get(rb)
		return cmp.Compare(b2i(x), b2i(y))
	case dt.HasStringStorage():
		return cmp.Compare(a.Strings().Get(ra), b.Strings().Get(rb))
	}
	switch dt.Physical().ID() {
	case dtype.TypeInt8:
		return cmpAt[int8](a, b, ra, rb)
	case dtype.TypeInt16:
		return cmpAt[int16](a, b, ra, rb)
	case dtype.TypeInt32:
		return cmpAt[int32](a, b, ra, rb)
	case dtype.TypeInt64:
		return cmpAt[int64](a, b, ra, rb)
	case dtype.TypeUint8:
		return cmpAt[uint8](a, b, ra, rb)
	case dtype.TypeUint16:
		return cmpAt[uint16](a, b, ra, rb)
	case dtype.TypeUint32:
		return cmpAt[uint32](a, b, ra, rb)
	case dtype.TypeUint64:
		return cmpAt[uint64](a, b, ra, rb)
	case dtype.TypeFloat32:
		x, y := data.MustValues[float32](a)[ra], data.MustValues[float32](b)[rb]
		return cmp.Compare(OrderKeyF32(x), OrderKeyF32(y))
	case dtype.TypeFloat64:
		x, y := data.MustValues[float64](a)[ra], data.MustValues[float64](b)[rb]
		return cmp.Compare(OrderKeyF64(x), OrderKeyF64(y))
	case dtype.TypeInt128:
		return data.MustValues[i128.Int128](a)[ra].Cmp(data.MustValues[i128.Int128](b)[rb])
	}
	return 0
}

func cmpAt[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64](a, b *data.Column, ra, rb int) int {
	return cmp.Compare(data.MustValues[T](a)[ra], data.MustValues[T](b)[rb])
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// OrderSign is -1, 0 or 1 as each value sorts before, with or after its type's zero:
// a number's sign, NaN's 1 and -0's 0; an instant's beside the epoch; a Boolean's 1
// for true. Null is null.
func OrderSign(name string, c *data.Column) (*data.Column, error) {
	c, err := readable(c)
	if err != nil {
		return nil, err
	}
	n := c.Len()
	res := make([]int8, n)
	dt := c.DType()
	for i := range n {
		if !c.IsValid(i) {
			continue
		}
		var s int
		switch {
		case dt.IsBool():
			s = b2i(c.Bools().Get(i))
		default:
			switch dt.Physical().ID() {
			case dtype.TypeInt8:
				s = cmp.Compare(data.MustValues[int8](c)[i], 0)
			case dtype.TypeInt16:
				s = cmp.Compare(data.MustValues[int16](c)[i], 0)
			case dtype.TypeInt32:
				s = cmp.Compare(data.MustValues[int32](c)[i], 0)
			case dtype.TypeInt64:
				s = cmp.Compare(data.MustValues[int64](c)[i], 0)
			case dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64:
				if !isZeroUnsigned(c, i) {
					s = 1
				}
			case dtype.TypeFloat32:
				s = cmp.Compare(OrderKeyF32(data.MustValues[float32](c)[i]), OrderKeyF32(0))
			case dtype.TypeFloat64:
				s = cmp.Compare(OrderKeyF64(data.MustValues[float64](c)[i]), OrderKeyF64(0))
			case dtype.TypeInt128:
				s = data.MustValues[i128.Int128](c)[i].Sign()
			default:
				return nil, uerr.Internalf("kernel: order_sign of %s", dt)
			}
		}
		res[i] = int8(s)
	}
	return data.NewFixed(name, dtype.Int8, res, c.Validity()), nil
}

func isZeroUnsigned(c *data.Column, i int) bool {
	switch c.DType().Physical().ID() {
	case dtype.TypeUint8:
		return data.MustValues[uint8](c)[i] == 0
	case dtype.TypeUint16:
		return data.MustValues[uint16](c)[i] == 0
	case dtype.TypeUint32:
		return data.MustValues[uint32](c)[i] == 0
	}
	return data.MustValues[uint64](c)[i] == 0
}

// Hash is each value's 64-bit hash, seeded: FNV-1a over the bytes the group-by keys
// it by, so values a group-by takes as one hash alike, -0 with 0 and NaN with NaN,
// and a null hashes too; then mixed by splitmix64's finalizer. It is its own
// algorithm, not Polars' (v0.6.6-scope.md §6.3).
func Hash(name string, c *data.Column, seed uint64) (*data.Column, error) {
	if c.DType().IsNull() {
		// No value a group-by keys on: each a null, which hashes as an Int64's does.
		var err error
		if c, err = NullColumn(name, dtype.Int64, c.Len()); err != nil {
			return nil, err
		}
	}
	enc, err := NewGroupKeyEncoder("hash", []*data.Column{c})
	if err != nil {
		return nil, err
	}
	n := c.Len()
	res := make([]uint64, n)
	for i := range n {
		h := uint64(14695981039346656037) ^ seed
		for _, b := range enc.Encode(i) {
			h ^= uint64(b)
			h *= 1099511628211
		}
		h ^= h >> 30
		h *= 0xbf58476d1ce4e5b9
		h ^= h >> 27
		h *= 0x94d049bb133111eb
		h ^= h >> 31
		res[i] = h
	}
	return data.NewFixed(name, dtype.Uint64, res, bitmap.AllSet(n)), nil
}
