package kernel_test

// dtype.CanCast and kernel.Cast must agree, and the agreement is enumerated.
//
// This divergence has been found and fixed FOUR times, each as a point fix:
//
//	unary.go  "Null(NullT).Cast(Int64) type-checked, planned, and failed at
//	           execution with not-implemented-yet"
//	unary.go  the same for string parse/format
//	unary.go  Decimal -> String, "the same plan-accepts / kernel-rejects
//	           divergence this file records for null casts and string casts"
//	promote.go String <-> Enum, "the same plan-accepts / kernel-rejects divergence
//	           that unary.go records twice and CLAIMS TO HAVE CLOSED"
//
// It was not closed. TestEvaluatorContract found six more in Bool <-> temporal, and
// the count is six only because its fixture has fourteen types; the known
// numeric <-> Decimal case is absent from that fixture entirely.
//
// A fifth point fix would be the same mistake. TypeID has a TypeIDCount sentinel and
// String() returns "Invalid" for anything undeclared, so the whole space enumerates
// with no list — the AggOp trick from step 51 — and the two answers are compared for
// every pair. A sixth recurrence fails the day it is written.
//
// Which side is right is a JUDGEMENT, not a lookup: three of the four historical
// fixes moved the KERNEL to implement what CanCast promised. Bool <-> temporal goes
// the other way, because a date is not a truth value.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// castSample is a representative column for every TypeID that has one.
//
// A type with no sample is REPORTED, not skipped: a TypeID nobody can build is a
// hole in this check, and silently passing over it is how a list goes quiet.
func castSample(id dtype.TypeID) (*data.Column, bool) {
	const n = 2
	v := bitmap.AllSet(n)
	fixed := func(dt dtype.DataType, vals any) *data.Column {
		switch x := vals.(type) {
		case []int8:
			return data.NewFixed("c", dt, x, v)
		case []int16:
			return data.NewFixed("c", dt, x, v)
		case []int32:
			return data.NewFixed("c", dt, x, v)
		case []int64:
			return data.NewFixed("c", dt, x, v)
		case []uint8:
			return data.NewFixed("c", dt, x, v)
		case []uint16:
			return data.NewFixed("c", dt, x, v)
		case []uint32:
			return data.NewFixed("c", dt, x, v)
		case []uint64:
			return data.NewFixed("c", dt, x, v)
		case []float32:
			return data.NewFixed("c", dt, x, v)
		case []float64:
			return data.NewFixed("c", dt, x, v)
		}
		return nil
	}

	switch id {
	case dtype.TypeBool:
		return data.NewBool("c", v, v), true
	case dtype.TypeInt8:
		return fixed(dtype.Int8, []int8{1, 2}), true
	case dtype.TypeInt16:
		return fixed(dtype.Int16, []int16{1, 2}), true
	case dtype.TypeInt32:
		return fixed(dtype.Int32, []int32{1, 2}), true
	case dtype.TypeInt64:
		return fixed(dtype.Int64, []int64{1, 2}), true
	case dtype.TypeUint8:
		return fixed(dtype.Uint8, []uint8{1, 2}), true
	case dtype.TypeUint16:
		return fixed(dtype.Uint16, []uint16{1, 2}), true
	case dtype.TypeUint32:
		return fixed(dtype.Uint32, []uint32{1, 2}), true
	case dtype.TypeUint64:
		return fixed(dtype.Uint64, []uint64{1, 2}), true
	case dtype.TypeFloat32:
		return fixed(dtype.Float32, []float32{1, 2}), true
	case dtype.TypeFloat64:
		return fixed(dtype.Float64, []float64{1, 2}), true
	case dtype.TypeString:
		return data.NewString("c", []string{"1", "2"}, v), true
	case dtype.TypeBinary:
		return data.NewString("c", []string{"1", "2"}, v).WithDType(dtype.Binary), true
	case dtype.TypeDate:
		return fixed(dtype.Date, []int32{1, 2}), true
	case dtype.TypeDatetime:
		return fixed(dtype.Datetime(dtype.Micro, "UTC"), []int64{1, 2}), true
	case dtype.TypeTime:
		return fixed(dtype.Time(dtype.Nano), []int64{1, 2}), true
	case dtype.TypeDuration:
		return fixed(dtype.Duration(dtype.Second), []int64{1, 2}), true
	case dtype.TypeNull:
		return data.NewNull("c", dtype.Null, n), true
	}
	return nil, false
}

