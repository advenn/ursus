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
// What this asserts is the agreement: the kernel performs every cast CanCast
// promises, to the type promised, with a null carried through. The values — false
// and true as 0 and 1, a number true where it is not zero, NaN and the infinities
// true and -0 false — are Polars 2.1.0's, recorded in the castbool group of
// testdata/oracle and asked through Cast by oracle_castbool_test.go since step 503,
// which took this file's values, NaN's among them.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

var boolCastTypes = []dtype.DataType{
	dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64, dtype.Int128,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64,
	dtype.Float32, dtype.Float64,
	// Decimal is deliberately NOT here. See TestDecimalCastDivergenceIsClosed.
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
			if !got.IsValid(0) || !got.IsValid(1) {
				t.Error("a valid number became null")
			}
			if got.IsValid(2) {
				t.Error("a null number became false rather than staying null")
			}
		})
	}
}

// TestDecimalCastDivergenceIsClosed is what the tripwire became.
//
// It was named for the divergence being KNOWN rather than closed, and it pinned
// BOTH sides of a
// disagreement step 12 found and deliberately did not resolve: CanCast's
// `from.IsNumeric() && to.IsNumeric()` arm admitted Decimal, and kernel.Cast
// refused every one of those casts on purpose, because "a decimal is stored as an
// unscaled integer, so this cast would be wrong by a factor of 10^scale rather than
// merely imprecise".
//
// Its closing line was "whichever one moves first, it fires, and the pair has to be
// reconciled rather than drifting further apart". Step 52 moved CanCast, it fired,
// and it named the consequence in advance — the fixture path math_test.go used to
// build a Decimal column at all. That is a tripwire working exactly as designed,
// and it is the reason this one is rewritten rather than deleted: the pair still
// needs pinning, just from the other side.
//
// Step 69 implemented the numeric pairs — exact or refused, in castdec.go — and
// both sides moved together, so they left this list rather than reopening the
// divergence; castdec_test.go asserts their VALUES, which agreement cannot. Bool is
// what stays: a truth value is not an amount, in either direction. Polars 2.1
// refuses a Boolean to a Decimal as this does (castbool/bool_to_decimal) and casts a
// Decimal to a Boolean, true where it is not zero, where ursus refuses on purpose
// (feature-gaps.md §9; the root's TestDecimalGuards asserts the refusal since step 507).
func TestDecimalCastDivergenceIsClosed(t *testing.T) {
	dec := dtype.Decimal(10, 2)
	for _, c := range []struct{ from, to dtype.DataType }{
		{dtype.Bool, dec}, {dec, dtype.Bool},
	} {
		if dtype.CanCast(c.from, c.to) {
			t.Errorf("CanCast(%s, %s) is true again — the kernel still refuses it, "+
				"so the divergence has reopened", c.from, c.to)
		}
		in, err := kernel.NullColumn("v", c.from, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kernel.Cast("v", c.to, false, in); err == nil {
			t.Errorf("the kernel now implements %s -> %s — CanCast should promise "+
				"it again, and this test should go", c.from, c.to)
		}
	}

	// The one Decimal conversion that IS exact and implemented, kept here so the
	// rule above cannot be read as "Decimal casts nowhere".
	if !dtype.CanCast(dec, dtype.String) {
		t.Error("Decimal -> String is exact: the scale is known, so the unscaled " +
			"integer can be formatted")
	}
}
