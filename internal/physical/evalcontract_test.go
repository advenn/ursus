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
	"time"

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

// allCallFns cannot use the `!= "?"` trick above, because CallFn.String() falls back
// to "call(N)" rather than "?". That fallback is the BETTER of the two — two unnamed
// constants render differently and so cannot collide in the three maps that dedup on
// String() — so the derivation moves rather than the enum.
//
// The six classifiers are exported and are range tests over contiguous blocks, which
// makes this total by construction: `IsString()` is `f < fnStrEnd`, so every declared
// string function is claimed and nothing between the families is.
//
// The values are NOT at round offsets. iota counts ConstSpecs across the whole block,
// so FnDtYear is 127, FnIsIn 247, FnMathRound 349, FnListLen 451 and FnStructField
// 500. A driver that walked 0..120 would find twenty-six functions and report nothing
// wrong, which is the failure this file exists to prevent.
func allCallFns() []expr.CallFn {
	var fns []expr.CallFn
	for i := range 600 {
		fn := expr.CallFn(i)
		if fn.IsString() || fn.IsTemporal() || fn.IsGeneral() ||
			fn.IsMath() || fn.IsList() || fn.IsStruct() {
			fns = append(fns, fn)
		}
	}
	return fns
}

// callArgSet is one argument tuple to drive a call with, and a label suffix for the
// calls that need more than one.
type callArgSet struct {
	label string
	args  []any
}

// contractCallArgs supplies the literal arguments each call needs.
//
// # Some calls need TWO sets, and that is the whole point
//
// A matrix that drives one fixed argument per call asks a smaller question than it
// looks like it is asking. The first divergence this arm found is invisible to one:
// dt.truncate takes an interval, and `Truncate(time.Hour)` on a Time column works
// while `Truncate(Every("1mo"))` does not — the kernel routes on iv.IsCalendar() and
// refuses a calendar grid on a clock with no date. One set sees one of those.
//
// The five flag-bearing string calls are the same shape one layer down: the trailing
// `literal` flag decides whether CompilePattern compiles a regex at all, so a single
// literal=true table would leave every regex path in the family unexercised.
//
// Values are chosen to be legal for the fixture rather than interesting: "1" is a
// string that actually occurs in the `st` column, and a regex "1" compiles.
var contractCallArgs = map[expr.CallFn][]callArgSet{
	// string, one literal argument
	expr.FnStrStartsWith:      {{args: []any{"1"}}},
	expr.FnStrEndsWith:        {{args: []any{"1"}}},
	expr.FnStrStripChars:      {{args: []any{" "}}},
	expr.FnStrStripCharsStart: {{args: []any{" "}}},
	expr.FnStrStripCharsEnd:   {{args: []any{" "}}},
	expr.FnStrStripPrefix:     {{args: []any{"1"}}},
	expr.FnStrStripSuffix:     {{args: []any{"1"}}},
	expr.FnStrSplit:           {{args: []any{","}}},

	// string, regex-only: there is no literal form, so there is one set
	expr.FnStrExtract:    {{args: []any{"1", int64(0)}}},
	expr.FnStrExtractAll: {{args: []any{"1"}}},

	// string, pattern + the literal flag that decides whether a regex is compiled
	expr.FnStrContains: {
		{label: ",literal", args: []any{"1", true}},
		{label: ",regex", args: []any{"1", false}},
	},
	expr.FnStrFind: {
		{label: ",literal", args: []any{"1", true}},
		{label: ",regex", args: []any{"1", false}},
	},
	expr.FnStrCountMatches: {
		{label: ",literal", args: []any{"1", true}},
		{label: ",regex", args: []any{"1", false}},
	},
	expr.FnStrReplace: {
		{label: ",literal", args: []any{"1", "x", true}},
		{label: ",regex", args: []any{"1", "x", false}},
	},
	expr.FnStrReplaceAll: {
		{label: ",literal", args: []any{"1", "x", true}},
		{label: ",regex", args: []any{"1", "x", false}},
	},

	// string, numeric arguments
	expr.FnStrSlice:    {{args: []any{int64(0), int64(1)}}},
	expr.FnStrSplitN:   {{args: []any{",", int64(2)}}},
	expr.FnStrZFill:    {{args: []any{int64(4)}}},
	expr.FnStrPadStart: {{args: []any{int64(4), " "}}},
	expr.FnStrPadEnd:   {{args: []any{int64(4), " "}}},

	// The interval, as three int64s: months, days, nanoseconds.
	expr.FnDtTruncate: {
		{label: ",nanos", args: []any{int64(0), int64(0), int64(time.Hour)}},
		{label: ",calendar", args: []any{int64(1), int64(0), int64(0)}},
		// Zero and negative intervals are refused by the kernel, and both are
		// reachable from the public API: FromDuration does not validate, so
		// `Truncate(time.Duration(0))` and `Truncate(-time.Hour)` carry no error out
		// of the builder. They are CONSTANTS of the expression, not data, so unlike a
		// strict cast's value refusal the planner can see them — which makes them this
		// test's business rather than an exemption.
		{label: ",zero", args: []any{int64(0), int64(0), int64(0)}},
		{label: ",negative", args: []any{int64(0), int64(0), int64(-time.Hour)}},
	},

	expr.FnMathRound: {{args: []any{int64(1)}}},

	// The list and struct families are unreachable against this fixture, which has
	// no List and no Struct column — every combination is an agreed refusal. The
	// arguments are here so that the day a List column is added the arm drives them
	// correctly rather than with a short slice, which listSort would index bare.
	expr.FnListGet:      {{args: []any{int64(0)}}},
	expr.FnListHead:     {{args: []any{int64(1)}}},
	expr.FnListTail:     {{args: []any{int64(1)}}},
	expr.FnListSlice:    {{args: []any{int64(0), int64(1)}}},
	expr.FnListSort:     {{args: []any{false}}},
	expr.FnListContains: {{args: []any{int64(1)}}},
	expr.FnStructField:  {{args: []any{"f"}}},
}