// castTarget is the DataType to cast TO for each TypeID.
func castTarget(id dtype.TypeID) (dtype.DataType, bool) {
	switch id {
	case dtype.TypeDatetime:
		return dtype.Datetime(dtype.Micro, "UTC"), true
	case dtype.TypeTime:
		return dtype.Time(dtype.Nano), true
	case dtype.TypeDuration:
		return dtype.Duration(dtype.Second), true
	case dtype.TypeDecimal:
		return dtype.Decimal(10, 2), true
	}
	if c, ok := castSample(id); ok {
		return c.DType(), true
	}
	return dtype.DataType{}, false
}

// TestCanCastAgreesWithTheKernel walks every ordered pair of TypeIDs.
func TestCanCastAgreesWithTheKernel(t *testing.T) {
	var pairs int
	unsampled := map[string]bool{}

	for f := range int(dtype.TypeIDCount) {
		from := dtype.TypeID(f)
		if from.String() == "Invalid" {
			continue
		}
		src, ok := castSample(from)
		if !ok {
			unsampled[from.String()] = true
			continue
		}
		for o := range int(dtype.TypeIDCount) {
			to := dtype.TypeID(o)
			if to.String() == "Invalid" {
				continue
			}
			target, ok := castTarget(to)
			if !ok {
				continue
			}
			pairs++

			promised := dtype.CanCast(src.DType(), target)
			// NON-STRICT, so a value that does not fit becomes a null rather than
			// an error. This asks whether the conversion is IMPLEMENTED, which is
			// what CanCast answers; whether a particular value survives it is
			// cast_test.go's question.
			_, err := kernel.Cast("c", target, false, src)
			delivered := err == nil

			if promised != delivered {
				verb := "refuses"
				if promised {
					verb = "promises"
				}
				t.Errorf("%s -> %s: CanCast %s it and the kernel does the opposite "+
					"(%v) — the planner and the evaluator disagree about what is "+
					"legal", src.DType(), target, verb, err)
			}
		}
	}

	// Anti-vacuity on both axes. A TypeID with no sample is a hole in the check,
	// so the count is reported rather than passed over.
	if pairs < 250 {
		t.Fatalf("only %d type pairs compared — the enumeration has gone vacuous",
			pairs)
	}
	// The uncovered types are NAMED, not counted, and compared against the set
	// that is expected to be uncoverable. A count would let a newly-added type
	// silently take the place of one that became samplable.
	//
	// Each of these needs more than a slice of values to build: a Decimal needs
	// Int128 storage and a scale, an Enum needs its categories in the TYPE, and
	// List, Struct and Array need a child column. Int128 and Uint128 have no Go
	// literal. They are reachable through the CAST TARGET side — castTarget builds
	// a Decimal(10,2), which is how the sixteen numeric -> Decimal disagreements
	// were found — so what is missing is only their use as a SOURCE.
	expectedUnsampled := map[string]bool{
		"Array": true, "Categorical": true, "Decimal": true, "Enum": true,
		"Int128": true, "List": true, "Struct": true, "Uint128": true,
	}
	for name := range unsampled {
		if !expectedUnsampled[name] {
			t.Errorf("%s has no sample column and is not in expectedUnsampled — "+
				"add one, or record why it cannot exist", name)
		}
	}
	for name := range expectedUnsampled {
		if !unsampled[name] {
			t.Errorf("%s is listed as unsamplable but now has a sample — delete "+
				"the entry so the list cannot go stale", name)
		}
	}
}
