package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// floatResult stores the float64 result of a computation at out's width: a Float64
// as it is, a Float32 rounded once to the nearest float32.
//
// # A computation is not a cast
//
// A Float32 sqrt is computed in float64 and stored in float32, and that store ROUNDS
// — which is what a float32 result is, and what Polars and PyArrow return. It went
// through narrow's round-trip test, which asks whether a CAST lost anything: every
// inexact result failed it and became a null, so sqrt(2) and ln(2) were null, and
// ErrInternal in a non-nullable column. Nothing here nulls or refuses. An overflow
// is the +Inf that float32 arithmetic gives, as unaryFloat's and every binary op's
// already are.
//
// For sqrt, rounding through float64 is exactly the correctly rounded float32: 53
// bits is more than twice 24 plus two. For the others it is within one ulp of it.
func floatResult(name string, out dtype.DataType, v []float64, valid bitmap.View) (*data.Column, error) {
	n := len(v)
	switch out.ID() {
	case dtype.TypeFloat64:
		buf, dst := newValuesBuffer[float64](n)
		copy(dst, v)
		return data.NewFixedBuffer(name, out, buf, n, valid), nil
	case dtype.TypeFloat32:
		buf, dst := newValuesBuffer[float32](n)
		for i, x := range v {
			dst[i] = float32(x)
		}
		return data.NewFixedBuffer(name, out, buf, n, valid), nil
	}
	return nil, uerr.Internalf("kernel: a float computation cannot produce %s", out)
}
