package physical

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// orderKeys reads a key column as int64s in the column's own order, for the
// operators that search or merge on one key: an as-of join and MergeSorted.
//
// Both used to read the key as int64 ticks, which only the temporal types and Int64
// are, so an as-of join on a Float, Int8 or Uint64 key planned and then failed at
// Collect with "cannot be read as Int64", and MergeSorted refused Float64 with a
// hint that the key "must be numeric or temporal" (audit.md J7). Every integer and
// float maps onto int64 without changing its order:
//
//   - a signed integer, or an unsigned one narrower than 64 bits, is its value;
//   - a Uint64 has its top bit flipped, which moves [0, 2^64) onto int64's range in
//     order and keeps every difference exact modulo 2^64, so absDiffU64 is still
//     the exact distance;
//   - a float is its IEEE bits arranged to sort as integers, under ursus's total
//     order: every NaN is one key, above everything, and -0 is +0. floatOfKey
//     undoes it, for the distance "nearest" measures.
//
// A null row's key is 0; the caller decides what a null means.
func orderKeys(c *data.Column) ([]int64, error) {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return widenKeys[int8](c)
	case dtype.TypeInt16:
		return widenKeys[int16](c)
	case dtype.TypeInt32:
		return widenKeys[int32](c)
	case dtype.TypeInt64:
		return data.Values[int64](c)
	case dtype.TypeUint8:
		return widenKeys[uint8](c)
	case dtype.TypeUint16:
		return widenKeys[uint16](c)
	case dtype.TypeUint32:
		return widenKeys[uint32](c)
	case dtype.TypeUint64:
		v, err := data.Values[uint64](c)
		if err != nil {
			return nil, err
		}
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = int64(x ^ 1<<63)
		}
		return out, nil
	case dtype.TypeFloat32:
		v, err := data.Values[float32](c)
		if err != nil {
			return nil, err
		}
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = keyOfFloat(float64(x))
		}
		return out, nil
	case dtype.TypeFloat64:
		v, err := data.Values[float64](c)
		if err != nil {
			return nil, err
		}
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = keyOfFloat(x)
		}
		return out, nil
	}
	return nil, uerr.Internalf("physical: %s has no order key", c.DType())
}

func widenKeys[T int8 | int16 | int32 | uint8 | uint16 | uint32](c *data.Column) ([]int64, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(v))
	for i, x := range v {
		out[i] = int64(x)
	}
	return out, nil
}

// keyOfFloat is x's place in ursus's total order, as an int64.
func keyOfFloat(x float64) int64 {
	switch {
	case math.IsNaN(x):
		return math.MaxInt64
	case x == 0:
		x = 0 // -0 is +0
	}
	u := math.Float64bits(x)
	if u>>63 == 1 {
		u = ^u // a negative float: larger magnitudes sort lower
	} else {
		u |= 1 << 63
	}
	return int64(u ^ 1<<63)
}

// floatOfKey undoes keyOfFloat. NaN's key gives NaN back.
func floatOfKey(k int64) float64 {
	if k == math.MaxInt64 {
		return math.NaN()
	}
	u := uint64(k) ^ 1<<63
	if u>>63 == 1 {
		u &^= 1 << 63
	} else {
		u = ^u
	}
	return math.Float64frombits(u)
}
