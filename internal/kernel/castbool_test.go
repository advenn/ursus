package kernel_test

// TestBoolCastAgreesWithCanCast closes the third instance of a divergence unary.go
// has now recorded three times: dtype.CanCast promised a conversion and the kernel
// refused it at execution.
//
// CanCast's arm is explicit —
//
//	case from.IsNumeric() && to.IsBool(), from.IsBool() && to.IsNumeric():
//	    return true
//
// — and kernel.Cast implemented neither direction, so `Col("flag").Cast(Int64)`
// planned cleanly and failed when it ran. Step 11 made it reachable WITHOUT a
// user-written cast: IsClose casts both of its operands to Float64, so
// Col("flag").IsClose(...) on a Bool column failed with a message that never
// mentions IsClose.
//
// Both directions, and over every numeric type rather than a representative one:
// the failure was in the type switch, so a single width would not have found it.

import (
	"math"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

var boolCastTypes = []dtype.DataType{
	dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64, dtype.Int128,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64,
	dtype.Float32, dtype.Float64,
	// Decimal is deliberately NOT here. See TestDecimalCastDivergenceIsKnown.
}

func TestBoolCastAgreesWithCanCast(t *testing.T) {
	// false, true, null — the null arm matters because a cast must carry validity
	// through rather than rendering an invalid slot as false.
	valid := bitmap.NewBuilder(3)
	for _, v := range []bool{true, true, false} {
		valid.Append(v)
	}
	bits := bitmap.NewBuilder(3)
	for _, v := range []bool{false, true, false} {
		bits.Append(v)
	}
	src := data.NewBool("v", bits.Finish(), valid.Finish())

	for _, to := range boolCastTypes {
		t.Run("bool_to_"+to.String(), func(t *testing.T) {
			if !dtype.CanCast(dtype.Bool, to) {
				t.Fatalf("CanCast(Bool, %s) is false; this test is stale", to)
			}
			got, err := kernel.Cast("v", to, false, src)
			if err != nil {
				t.Fatalf("CanCast promised Bool -> %s and the kernel refused: %v", to, err)
			}
			if got.DType() != to {
				t.Fatalf("cast to %s produced %s", to, got.DType())
			}
			if got.Len() != 3 {
				t.Fatalf("cast produced %d rows", got.Len())
			}
			if !got.IsValid(0) || !got.IsValid(1) {
				t.Error("a valid bool became null")
			}
			if got.IsValid(2) {
				t.Error("a null bool became a value")
			}
			// false is 0 and true is 1, which is what every other engine does and what
			// the String cast already renders.
			if zero, one := numAt(t, got, 0), numAt(t, got, 1); zero != 0 || one != 1 {
				t.Errorf("false/true cast to %v/%v, want 0/1", zero, one)
			}
		})
	}

	for _, from := range boolCastTypes {
		t.Run(from.String()+"_to_bool", func(t *testing.T) {
			if !dtype.CanCast(from, dtype.Bool) {
				t.Fatalf("CanCast(%s, Bool) is false; this test is stale", from)
			}
			// 0, 1 and null in `from`'s own storage, built by casting from Int64 so this
			// test does not need eleven literal constructors.
			ints := data.NewFixed("v", dtype.Int64, []int64{0, 1, 0}, valid.Finish())
			in, err := kernel.Cast("v", from, false, ints)
			if err != nil {
				t.Fatalf("building the %s fixture: %v", from, err)
			}
			got, err := kernel.Cast("v", dtype.Bool, false, in)
			if err != nil {
				t.Fatalf("CanCast promised %s -> Bool and the kernel refused: %v", from, err)
			}
			if !got.DType().IsBool() {
				t.Fatalf("cast to Bool produced %s", got.DType())
			}
			vs := got.Bools()
			if vs.Get(0) || !vs.Get(1) {
				t.Errorf("0/1 cast to %v/%v, want false/true", vs.Get(0), vs.Get(1))
			}
			if got.IsValid(2) {
				t.Error("a null number became false rather than staying null")
			}
		})
	}
}

// TestNanCastsToTrue pins the one value where "!= 0" is a choice rather than a
// definition. NaN is not zero, so it is true — which is what C, SQL and Polars all
// answer, and the alternative would make a cast the only place in the library where
// NaN behaves like a null.
func TestNanCastsToTrue(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), math.Copysign(0, -1)} {
		in := data.NewFixed("v", dtype.Float64, []float64{v}, bitmap.AllSet(1))
		got, err := kernel.Cast("v", dtype.Bool, false, in)
		if err != nil {
			t.Fatal(err)
		}
		// -0.0 == 0 is true in IEEE 754, so negative zero is FALSE — the one case
		// where the bit pattern and the comparison disagree.
		want := v != 0
		if got := got.Bools().Get(0); got != want {
			t.Errorf("%v cast to %v, want %v", v, got, want)
		}
	}
}

