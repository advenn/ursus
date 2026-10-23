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
