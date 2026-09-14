package kernel_test

// A Null OPERAND, asserted on values rather than on types.
//
// The defect that opened step 55 produced the CORRECT type. `not` on a Null column
// returned a Bool column, which is what ResolveUnary promised and what every
// type-level assertion in the repository would have accepted — over zero rows, from
// an n-row input, with a nil error. So every assertion here is about the shape and
// the contents, and the type check is the least of them.

import (
	"errors"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

const nullRows = 3

func nullCol(t *testing.T) *data.Column {
	t.Helper()
	return data.NewNull("nu", dtype.Null, nullRows)
}

// runUnary resolves and applies, so the test cannot drift from what the evaluator
// does: physical.evalUnary is these same two calls.
func runUnary(t *testing.T, op expr.UnaryOp, c *data.Column) (*data.Column, error) {
	t.Helper()
	out, err := expr.ResolveUnary(op, c.DType())
	if err != nil {
		return nil, err
	}
	return kernel.Unary(op, "r", out, c)
}

// TestNullOperandIsNullInNullOut covers the five ops that ResolveUnary admits for a
// Null operand and promises Bool for.
//
// Both answers are written down elsewhere and neither was implemented: op.go says
// "Kleene NOT: NOT null is null", and three separate files say "null.is_nan() is
// NULL, not false, because a missing value is not a float that could be tested".
func TestNullOperandIsNullInNullOut(t *testing.T) {
	ops := []expr.UnaryOp{
		expr.OpNot, expr.OpIsNan, expr.OpIsNotNan, expr.OpIsFinite, expr.OpIsInfinite,
	}
	for _, op := range ops {
		t.Run(op.String(), func(t *testing.T) {
			got, err := runUnary(t, op, nullCol(t))
			if err != nil {
				t.Fatalf("%s(null) must be null, not an error: %v", op, err)
			}
			if got.DType() != dtype.Bool {
				t.Errorf("type is %s, want Bool", got.DType())
			}
			// The one that was wrong. bitmap.Not is length-driven and a Null column
			// has no payload bitmap, so this was 0 for as long as the file existed.
			if got.Len() != nullRows {
				t.Fatalf("%d rows for a %d-row operand — the row count came from an "+
					"empty payload bitmap", got.Len(), nullRows)
			}
			if got.NullCount() != nullRows {
				t.Errorf("%d of %d rows are null; every one must be", got.NullCount(), nullRows)
			}
			// It must be an OPERAND, not only an answer: take.go's rule is that
			// data.NewNull carries no buffer and anything that gathers from it
			// fails. NullColumn is what makes the difference, and asserting the
			// type alone cannot tell the two apart.
			if got.IsPayloadFree() {
				t.Error("the result has no payload buffer, so Take and Concat will " +
					"refuse it — data.NewNull was used where NullColumn belongs")
			}
		})
	}
}

// TestNullPredicatesStayTotalOverANullColumn is the other half, and it is why the
// kernel's Null arm cannot simply cover every op.
//
// is_null and is_not_null are the only operations whose result is never null,
// "because the null-ness IS the answer". Handing them n nulls would be the exact
// inverse of the bug above: a correct type, a correct row count, and every value
// wrong.
func TestNullPredicatesStayTotalOverANullColumn(t *testing.T) {
	cases := []struct {
		op   expr.UnaryOp
		want bool
	}{
		{expr.OpIsNull, true},
		{expr.OpIsNotNull, false},
	}
	for _, c := range cases {
		t.Run(c.op.String(), func(t *testing.T) {
			got, err := runUnary(t, c.op, nullCol(t))
			if err != nil {
				t.Fatal(err)
			}
			if got.Len() != nullRows {
				t.Fatalf("%d rows, want %d", got.Len(), nullRows)
			}
			if got.NullCount() != 0 {
				t.Errorf("%d null rows; a null predicate is total and answers for "+
					"every row", got.NullCount())
			}
			bits := got.Bools()
			for i := range nullRows {
				if bits.Get(i) != c.want {
					t.Fatalf("row %d is %v, want %v", i, bits.Get(i), c.want)
				}
			}
		})
	}
}

// TestNumericUnaryRefusesNullAtPlanTime pins the narrowing.
//
// These nine used to be admitted by ResolveUnary and implemented by nothing:
// sign/floor/ceil promised Null and unaryArith had no case for it, and the six maths
// ops promised Float64 and toFloat64 could not widen it. neg and abs — the same
// family, thirty lines away — refused it all along, so one switch answered one
// question three ways.
//
// The refusal must be a USER error. Six of the nine used to surface as Internalf,
// which tells the user that an expression the planner accepted is a bug in ursus.
// errors.Is is not enough to see that: it walks the cause chain, so a KindType cause
// under a KindInternal wrapper matches ErrType. The top-level kind is the assertion.
func TestNumericUnaryRefusesNullAtPlanTime(t *testing.T) {
	ops := []expr.UnaryOp{
		expr.OpNeg, expr.OpAbs, expr.OpSign, expr.OpFloor, expr.OpCeil,
		expr.OpSqrt, expr.OpCbrt, expr.OpExp, expr.OpLn, expr.OpLog10, expr.OpLog1p,
	}
	for _, op := range ops {
		t.Run(op.String(), func(t *testing.T) {
			_, err := expr.ResolveUnary(op, dtype.Null)
			if err == nil {
				t.Fatalf("%s(null) resolved; arithmetic on a value that is not there "+
					"has no answer, and neg and abs have always said so", op)
			}
			var e *uerr.Error
			if !errors.As(err, &e) || e.Kind != uerr.KindType {
				t.Errorf("%s(null) is %v, want a top-level KindType — the user asked "+
					"for something that is not defined, which is not a bug in ursus", op, err)
			}
		})
	}
}

// TestNullComparisonAnswersTheWayOpGoSaysItDoes.
//
// Two Null operands promote to Null, equality needs no ordering, so the binding is
// CastL = CastR = Null with Out Bool and nothing casts. dispatchCompare refused the
// whole family after Field had promised Bool.
//
// The two families answer DIFFERENTLY and that is the point of the pair:
//
//	==  and !=   three-valued, so null == null is NULL
//	<=> and <!>  nulls are data, so null <=> null is TRUE and never null
//
// A fix that gave all four the same answer would satisfy every type assertion and
// be wrong about half of them.
func TestNullComparisonAnswersTheWayOpGoSaysItDoes(t *testing.T) {
	cases := []struct {
		op        expr.BinaryOp
		wantNull  bool
		wantValue bool // only read when wantNull is false
	}{
		{expr.OpEq, true, false},
		{expr.OpNe, true, false},
		{expr.OpEqMissing, false, true},
		{expr.OpNeMissing, false, false},
	}
	for _, c := range cases {
		t.Run(c.op.String(), func(t *testing.T) {
			l, r := nullCol(t), nullCol(t)
			bind, err := expr.ResolveBinary(c.op, l.DType(), r.DType())
			if err != nil {
				t.Fatalf("%s(null,null) must resolve: %v", c.op, err)
			}
			got, err := kernel.Binary(c.op, "r", bind.Out, l, r)
			if err != nil {
				t.Fatalf("%s(null,null): %v", c.op, err)
			}
			if got.DType() != dtype.Bool || got.Len() != nullRows {
				t.Fatalf("got %s over %d rows, want Bool over %d",
					got.DType(), got.Len(), nullRows)
			}
			if c.wantNull {
				if got.NullCount() != nullRows {
					t.Errorf("%d of %d rows are null; three-valued comparison with a "+
						"null operand is null in every row", got.NullCount(), nullRows)
				}
				return
			}
			if got.NullCount() != 0 {
				t.Fatalf("%d null rows; %s treats nulls as data and its result is "+
					"never null", got.NullCount(), c.op)
			}
			bits := got.Bools()
			for i := range nullRows {
				if bits.Get(i) != c.wantValue {
					t.Fatalf("row %d of %s(null,null) is %v, want %v",
						i, c.op, bits.Get(i), c.wantValue)
				}
			}
		})
	}
}