// noCallArgs is the default: one set, no arguments. is_in takes it deliberately —
// see the comment in the subtest.
var noCallArgs = []callArgSet{{}}

func contractArgSets(fn expr.CallFn) []callArgSet {
	if sets, ok := contractCallArgs[fn]; ok {
		return sets
	}
	return noCallArgs
}

// callLit builds a literal argument the way litNode does, DT and all.
//
// Setting DT matters less here than it does for an operand — CallArgs reads Value and
// ignores DT — but a fixture that builds a node the public API cannot build is how the
// literal arm of this matrix spent its whole life asserting the wrong thing.
func callLit(v any) expr.Node {
	switch x := v.(type) {
	case string:
		return &expr.Lit{Value: x, DT: dtype.String}
	case int64:
		return &expr.Lit{Value: x, DT: dtype.Int64}
	case bool:
		return &expr.Lit{Value: x, DT: dtype.Bool}
	default:
		panic("contractCallArgs holds a value callLit cannot type")
	}
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
//
// # Step 56: nine, and every one of them is dt.truncate
//
// The call arm below covers 62 functions over 17 receivers and found exactly one
// broken function — which is a far better result than the operator surface gave, and
// worth saying rather than burying. But that one is broken three ways, and all three
// have the same cause: ResolveCall takes the whole Call node precisely so a call's
// output can depend on an ARGUMENT, and dtCallOut throws the arguments away.
//
// Checked in before the fix, as step 55's Null column was.
var knownContractGaps = map[string]string{
	// The interval decides which kernel runs. truncateTemporal routes on
	// iv.IsCalendar(), and truncateCalendar refuses a Time — "a Time has no date, so
	// it cannot be floored to a day or a month" — while the nanosecond path on the
	// same column works. dtCallOut sees only the receiver, so it promises `in` for
	// both and Explain prints a plan that runs for one interval and not the other.
	"dt.truncate(tm,calendar)": "dtCallOut ignores the interval; a calendar grid has no meaning on a clock",

	// A zero interval, refused by the kernel with KindValue. Reachable from the
	// public API: dtype.FromDuration does no validation, so `Truncate(time.Duration(0))`
	// carries no error out of the builder and DtExpr.Truncate's iv.Err() check passes.
	"dt.truncate(dt,zero)": "a zero interval is a constant of the expression and is refused only at execution",
	"dt.truncate(ts,zero)": "a zero interval is a constant of the expression and is refused only at execution",
	"dt.truncate(tn,zero)": "a zero interval is a constant of the expression and is refused only at execution",
	"dt.truncate(tm,zero)": "a zero interval is a constant of the expression and is refused only at execution",

	// The same, one sign over. `Truncate(-time.Hour)` builds, plans and renders.
	"dt.truncate(dt,negative)": "a negative interval is refused only at execution",
	"dt.truncate(ts,negative)": "a negative interval is refused only at execution",
	"dt.truncate(tn,negative)": "a negative interval is refused only at execution",
	"dt.truncate(tm,negative)": "a negative interval is refused only at execution",
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

	// The CALL arm, which did not exist until step 56.
	//
	// internal/expr declares 62 call functions across six namespaces and NO TEST FILE
	// in the repository referenced a single CallFn constant. The surface was covered
	// only behaviourally, by hand-written cases — and step 55 probed six of them by
	// hand, found one broken (is_in on a Null receiver) and wrote down that the arm
	// was separate work. One in six is not a rate a hand list can be trusted at.
	//
	// is_in is driven with an EMPTY probe set, and that is a limitation written down
	// rather than left quiet. A fixed probe is strict-cast to the receiver, so one
	// int64 against `bo`, `dt` or `tm` fails on the VALUE — the same trap the cast arm
	// above sidesteps — and a value refusal is not a contract violation. An empty set
	// is legal and type-independent, so it asks the type question for all 17
	// receivers; real probes stay isin_test.go's business.
	t.Run("call", func(t *testing.T) {
		fns := allCallFns()
		// Anti-vacuity on the derivation itself. A classifier whose range broke would
		// drop a whole family and the loop would still complete.
		if len(fns) < 62 {
			t.Fatalf("only %d call functions derived; the enum declares 62", len(fns))
		}
		var ran int
		for _, fn := range fns {
			for _, set := range contractArgSets(fn) {
				for _, c := range contractCols {
					args := []expr.Node{&expr.Col{Name: c}}
					for _, a := range set.args {
						args = append(args, callLit(a))
					}
					n := &expr.Call{Fn: fn, Args: args}
					if checkContract(t, n, b, fn.String()+"("+c+set.label+")") {
						ran++
					}
				}
			}
		}
		// 62 fns over 17 columns is 1054 labels, but .list and .struct are
		// unreachable against this fixture and every string call refuses a numeric
		// receiver, so most are agreed refusals. Around 142 actually run.
		if ran < 100 {
			t.Errorf("only %d call combinations resolved", ran)
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
