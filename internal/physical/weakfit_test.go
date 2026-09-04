package physical_test

// TestWeakFitAgreesWithCast is what licenses expr.FitsExactly existing at all.
//
// kernel.narrow already answers "is this value representable in that type" —
// `t := T(v); if float64(t) != v` — and expr cannot call it: internal/expr is L20
// and internal/kernel is L30, so the import is a levels violation and the check
// has to be written twice. Two copies of a rule is the shape this codebase has
// been bitten by repeatedly, so the two are pinned together here rather than by
// hope.
//
// This package is the lowest level that can see both, which is why the test lives
// beside the evaluator rather than beside either implementation.
//
// The claim is ONE-DIRECTIONAL: fits ⟹ a strict cast succeeds and round-trips.
// The converse is deliberately not asserted, because FitsExactly is conservative
// at the edges (above 2^53 for a float target, and for every type it does not
// enumerate) and being conservative only ever costs a fallback to the type
// ordinary promotion would have chosen anyway.

import (
	"math"
	"testing"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/expr"
	"ursus/internal/kernel"
)

// oneRow builds a length-1 column holding v, the way litColumn does.
func oneRow(t *testing.T, v any) (*data.Column, dtype.DataType) {
	t.Helper()
	switch x := v.(type) {
	case int64:
		return data.NewFixed("lit", dtype.Int64, []int64{x}, bitmap.AllSet(1)), dtype.Int64
	case uint64:
		return data.NewFixed("lit", dtype.Uint64, []uint64{x}, bitmap.AllSet(1)), dtype.Uint64
	case float64:
		return data.NewFixed("lit", dtype.Float64, []float64{x}, bitmap.AllSet(1)), dtype.Float64
	default:
		t.Fatalf("oneRow has no case for %T", v)
		return nil, dtype.Null
	}
}

// readBack reads the single value out of a column as a float64 for comparison,
// or reports that the type is one this test does not compare numerically.
func readBack(c *data.Column) (float64, bool) {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return float64(data.MustValues[int8](c)[0]), true
	case dtype.TypeInt16:
		return float64(data.MustValues[int16](c)[0]), true
	case dtype.TypeInt32:
		return float64(data.MustValues[int32](c)[0]), true
	case dtype.TypeInt64:
		return float64(data.MustValues[int64](c)[0]), true
	case dtype.TypeUint8:
		return float64(data.MustValues[uint8](c)[0]), true
	case dtype.TypeUint16:
		return float64(data.MustValues[uint16](c)[0]), true
	case dtype.TypeUint32:
		return float64(data.MustValues[uint32](c)[0]), true
	case dtype.TypeUint64:
		return float64(data.MustValues[uint64](c)[0]), true
	case dtype.TypeFloat32:
		return float64(data.MustValues[float32](c)[0]), true
	case dtype.TypeFloat64:
		return data.MustValues[float64](c)[0], true
	case dtype.TypeInt128:
		return data.MustValues[i128.Int128](c)[0].Float64(), true
	default:
		return 0, false
	}
}

var weakTargets = []dtype.DataType{
	dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64,
	dtype.Float32, dtype.Float64, dtype.Int128,
	// Types FitsExactly must never admit. Included so a rule that started saying
	// yes to them is caught here rather than at execution.
	dtype.String, dtype.Bool, dtype.Decimal(10, 2),
	dtype.Datetime(dtype.Micro, ""), dtype.Date,
}

func TestWeakFitAgreesWithCast(t *testing.T) {
	values := []any{
		int64(0), int64(1), int64(-1),
		int64(127), int64(128), int64(-128), int64(-129),
		int64(255), int64(256), int64(5000), int64(65535),
		int64(1 << 24), int64(1<<24 + 1),
		int64(1 << 53), int64(1<<53 + 1),
		int64(math.MaxInt64), int64(math.MinInt64),
		uint64(0), uint64(math.MaxUint64),
		0.0, math.Copysign(0, -1), 0.1, 1.5, 3.0, -3.0,
		float64(1 << 24), float64(1<<24 + 1), float64(1 << 53),
		math.NaN(), math.Inf(1), math.Inf(-1),
	}

	var admitted int
	for _, v := range values {
		src, _ := oneRow(t, v)
		for _, to := range weakTargets {
			if !expr.FitsExactly(v, to) {
				continue
			}
			admitted++

			// Strict: a value FitsExactly admits must convert with no loss at all,
			// which is exactly what evalCond's strict cast relies on.
			got, err := kernel.Cast("lit", to, true, src)
			if err != nil {
				t.Errorf("FitsExactly(%v (%T), %s) is true but a strict cast failed: %v",
					v, v, to, err)
				continue
			}
			back, ok := readBack(got)
			if !ok {
				t.Errorf("FitsExactly admitted %s, which this test cannot read back — "+
					"it should not have been admitted at all", to)
				continue
			}
			want, _ := readBack(src)
			if math.IsNaN(want) {
				if !math.IsNaN(back) {
					t.Errorf("NaN into %s round-tripped as %v", to, back)
				}
				continue
			}
			if back != want {
				t.Errorf("FitsExactly(%v, %s) is true but the value round-tripped as %v",
					v, to, back)
			}
		}
	}

	// Corpus adequacy: if the rule admitted nothing, every assertion above was
	// skipped and the test proved nothing at all.
	if admitted < 50 {
		t.Errorf("FitsExactly admitted only %d of %d pairs — too few for the "+
			"agreement to mean anything", admitted, len(values)*len(weakTargets))
	}
}

// TestFitsExactlyMustAdmitTheOrdinaryCases is the other side: conservatism is free
// only while the rule still fires where the feature needs it.
func TestFitsExactlyMustAdmitTheOrdinaryCases(t *testing.T) {
	must := []struct {
		v  any
		to dtype.DataType
	}{
		{int64(0), dtype.Int8}, {int64(0), dtype.Int16}, {int64(0), dtype.Int32},
		{int64(0), dtype.Uint8}, {int64(0), dtype.Uint16}, {int64(0), dtype.Uint32},
		{int64(0), dtype.Uint64}, {int64(0), dtype.Float32}, {int64(0), dtype.Float64},
		{int64(1), dtype.Int8}, {int64(1), dtype.Uint64},
		{0.0, dtype.Float32}, {1.5, dtype.Float32}, {3.0, dtype.Int32},
		{math.NaN(), dtype.Float32},
	}
	for _, c := range must {
		if !expr.FitsExactly(c.v, c.to) {
			t.Errorf("FitsExactly(%v, %s) is false; the fill family needs it", c.v, c.to)
		}
	}

	never := []struct {
		v  any
		to dtype.DataType
	}{
		{int64(5000), dtype.Int8}, {int64(-1), dtype.Uint32},
		{int64(256), dtype.Uint8}, {0.1, dtype.Int32},
		{math.Inf(1), dtype.Int64}, {math.NaN(), dtype.Int64},
		{int64(0), dtype.String}, {int64(0), dtype.Bool},
		{int64(0), dtype.Decimal(10, 2)}, {int64(0), dtype.Date},
		{"x", dtype.String}, {true, dtype.Bool},
	}
	for _, c := range never {
		if expr.FitsExactly(c.v, c.to) {
			t.Errorf("FitsExactly(%v (%T), %s) is true; it must fall back instead",
				c.v, c.v, c.to)
		}
	}
}
