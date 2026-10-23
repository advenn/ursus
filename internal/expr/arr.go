package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// The array namespace (steps 341 and 342). An Array is a List each of whose rows has
// its size, so its functions are a List's: most of Arr()'s are List()'s of
// ToList(), written so by the API, and the ones that keep each row's length keep its
// Array here — reverse, sort and shift, eval of an element's value — and to_struct
// and dot read its size.

// ArrDotType is arr.dot's type over Arrays a and b: the sum of their elements'
// products, as list.sum sums them. Each is an Array of numbers, both of one size.
func ArrDotType(a, b dtype.DataType) (dtype.DataType, error) {
	if a.ID() != dtype.TypeArray || b.ID() != dtype.TypeArray {
		return dtype.Null, uerr.New(uerr.KindType, "arr.dot", "arr.dot multiplies two Arrays, got %s and %s", a, b)
	}
	if a.Size() != b.Size() {
		return dtype.Null, uerr.New(uerr.KindType, "arr.dot",
			"arr.dot multiplies Arrays of one size, got %s and %s", a, b)
	}
	if !a.Inner().IsNumeric() || !b.Inner().IsNumeric() {
		return dtype.Null, uerr.New(uerr.KindType, "arr.dot", "arr.dot multiplies numbers, got %s and %s", a, b)
	}
	m, err := ResolveBinary(OpMul, a.Inner(), b.Inner())
	if err != nil {
		return dtype.Null, err
	}
	s, err := ResolveAggBinding(AggSum, m.Out)
	if err != nil {
		return dtype.Null, err
	}
	return s.Out, nil
}

// ReshapeType is the type of t's values in Arrays of shape (step 343): its leaves,
// what a List's or an Array's elements hold, nested in an Array for each dimension
// past the first, the last innermost; of one dimension, the leaves themselves. The
// first dimension is the rows, -1 to infer it, as Polars' reshape.
func ReshapeType(t dtype.DataType, shape []int64) (dtype.DataType, error) {
	if len(shape) == 0 {
		return dtype.Null, uerr.New(uerr.KindValue, "reshape", "reshape takes a shape of at least one dimension")
	}
	for i, d := range shape {
		switch {
		case d == -1 && i > 0:
			return dtype.Null, uerr.New(uerr.KindValue, "reshape", "reshape can only infer the first dimension, got %v", shape).
				Hint("-1 first: the rows are what the values make of the rest")
		case d < -1:
			return dtype.Null, uerr.New(uerr.KindValue, "reshape", "reshape takes no dimension of %d", d)
		}
	}
	leaf := t
	for leaf.IsListLike() {
		leaf = leaf.Inner()
	}
	out := leaf
	for i := len(shape) - 1; i >= 1; i-- {
		out = dtype.Array(out, int(shape[i]))
	}
	return out, nil
}

// ConcatArrType is concat_arr's type over ops (step 343): an Array of their one
// element type, an Array operand's own or another's type, as wide as an Array
// operand's elements and another's one value come to. Polars refuses operands of
// two element types, an integer beside a float too; so does this.
func ConcatArrType(ops []dtype.Field) (dtype.DataType, error) {
	if len(ops) == 0 {
		return dtype.Null, uerr.Internalf("expr: concat_arr of no operand")
	}
	var elem dtype.DataType
	width := 0
	for i, f := range ops {
		t, w := f.Type, 1
		switch t.ID() {
		case dtype.TypeArray:
			t, w = t.Inner(), t.Size()
		case dtype.TypeList:
			return dtype.Null, uerr.New(uerr.KindUnsupported, "concat_arr",
				"concat_arr takes Arrays and values, and %s is a %s", f.Name, f.Type).
				Hint("a List becomes an Array of a width by List().ToArray(width); an Array of Lists is not built yet")
		}
		switch {
		case i == 0 || elem.IsNull():
			elem = t
		case t != elem && !t.IsNull():
			return dtype.Null, uerr.New(uerr.KindType, "concat_arr",
				"concat_arr's operands are of one element type: %s, then %s at %s", elem, t, f.Name).
				Hint("cast the operand first, as Polars refuses it too")
		}
		width += w
	}
	return dtype.Array(elem, width), nil
}
