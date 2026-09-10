package physical

// The evaluator contract, asserted rather than claimed.
//
// Eval's doc has always said:
//
//	Eval(ctx, n, b).DType() == n.Field(b.Schema()).Type
//
// and that "TestEvaluatorContract asserts it across the whole op × dtype matrix".
// No such test existed. Repo-wide, the only occurrence of that name was the sentence
// promising it — the third time this project has cited an instrument that was never
// built, and in the one place where the contract is most load-bearing: it is what
// makes CollectSchema honest, because a schema promised before any data is read is a
// lie if the evaluator can produce a different type.
//
// It is also the guard a UDF most needs. Step 50 made the user the author of both
// sides of that equality, and evalUDF checks its half; this checks everyone else's.

import (
	"context"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// contractBatch is one column per dtype worth exercising, all length 3, all with a
// materialised validity bitmap so no kernel takes a no-nulls shortcut by accident.
func contractBatch(t *testing.T) *data.Batch {
	t.Helper()

	valid := func(n int) bitmap.View {
		b := bitmap.NewBuilder(n)
		for range n {
			b.Append(true)
		}
		return b.Finish()
	}
	const n = 3

	fields := []dtype.Field{
		dtype.Of("i8", dtype.Int8), dtype.Of("i16", dtype.Int16),
		dtype.Of("i32", dtype.Int32), dtype.Of("i64", dtype.Int64),
		dtype.Of("u8", dtype.Uint8), dtype.Of("u32", dtype.Uint32),
		dtype.Of("u64", dtype.Uint64),
		dtype.Of("f32", dtype.Float32), dtype.Of("f64", dtype.Float64),
		dtype.Of("bo", dtype.Bool), dtype.Of("st", dtype.String),
		dtype.Of("dt", dtype.Date), dtype.Of("ts", dtype.Datetime(dtype.Micro, "UTC")),
		dtype.Of("du", dtype.Duration(dtype.Nano)),
	}
	cols := []*data.Column{
		data.NewFixed("i8", dtype.Int8, []int8{1, 2, 3}, valid(n)),
		data.NewFixed("i16", dtype.Int16, []int16{1, 2, 3}, valid(n)),
		data.NewFixed("i32", dtype.Int32, []int32{1, 2, 3}, valid(n)),
		data.NewFixed("i64", dtype.Int64, []int64{1, 2, 3}, valid(n)),
		data.NewFixed("u8", dtype.Uint8, []uint8{1, 2, 3}, valid(n)),
		data.NewFixed("u32", dtype.Uint32, []uint32{1, 2, 3}, valid(n)),
		data.NewFixed("u64", dtype.Uint64, []uint64{1, 2, 3}, valid(n)),
		data.NewFixed("f32", dtype.Float32, []float32{1, 2, 3}, valid(n)),
		data.NewFixed("f64", dtype.Float64, []float64{1, 2, 3}, valid(n)),
		data.NewBool("bo", valid(n), valid(n)),
		// Numeric text, so a strict cast to a numeric type fails on the TYPE
		// rules or not at all. "a" would fail on the VALUE, which is correct
		// behaviour and not a contract violation — the contract is about types.
		data.NewString("st", []string{"1", "2", "3"}, valid(n)),
		data.NewFixed("dt", dtype.Date, []int32{1, 2, 3}, valid(n)),
		data.NewFixed("ts", dtype.Datetime(dtype.Micro, "UTC"), []int64{1, 2, 3}, valid(n)),
		data.NewFixed("du", dtype.Duration(dtype.Nano), []int64{1, 2, 3}, valid(n)),
	}
	schema, err := dtype.NewSchema(fields...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(schema, cols)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var contractCols = []string{
	"i8", "i16", "i32", "i64", "u8", "u32", "u64",
	"f32", "f64", "bo", "st", "dt", "ts", "du",
}

var contractBinaryOps = []expr.BinaryOp{
	expr.OpAdd, expr.OpSub, expr.OpMul, expr.OpDiv, expr.OpFloorDiv, expr.OpMod,
	expr.OpEq, expr.OpNe, expr.OpLt, expr.OpLe, expr.OpGt, expr.OpGe,
	expr.OpEqMissing, expr.OpNeMissing, expr.OpAnd, expr.OpOr, expr.OpXor,
}

var contractUnaryOps = []expr.UnaryOp{
	expr.OpNeg, expr.OpAbs, expr.OpNot, expr.OpIsNull, expr.OpIsNotNull,
	expr.OpIsNan, expr.OpIsNotNan, expr.OpIsFinite, expr.OpIsInfinite,
	expr.OpSqrt, expr.OpCbrt, expr.OpExp, expr.OpLn, expr.OpSign,
	expr.OpFloor, expr.OpCeil,
}

var contractCastTargets = []dtype.DataType{
	dtype.Int8, dtype.Int32, dtype.Int64, dtype.Uint32, dtype.Uint64,
	dtype.Float32, dtype.Float64, dtype.Bool, dtype.String,
	dtype.Date, dtype.Datetime(dtype.Nano, "UTC"), dtype.Duration(dtype.Second),
}

// checkContract is the assertion, and it has TWO halves.
//
// If Field succeeds, Eval must succeed and produce exactly that type. If Field
// FAILS, Eval must fail too — otherwise the planner refuses a query the engine could
// have run, or worse, Explain reports a refusal for something Collect would accept.
// Field and Eval have to agree about what is legal, not merely about types.
func checkContract(t *testing.T, n expr.Node, b *data.Batch, label string) (ran bool) {
	t.Helper()

	f, ferr := n.Field(b.Schema())
	c, eerr := Eval(context.Background(), n, b)

	_, known := knownContractGaps[label]
	if known {
		seenContractGaps[label] = true
	}

	switch {
	case ferr != nil && eerr != nil:
		if known {
			t.Errorf("%s is listed in knownContractGaps but Field and Eval now "+
				"agree — delete the entry", label)
		}
		return false // agreed refusal; nothing to compare
	case ferr != nil && eerr == nil:
		if known {
			return false
		}
		t.Errorf("%s: Field refused (%v) but Eval produced %s — the planner would "+
			"reject a query the engine can run", label, ferr, c.DType())
		return false
	case ferr == nil && eerr != nil:
		if known {
			return false
		}
		t.Errorf("%s: Field promised %s but Eval failed: %v — CollectSchema and "+
			"Explain would report a plan that cannot run", label, f.Type, eerr)
		return false
	}
	if known {
		t.Errorf("%s is listed in knownContractGaps but now agrees — delete the entry",
			label)
	}

	if c.DType() != f.Type {
		t.Errorf("%s: Field promised %s, Eval produced %s", label, f.Type, c.DType())
	}
	if c.Len() != b.Rows() && c.Len() != 1 {
		t.Errorf("%s: produced %d rows for a %d-row batch", label, c.Len(), b.Rows())
	}
	return true
}

// knownContractGaps is where Field and Eval currently DISAGREE, with the mechanism.
//
// It is a ratchet, not an allow-list. Every entry is asserted to still be a gap, so
// fixing one FAILS this test until the entry is removed — the property every other
// list in this repository lacks, and the reason they go quiet instead of failing.
//
// # The six casts: CanCast and the cast kernel disagree
//
// Cast.Field consults dtype.CanCast, which refuses Bool ↔ temporal. kernel.Cast
// performs it anyway, by physical punning. The direction is the safe one — the
// planner refuses, so no query reaches the kernel — and it is the same standing item
// already recorded for numeric ↔ Decimal. The fix is one guard in kernel.Cast so the
// two agree by construction rather than by coincidence.
//
// # The four Date cases: a real binding bug, and a semantic question under it
//
// Date - Date binds CastL/CastR to Date (physical Int32) and Out to Duration(s)
// (physical Int64). kernel.arithmetic dispatches on Out.Physical(), so the kernel
// reads Int32 storage as Int64 and refuses.
//
// Widening the cast is not the whole fix, because Date counts DAYS and Duration(s)
// counts seconds: making the widths agree would turn a type error into an answer
// wrong by 86400. That is a semantic decision about Date's unit, which is its own
// step. resolveTemporalArithmetic's own comment already records that this area went
// untested — "nothing caught it because no test touched temporal arithmetic".
var knownContractGaps = map[string]string{
	"cast(bo->Date)":              "CanCast refuses Bool->Date; kernel.Cast puns it",
	"cast(bo->Datetime(ns, UTC))": "CanCast refuses Bool->Datetime; kernel.Cast puns it",
	"cast(bo->Duration(s))":       "CanCast refuses Bool->Duration; kernel.Cast puns it",
	"cast(dt->Bool)":              "CanCast refuses Date->Bool; kernel.Cast puns it",
	"cast(du->Bool)":              "CanCast refuses Duration->Bool; kernel.Cast puns it",
	"cast(ts->Bool)":              "CanCast refuses Datetime->Bool; kernel.Cast puns it",
	"-(dt,dt)":                    "Date is Int32, Duration(s) is Int64; and days vs seconds",
	"+(dt,du)":                    "Date is Int32, Duration is Int64; and days vs seconds",
	"-(dt,du)":                    "Date is Int32, Duration is Int64; and days vs seconds",
	"+(du,dt)":                    "Date is Int32, Duration is Int64; and days vs seconds",
}

var seenContractGaps = map[string]bool{}

// TestEvaluatorContract walks the op × dtype matrix the doc has promised since the
// evaluator was written.
func TestEvaluatorContract(t *testing.T) {
	b := contractBatch(t)

	t.Run("binary", func(t *testing.T) {
		var ran int
		for _, op := range contractBinaryOps {
			for _, l := range contractCols {
				for _, r := range contractCols {
					n := &expr.Binary{Op: op, L: &expr.Col{Name: l}, R: &expr.Col{Name: r}}
					if checkContract(t, n, b, op.String()+"("+l+","+r+")") {
						ran++
					}
				}
			}
		}
		// Anti-vacuity. 17 ops × 14 × 14 is 3332 combinations; if promotion ever
		// starts refusing everything, the loop still completes and every assertion
		// is skipped. A count that cannot see that is no guard at all.
		if ran < 500 {
			t.Errorf("only %d binary combinations resolved — too few for this "+
				"matrix to mean anything", ran)
		}
	})

	t.Run("unary", func(t *testing.T) {
		var ran int
		for _, op := range contractUnaryOps {
			for _, c := range contractCols {
				n := &expr.Unary{Op: op, Child: &expr.Col{Name: c}}
				if checkContract(t, n, b, op.String()+"("+c+")") {
					ran++
				}
			}
		}
		if ran < 50 {
			t.Errorf("only %d unary combinations resolved", ran)
		}
	})

	t.Run("cast", func(t *testing.T) {
		var ran int
		for _, to := range contractCastTargets {
			for _, c := range contractCols {
				// NON-STRICT only. A strict cast may legitimately fail on a
				// VALUE — "2" is not a Bool, 1<<40 is not an Int8 — and that is
				// correct behaviour, not a Field/Eval disagreement. The contract
				// is about types, so this arm asks only about types; the strict
				// path's value refusals are cast_test.go's business.
				for _, strict := range []bool{false} {
					n := &expr.Cast{Child: &expr.Col{Name: c}, To: to, Strict: strict}
					if checkContract(t, n, b, "cast("+c+"->"+to.String()+")") {
						ran++
					}
				}
			}
		}
		if ran < 100 {
			t.Errorf("only %d cast combinations resolved", ran)
		}
	})

	// A literal against every column: the broadcast path, and the one where weak
	// literal typing decides the result type rather than the operands alone.
	t.Run("binary against a literal", func(t *testing.T) {
		var ran int
		for _, op := range contractBinaryOps {
			for _, c := range contractCols {
				n := &expr.Binary{
					Op: op, L: &expr.Col{Name: c}, R: &expr.Lit{Value: int64(2)}}
				if checkContract(t, n, b, op.String()+"("+c+",lit)") {
					ran++
				}
			}
		}
		if ran < 50 {
			t.Errorf("only %d literal combinations resolved", ran)
		}
	})

	// Every known gap must have been REACHED. An entry the matrix no longer
	// produces is a stale exemption, and a stale exemption is how a list goes
	// quiet — the thing this file exists to prevent.
	for label := range knownContractGaps {
		if !seenContractGaps[label] {
			t.Errorf("knownContractGaps lists %q, but the matrix never produced it — "+
				"the entry is stale, or the fixture stopped covering it", label)
		}
	}
}