// numAt reads row i of any numeric column as a float64, so one assertion covers
// eleven storage types.
func numAt(t *testing.T, c *data.Column, i int) float64 {
	t.Helper()
	switch c.DType().ID() {
	case dtype.TypeInt8:
		return float64(mustVals[int8](t, c)[i])
	case dtype.TypeInt16:
		return float64(mustVals[int16](t, c)[i])
	case dtype.TypeInt32:
		return float64(mustVals[int32](t, c)[i])
	case dtype.TypeInt64:
		return float64(mustVals[int64](t, c)[i])
	case dtype.TypeInt128:
		return mustVals[i128.Int128](t, c)[i].Float64()
	case dtype.TypeUint8:
		return float64(mustVals[uint8](t, c)[i])
	case dtype.TypeUint16:
		return float64(mustVals[uint16](t, c)[i])
	case dtype.TypeUint32:
		return float64(mustVals[uint32](t, c)[i])
	case dtype.TypeUint64:
		return float64(mustVals[uint64](t, c)[i])
	case dtype.TypeFloat32:
		return float64(mustVals[float32](t, c)[i])
	case dtype.TypeFloat64:
		return mustVals[float64](t, c)[i]
	default:
		t.Fatalf("numAt does not handle %s", c.DType())
		return 0
	}
}

func mustVals[T data.Fixed](t *testing.T, c *data.Column) []T {
	t.Helper()
	v, err := data.Values[T](c)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestDecimalCastDivergenceIsKnown is a tripwire, not an assertion of correctness.
//
// Running the bool differential over everything CanCast calls numeric surfaced a
// FOURTH instance of the plan-accepts / kernel-rejects divergence, and this one is
// not a bug in the kernel: CanCast's `from.IsNumeric() && to.IsNumeric()` arm
// admits Decimal, and kernel.Cast refuses it on purpose with a reason that is
// right —
//
//	a decimal is stored as an unscaled integer, so this cast would be wrong by a
//	factor of 10^scale rather than merely imprecise
//
// So the two sides disagree about a whole family of casts, and every numeric ->
// Decimal cast plans cleanly and fails at execution. Reconciling them is a
// deliberate change with reach: dropping Decimal from CanCast's numeric arm moves
// the refusal to plan time, which is where it belongs, but it also changes the
// fixture path decimal_test.go and TestDecimalMathIsRefusedAtPlanTime use to build
// a Decimal column at all. That is its own decision, not a side effect of a bool
// cast, so step 12 records it rather than making it.
//
// This test pins the current state of BOTH sides. Whichever one moves first, it
// fires, and the pair has to be reconciled rather than drifting further apart.
func TestDecimalCastDivergenceIsKnown(t *testing.T) {
	dec := dtype.Decimal(10, 2)
	for _, c := range []struct{ from, to dtype.DataType }{
		{dtype.Int64, dec}, {dec, dtype.Int64},
		{dtype.Bool, dec}, {dec, dtype.Bool},
		{dtype.Float64, dec},
	} {
		if !dtype.CanCast(c.from, c.to) {
			t.Errorf("CanCast(%s, %s) is now false — the kernel already refuses this, "+
				"so drop this pair and the divergence is closed", c.from, c.to)
			continue
		}
		in, err := kernel.NullColumn("v", c.from, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kernel.Cast("v", c.to, false, in); err == nil {
			t.Errorf("the kernel now implements %s -> %s — update this tripwire and "+
				"fold the pair into TestBoolCastAgreesWithCanCast", c.from, c.to)
		}
	}
}
