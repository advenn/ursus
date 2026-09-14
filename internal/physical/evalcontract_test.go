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
		// A NAIVE datetime and a Time, neither of which the matrix had. Their
		// absence is why it could not see that arithmetic and comparison disagreed
		// about zones: with one Datetime column there is no pair to disagree about.
		dtype.Of("tn", dtype.Datetime(dtype.Micro, "")),
		dtype.Of("tm", dtype.Time(dtype.Nano)),
		// A NULL column, which the matrix had no way to reach. ResolveUnary admits
		// Null for eleven ops and kernel.Unary implements none of them; the row-count
		// assertion below was already written and could not fire, because the unit of
		// coverage is the fixture and this fixture had sixteen types and not this one.
		//
		// It is also the only column here with no payload buffer at all — data.NewNull
		// carries validity and nothing else — so it is the only one that tests what a
		// kernel does when there is nothing to read.
		dtype.Of("nu", dtype.Null),
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
		data.NewFixed("tn", dtype.Datetime(dtype.Micro, ""), []int64{1, 2, 3}, valid(n)),
		data.NewFixed("tm", dtype.Time(dtype.Nano), []int64{1, 2, 3}, valid(n)),
		data.NewNull("nu", dtype.Null, n),
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
	"f32", "f64", "bo", "st", "dt", "ts", "du", "tn", "tm", "nu",
}

// The op lists are DERIVED, not written down.
//
// They used to be hand-written, and a hand-written list of an enum goes quiet
// exactly the way an allow-list does. Three declared ops were missing from it —
// OpPow, OpLog10 and OpLog1p — so `**`, `log10` and `log1p` had never once been
// through this matrix, and nothing could say so.
//
// Both String() methods return "?" for an undeclared value and are sized by their
// enum's own sentinel, which makes the range walk total: every op that exists is
// covered, and one added tomorrow is covered the day it is named. Same trick as
// AggOp's coverage check in step 51, and it is available here only because op.go
// fixed the empty-string fallback that would have made "?" unreachable.
var (
	contractBinaryOps = allBinaryOps()
	contractUnaryOps  = allUnaryOps()
)

func allBinaryOps() []expr.BinaryOp {
	var ops []expr.BinaryOp
	for i := range 256 {
		if op := expr.BinaryOp(i); op.String() != "?" {
			ops = append(ops, op)
		}
	}
	return ops
}

func allUnaryOps() []expr.UnaryOp {
	var ops []expr.UnaryOp
	for i := range 256 {
		if op := expr.UnaryOp(i); op.String() != "?" {
			ops = append(ops, op)
		}
	}
	return ops
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

	// fail records a gap. A LISTED gap is expected, so it is silent; an unlisted one
	// is the failure this test exists for. Routing every assertion through here is
	// what lets the ratchet cover the SHAPE checks below and not only the
	// Field/Eval switch — which it did not, and `not(nu)` is why that mattered: both
	// halves succeeded, the promised type was right, and the row count was zero.
	agreed := true
	fail := func(format string, args ...any) {
		t.Helper()
		agreed = false
		if !known {
			t.Errorf(format, args...)
		}
	}

	switch {
	case ferr != nil && eerr != nil:
		if known {
			t.Errorf("%s is listed in knownContractGaps but Field and Eval now "+
				"agree — delete the entry", label)
		}
		return false // agreed refusal; nothing to compare
	case ferr != nil && eerr == nil:
		fail("%s: Field refused (%v) but Eval produced %s — the planner would "+
			"reject a query the engine can run", label, ferr, c.DType())
		return false
	case ferr == nil && eerr != nil:
		fail("%s: Field promised %s but Eval failed: %v — CollectSchema and "+
			"Explain would report a plan that cannot run", label, f.Type, eerr)
		return false
	}

	if c.DType() != f.Type {
		fail("%s: Field promised %s, Eval produced %s", label, f.Type, c.DType())
	}
	// The SHAPE half. A kernel that returns the promised type over the wrong number
	// of rows has satisfied the type contract and broken the batch, and the type
	// contract is the half that gets read. Length 1 is the literal broadcast.
	if c.Len() != b.Rows() && c.Len() != 1 {
		fail("%s: produced %d rows for a %d-row batch", label, c.Len(), b.Rows())
	}
	if known && agreed {
		t.Errorf("%s is listed in knownContractGaps but now agrees — delete the entry",
			label)
	}
	return true
}

