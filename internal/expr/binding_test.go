package expr_test

// Every arithmetic binding must be physically coherent.
//
// kernel.arithmetic dispatches on Binding.Out.Physical() and then reads BOTH
// operand columns at that width. So a binding where Out.Physical() differs from
// CastL.Physical() or CastR.Physical() is a guaranteed runtime refusal — the
// planner accepts the query, CollectSchema and Explain report a type, and Collect
// fails with "cannot be read as".
//
// That makes the defect MECHANICAL, and checkable here, at resolution, with no
// evaluator, no kernel and no fixture. Four instances shipped: Date - Date,
// Date ± Duration and Duration + Date all bound an Int32 Date against an Int64
// Duration. TestEvaluatorContract found them by executing the matrix; this finds
// the same class by reading the binding, which is cheaper and earlier.
//
// It is total over the type cross-product rather than over a list of arms, so an
// arm added later is covered the day it is written.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// bindingTypes is one representative of every family ResolveBinary branches on,
// including BOTH Datetime zones and both a Date and a Time — the distinctions that
// the temporal algebra actually turns on.
var bindingTypes = []dtype.DataType{
	dtype.Bool,
	dtype.Int8, dtype.Int32, dtype.Int64,
	dtype.Uint32, dtype.Uint64,
	dtype.Float32, dtype.Float64,
	dtype.String,
	dtype.Date,
	dtype.Datetime(dtype.Second, "UTC"),
	dtype.Datetime(dtype.Nano, "UTC"),
	dtype.Datetime(dtype.Second, ""),
	dtype.Time(dtype.Micro),
	dtype.Duration(dtype.Second),
	dtype.Duration(dtype.Nano),
	dtype.Decimal(10, 2),
}

var bindingOps = []expr.BinaryOp{
	expr.OpAdd, expr.OpSub, expr.OpMul, expr.OpDiv, expr.OpFloorDiv, expr.OpMod,
	expr.OpPow,
	expr.OpEq, expr.OpNe, expr.OpLt, expr.OpLe, expr.OpGt, expr.OpGe,
	expr.OpEqMissing, expr.OpNeMissing,
	expr.OpAnd, expr.OpOr, expr.OpXor,
}

// TestBindingsArePhysicallyCoherent walks the whole cross-product.
func TestBindingsArePhysicallyCoherent(t *testing.T) {
	var checked int

	for _, op := range bindingOps {
		for _, l := range bindingTypes {
			for _, r := range bindingTypes {
				b, err := expr.ResolveBinary(op, l, r)
				if err != nil {
					continue // a refusal is always coherent
				}
				checked++

				// A comparison's output is Bool while its operands are whatever
				// they were, so the rule applies to the OPERANDS agreeing with
				// each other; arithmetic additionally requires the output to
				// agree, because that is what the kernel dispatches on.
				lp, rp := b.CastL.Physical(), b.CastR.Physical()
				if lp != rp {
					t.Errorf("%s(%s, %s): operands bind to %s and %s — the kernel "+
						"reads both at one width, so one of them is misread",
						op, l, r, b.CastL, b.CastR)
					continue
				}
				if op.IsComparison() || op.IsMissingComparison() || op.IsLogical() {
					continue
				}
				if op2 := b.Out.Physical(); op2 != lp {
					t.Errorf("%s(%s, %s): output %s is physically %s but the "+
						"operands bind to %s — kernel.arithmetic dispatches on the "+
						"OUTPUT and would read the operands at the wrong width",
						op, l, r, b.Out, op2, b.CastL)
				}
			}
		}
	}

	// Anti-vacuity. 18 ops x 17 x 17 is 5202 combinations; if ResolveBinary began
	// refusing everything the loop would still complete and assert nothing.
	if checked < 800 {
		t.Fatalf("only %d bindings resolved — too few for this matrix to mean "+
			"anything", checked)
	}
}