// knownContractGaps is EMPTY, and that is the result of step 52.
//
// It held ten entries when this test was written: four Date arithmetic bindings
// that mixed an Int32 Date with an Int64 Duration, and six Bool <-> temporal casts
// where CanCast and the kernel disagreed.
//
// All ten are fixed, and both classes now have a check that is total rather than a
// list — expr's TestBindingsArePhysicallyCoherent over the binding cross-product,
// and kernel's TestCanCastAgreesWithTheKernel over every TypeID pair. The second
// found seventeen MORE disagreements than this ratchet ever recorded, including
// sixteen in the dangerous direction, because this fixture has no Decimal column.
//
// It is kept, empty, because the mechanism is the useful part: an entry asserts a
// gap still EXISTS, so fixing one without deleting its line fails the test. That is
// the property a plain allow-list does not have.
//
// # It held twenty-two again, for one commit, and is empty again
//
// Adding one Null column produced all twenty-two. They were checked in BEFORE the
// fix, in their own commit, because a list of gaps written down is evidence and a
// list of gaps described is a claim — the same reason step 54 widened its fixtures
// in a commit of their own. Read `git show` on the commit below this one to see
// them.
//
// Fourteen were unary: ResolveUnary admitted Null for eleven ops and kernel.Unary
// implemented none of them, `not` by returning a ZERO-ROW Bool column with a nil
// error. Eight were the equality family against a Null operand, which
// dispatchCompare refused after Field had promised Bool. Both classes are closed,
// not exempted.
var knownContractGaps = map[string]string{}

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
		// Anti-vacuity, on both axes. The op list is derived, so a String() that
		// stopped returning "?" would shrink it silently; the column list is
		// written down, so it can only shrink by hand. Then the count: if
		// promotion ever starts refusing everything, the loop still completes and
		// every assertion is skipped, and a count is what sees that.
		if len(contractBinaryOps) < 18 || len(contractCols) < 17 {
			t.Fatalf("the matrix is %d ops × %d columns, which is smaller than it "+
				"has ever been", len(contractBinaryOps), len(contractCols))
		}
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
		if len(contractUnaryOps) < 18 {
			t.Fatalf("only %d unary ops derived; the enum declares more",
				len(contractUnaryOps))
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

	// A literal against every column: the broadcast path, where one operand is a
	// length-1 column and the result must still be batch-height.
	//
	// The typed literal used to be `&expr.Lit{Value: int64(2)}` with DT left at its
	// zero value — which is dtype.Null. litNode NEVER builds that: Lit.Field reports
	// DT while litColumn switches on Value, so such a node DECLARES Null and
	// EVALUATES to Int64, and the matrix survived it only because Promote(T, Null)
	// is T for every column it had. Against a Null column there is no T to adopt,
	// and it surfaced as four failures belonging to the fixture rather than to the
	// engine. Both literals below are now what the public surface actually builds.
	t.Run("binary against a literal", func(t *testing.T) {
		lits := []struct {
			label string
			node  *expr.Lit
		}{
			{"lit", &expr.Lit{Value: int64(2), DT: dtype.Int64}}, // ursus.Lit(int64(2))
			{"null", &expr.Lit{Value: nil, DT: dtype.Null}},      // ursus.Null(ursus.NullT)
		}
		var ran int
		for _, op := range contractBinaryOps {
			for _, c := range contractCols {
				for _, l := range lits {
					n := &expr.Binary{Op: op, L: &expr.Col{Name: c}, R: l.node}
					if checkContract(t, n, b, op.String()+"("+c+","+l.label+")") {
						ran++
					}
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
