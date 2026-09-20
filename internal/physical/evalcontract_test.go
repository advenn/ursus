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
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// contractBatch is one column per dtype worth exercising, all length 3, all with a
// materialised validity bitmap so no kernel takes a no-nulls shortcut by accident.
//
// # The columns are built first and the schema is derived from them
//
// data.NewList and data.NewStruct DERIVE their dtype from the child column and the
// field columns, and both say why: a declared type that disagreed with the columns
// actually holding the values would be a lie no caller could detect. Writing the
// fields out separately would reintroduce exactly that, so the fields come from
// c.DType() and there is no parallel list to keep in step.
func contractBatch(t *testing.T) *data.Batch {
	t.Helper()
	cols := contractColumns(t)

	fields := make([]dtype.Field, len(cols))
	for i, c := range cols {
		fields[i] = dtype.Of(c.Name(), c.DType())
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

const contractRows = 3

func contractValid(n int) bitmap.View {
	b := bitmap.NewBuilder(n)
	for range n {
		b.Append(true)
	}
	return b.Finish()
}

// contractList builds a three-row List column holding [w, x], [y, z] and [].
//
// The child is FOUR elements long against three rows, deliberately: a kernel that
// confuses an element count with a row count cannot be right here by coincidence.
// The empty row is the other half — an empty list and a null list leave the offset
// unmoved either way, and only the validity bit tells them apart.
func contractList(name string, child *data.Column) *data.Column {
	return data.NewList(name, []int32{0, 2, 4, 4}, child, contractValid(contractRows))
}

// contractColumns is the fixture's type axis.
//
// # Why this is not buildOperand
//
// temporaloverflow_test.go's builder hides an EXTREME value in a null slot on
// purpose. This fixture has no null slots, so that value would be visible — and a
// visible MinInt64 in a Date column makes the cast arm refuse on the VALUE, which is
// correct behaviour rather than a contract violation, and this arm asks only about
// types. The two builders answer different questions, so the duplication is
// deliberate.
//
// What is NOT left to chance is completeness. TestContractColumnsCoverTheEnum walks
// TypeIDCount against this list, so a type it forgets fails the suite instead of
// going quiet — which is the property the op lists below have always had and this
// axis did not.
func contractColumns(t *testing.T) []*data.Column {
	t.Helper()
	v := contractValid(contractRows)
	wide := []i128.Int128{i128.FromInt64(1), i128.FromInt64(2), i128.FromInt64(3)}

	return []*data.Column{
		data.NewFixed("i8", dtype.Int8, []int8{1, 2, 3}, v),
		data.NewFixed("i16", dtype.Int16, []int16{1, 2, 3}, v),
		data.NewFixed("i32", dtype.Int32, []int32{1, 2, 3}, v),
		data.NewFixed("i64", dtype.Int64, []int64{1, 2, 3}, v),
		data.NewFixed("u8", dtype.Uint8, []uint8{1, 2, 3}, v),
		// Uint16 was absent for no reason beyond the list having been written by
		// hand: it is an ordinary operand type every one of its neighbours covers.
		data.NewFixed("u16", dtype.Uint16, []uint16{1, 2, 3}, v),
		data.NewFixed("u32", dtype.Uint32, []uint32{1, 2, 3}, v),
		data.NewFixed("u64", dtype.Uint64, []uint64{1, 2, 3}, v),
		data.NewFixed("f32", dtype.Float32, []float32{1, 2, 3}, v),
		data.NewFixed("f64", dtype.Float64, []float64{1, 2, 3}, v),
		// Int128 is Sum's accumulator and output type. Decimal is the absence this
		// file's own ratchet comment blames for seventeen cast disagreements it
		// could not see — "because this fixture has no Decimal column".
		data.NewFixed("i128", dtype.Int128, wide, v),
		data.NewFixed("dec", dtype.Decimal(18, 3), wide, v),
		data.NewBool("bo", v, v),
		// Numeric text, so a strict cast to a numeric type fails on the TYPE
		// rules or not at all. "a" would fail on the VALUE, which is correct
		// behaviour and not a contract violation — the contract is about types.
		data.NewString("st", []string{"1", "2", "3"}, v),
		// Binary shares String's storage and is a different type, which is the whole
		// reason to carry both: a kernel switching on the physical layout rather
		// than on the type would agree here and only here.
		data.NewString("bin", []string{"1", "2", "3"}, v).WithDType(dtype.Binary),
		data.NewFixed("dt", dtype.Date, []int32{1, 2, 3}, v),
		data.NewFixed("ts", dtype.Datetime(dtype.Micro, "UTC"), []int64{1, 2, 3}, v),
		data.NewFixed("du", dtype.Duration(dtype.Nano), []int64{1, 2, 3}, v),
		// A NAIVE datetime and a Time, neither of which the matrix had. Their
		// absence is why it could not see that arithmetic and comparison disagreed
		// about zones: with one Datetime column there is no pair to disagree about.
		data.NewFixed("tn", dtype.Datetime(dtype.Micro, ""), []int64{1, 2, 3}, v),
		data.NewFixed("tm", dtype.Time(dtype.Nano), []int64{1, 2, 3}, v),
		// THREE List columns, because the ELEMENT type decides what the .list
		// namespace answers. list.mean is the case: internal/expr/call.go answers
		// Float64 for every element type without reading Inner(), while
		// internal/kernel/listfn.go derives ResolveAggBinding(AggMean, elem). Those
		// agree for Int64 and disagree for both of the others — Duration since step
		// 62 made its mean exact, Float32 for as long as the function has existed.
		// A fixture carrying only List(Int64) would add three hundred combinations
		// and let the one known defect walk straight through.
		contractList("li",
			data.NewFixed("item", dtype.Int64, []int64{1, 2, 3, 4}, contractValid(4))),
		contractList("lidur",
			data.NewFixed("item", dtype.Duration(dtype.Nano), []int64{1, 2, 3, 4}, contractValid(4))),
		contractList("lif32",
			data.NewFixed("item", dtype.Float32, []float32{1, 2, 3, 4}, contractValid(4))),

		// A Struct with TWO fields of DIFFERENT types. One field would not be
		// enough: struct.field's whole claim is that the output type comes from the
		// ARGUMENT rather than from the receiver, and against a single-field struct
		// "the argument's type" and "the receiver's only type" are the same answer.
		// The field named "f" is mandatory — that is the name contractCallArgs hands
		// struct.field, and without it the one struct combination stays an agreed
		// refusal and the column proves nothing.
		data.NewStruct("sr", []*data.Column{
			data.NewFixed("f", dtype.Int64, []int64{1, 2, 3}, v),
			data.NewString("g", []string{"a", "b", "c"}, v),
		}, v),

		// A NULL column, which the matrix had no way to reach until step 52.
		// ResolveUnary admits Null for eleven ops and kernel.Unary implements none
		// of them; the row-count assertion below was already written and could not
		// fire, because the unit of coverage is the fixture and this fixture had
		// sixteen types and not this one.
		//
		// It is also the only column here with no payload buffer at all —
		// data.NewNull carries validity and nothing else — so it is the only one
		// that tests what a kernel does when there is nothing to read.
		data.NewNull("nu", dtype.Null, contractRows),
	}
}

// noContractColumn names every TypeID this fixture cannot hold, with the reason.
//
// It is deliberately NOT noOperand from temporaloverflow_test.go, though it is the
// same shape. That map excuses List, Struct and Enum because arithmetic on them goes
// through the .list and .struct namespaces — sound for an arithmetic sweep, and the
// wrong reason here, where those namespaces are precisely what is being checked.
// Two maps, two questions, two sets of reasons.
var noContractColumn = map[dtype.TypeID]string{
	dtype.TypeArray:       "declared but not constructible: there is no data.Column for it",
	dtype.TypeCategorical: "reserved; its mapping grows at runtime",
	dtype.TypeUint128:     "reserved; Int128 covers every unsigned value",

	// Enum is CONSTRUCTIBLE — data.NewFixed under dtype.Enum, physically Uint32,
	// which internal/data/nullcheck_test.go builds today — and it is excused anyway
	// because adding it does not produce a gap, it produces a PANIC, in the cast
	// arm, before any gap can be recorded:
	//
	//	panic: runtime error: index out of range [0] with length 0
	//
	// dtype.IsString() is true for an Enum, so CanCast admits Enum -> numeric;
	// kernel.castTo then routes it to parseFromString, which opens with c.Strings()
	// on a column that has no offsets buffer because its payload is uint32 indices.
	// ResolveCall names the identical hazard and calls it "latent today only because
	// Enum columns cannot yet be built from the public API".
	//
	// This entry is a DEBT, not an exemption: it is the one type whose absence is
	// now measured rather than assumed, and the walk below fails the day someone
	// deletes the line without building the column.
	dtype.TypeEnum: "constructible, but CanCast admits Enum -> numeric through " +
		"IsString and parseFromString then calls Strings() on a uint32 payload, " +
		"which panics before the matrix can judge anything; its own step",
}

// TestContractColumnsCoverTheEnum is the check the type axis never had.
//
// The op lists below are derived from their enums precisely because a hand-written
// list goes quiet, and three declared ops had in fact been missing from them. The
// COLUMN list was hand-written for twelve steps, and List and Struct were missing
// from it the whole time — so every .list function and struct.field was an agreed
// refusal against a fixture that had no receiver to offer them.
func TestContractColumnsCoverTheEnum(t *testing.T) {
	seen := map[dtype.TypeID]bool{}
	for _, c := range contractColumns(t) {
		seen[c.DType().ID()] = true
	}
	for id := dtype.TypeID(0); id < dtype.TypeIDCount; id++ {
		why, excused := noContractColumn[id]
		switch {
		case seen[id] && excused:
			t.Errorf("%s is both built and excused (%q)", id, why)
		case !seen[id] && !excused:
			t.Errorf("%s is neither built by contractColumns nor named in "+
				"noContractColumn — the matrix cannot see it", id)
		}
	}
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
	// THREE sets, following FnDtTruncate above: the answer must come from the
	// argument, so a second field of a different type is what proves it, and a name
	// that is not there must refuse at plan time rather than inside the kernel.
	expr.FnStructField: {
		{label: ",f", args: []any{"f"}},
		{label: ",g", args: []any{"g"}},
		{label: ",absent", args: []any{"nope"}},
	},
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

// contractCastTargets was the fixture's SECOND hand-written list, twelve entries
// with no Decimal, no Binary, no Int128, no Time and nothing nested. It is derived
// from the column axis now, plus the two temporal variants worth naming that no
// column carries — a cast target needs no column, only a type.
//
// Cast x nested was invisible to both of this repository's cast instruments at once:
// dtype.CanCast promises List -> List while the kernel refuses anything non-numeric,
// and TestCanCastAgreesWithTheKernel names List and Struct as unsamplable SOURCES.
// Deriving this axis is what closes that.
func contractCastTargetsOf(cols []*data.Column) []dtype.DataType {
	seen := map[string]bool{}
	var out []dtype.DataType
	add := func(d dtype.DataType) {
		if k := d.String(); !seen[k] {
			seen[k] = true
			out = append(out, d)
		}
	}
	for _, c := range cols {
		add(c.DType())
	}
	// A different unit and a different zone from the columns', so a cast that
	// relabels instead of converting has somewhere to show it.
	add(dtype.Datetime(dtype.Nano, "UTC"))
	add(dtype.Duration(dtype.Second))
	return out
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
// # Step 56 held nine, for one commit, and every one was dt.truncate
//
// The call arm below covers 62 functions over 17 receivers — 155 combinations run —
// and found exactly one broken function. That is a far better result than the
// operator surface gave and is worth saying rather than burying. But that one was
// broken three ways, and all three had one cause: ResolveCall takes the whole Call
// node precisely so an output type can depend on an ARGUMENT, and dtCallOut threw
// the arguments away. `git show` the commit below this one for the list.
// # Step 64 derived the TYPE axis, and it held a hundred and six
//
// The op lists above are derived because a hand-written list of an enum goes quiet.
// The COLUMN list was hand-written anyway, for twelve steps, and the binary arm's own
// anti-vacuity comment said so out loud — "the column list is written down, so it can
// only shrink by hand". List and Struct were missing the whole time, so fourteen
// .list functions and struct.field were agreed refusals against a fixture with no
// receiver to offer them, and Uint16, Decimal, Binary and Int128 were missing with no
// reason at all.
//
// Step 58 predicted "~32 disagreements" and that number was carried forward five
// times without once being measured. Deriving both type axes produced 106, in seven
// classes, and only two of them are about the nested types the prediction was about.
// The estimate was not wrong so much as scoped to what its author could see.
var knownContractGaps = map[string]string{
	// BINARY HAS NO COMPARISON, AND NOTHING SAID SO. 32 labels, and the only class
	// here that is not about a nested type: Binary is an ordinary scalar a Parquet
	// or Arrow file produces, and `Col("blob").Eq(Col("blob"))` type-checks and then
	// fails in the kernel. resolveComparison admits it — Binary is ordered and
	// promotes with itself — while kernel.dispatch has no Binary arm and falls to
	// "comparison is not implemented for %s". All EIGHT comparison operators, not
	// just equality. The fixture could not see it because it had no Binary column.
	"!=(bin,bin)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	"!=(bin,nu)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	"!=(bin,null)":  "Binary resolves to Bool and the kernel has no Binary comparison",
	"!=(nu,bin)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	"<!>(bin,bin)":  "Binary resolves to Bool and the kernel has no Binary comparison",
	"<!>(bin,nu)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	"<!>(bin,null)": "Binary resolves to Bool and the kernel has no Binary comparison",
	"<!>(nu,bin)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	"<(bin,bin)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	"<(bin,nu)":     "Binary resolves to Bool and the kernel has no Binary comparison",
	"<(bin,null)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	"<(nu,bin)":     "Binary resolves to Bool and the kernel has no Binary comparison",
	"<=(bin,bin)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	"<=(bin,nu)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	"<=(bin,null)":  "Binary resolves to Bool and the kernel has no Binary comparison",
	"<=(nu,bin)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	"<=>(bin,bin)":  "Binary resolves to Bool and the kernel has no Binary comparison",
	"<=>(bin,nu)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	"<=>(bin,null)": "Binary resolves to Bool and the kernel has no Binary comparison",
	"<=>(nu,bin)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	"==(bin,bin)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	"==(bin,nu)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	"==(bin,null)":  "Binary resolves to Bool and the kernel has no Binary comparison",
	"==(nu,bin)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	">(bin,bin)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	">(bin,nu)":     "Binary resolves to Bool and the kernel has no Binary comparison",
	">(bin,null)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	">(nu,bin)":     "Binary resolves to Bool and the kernel has no Binary comparison",
	">=(bin,bin)":   "Binary resolves to Bool and the kernel has no Binary comparison",
	">=(bin,nu)":    "Binary resolves to Bool and the kernel has no Binary comparison",
	">=(bin,null)":  "Binary resolves to Bool and the kernel has no Binary comparison",
	">=(nu,bin)":    "Binary resolves to Bool and the kernel has no Binary comparison",

	// EQUALITY OVER A LIST. 48 labels. resolveComparison's rule is that "equality
	// is defined for anything with a common type", and Promote(List(T), List(T))
	// returns List(T) — so Field promises Bool and dispatchCompare, which switches
	// on the PHYSICAL type and gets a List back unchanged, has no arm for it.
	// Ordering is correctly refused by both halves; it is equality and the two
	// missing-value operators that resolve and then fail.
	"!=(li,li)":        "equality over a List resolves and the kernel has no nested arm",
	"!=(li,nu)":        "equality over a List resolves and the kernel has no nested arm",
	"!=(li,null)":      "equality over a List resolves and the kernel has no nested arm",
	"!=(lidur,lidur)":  "equality over a List resolves and the kernel has no nested arm",
	"!=(lidur,nu)":     "equality over a List resolves and the kernel has no nested arm",
	"!=(lidur,null)":   "equality over a List resolves and the kernel has no nested arm",
	"!=(lif32,lif32)":  "equality over a List resolves and the kernel has no nested arm",
	"!=(lif32,nu)":     "equality over a List resolves and the kernel has no nested arm",
	"!=(lif32,null)":   "equality over a List resolves and the kernel has no nested arm",
	"!=(nu,li)":        "equality over a List resolves and the kernel has no nested arm",
	"!=(nu,lidur)":     "equality over a List resolves and the kernel has no nested arm",
	"!=(nu,lif32)":     "equality over a List resolves and the kernel has no nested arm",
	"<!>(li,li)":       "equality over a List resolves and the kernel has no nested arm",
	"<!>(li,nu)":       "equality over a List resolves and the kernel has no nested arm",
	"<!>(li,null)":     "equality over a List resolves and the kernel has no nested arm",
	"<!>(lidur,lidur)": "equality over a List resolves and the kernel has no nested arm",
	"<!>(lidur,nu)":    "equality over a List resolves and the kernel has no nested arm",
	"<!>(lidur,null)":  "equality over a List resolves and the kernel has no nested arm",
	"<!>(lif32,lif32)": "equality over a List resolves and the kernel has no nested arm",
	"<!>(lif32,nu)":    "equality over a List resolves and the kernel has no nested arm",
	"<!>(lif32,null)":  "equality over a List resolves and the kernel has no nested arm",
	"<!>(nu,li)":       "equality over a List resolves and the kernel has no nested arm",
	"<!>(nu,lidur)":    "equality over a List resolves and the kernel has no nested arm",
	"<!>(nu,lif32)":    "equality over a List resolves and the kernel has no nested arm",
	"<=>(li,li)":       "equality over a List resolves and the kernel has no nested arm",
	"<=>(li,nu)":       "equality over a List resolves and the kernel has no nested arm",
	"<=>(li,null)":     "equality over a List resolves and the kernel has no nested arm",
	"<=>(lidur,lidur)": "equality over a List resolves and the kernel has no nested arm",
	"<=>(lidur,nu)":    "equality over a List resolves and the kernel has no nested arm",
	"<=>(lidur,null)":  "equality over a List resolves and the kernel has no nested arm",
	"<=>(lif32,lif32)": "equality over a List resolves and the kernel has no nested arm",
	"<=>(lif32,nu)":    "equality over a List resolves and the kernel has no nested arm",
	"<=>(lif32,null)":  "equality over a List resolves and the kernel has no nested arm",
	"<=>(nu,li)":       "equality over a List resolves and the kernel has no nested arm",
	"<=>(nu,lidur)":    "equality over a List resolves and the kernel has no nested arm",
	"<=>(nu,lif32)":    "equality over a List resolves and the kernel has no nested arm",
	"==(li,li)":        "equality over a List resolves and the kernel has no nested arm",
	"==(li,nu)":        "equality over a List resolves and the kernel has no nested arm",
	"==(li,null)":      "equality over a List resolves and the kernel has no nested arm",
	"==(lidur,lidur)":  "equality over a List resolves and the kernel has no nested arm",
	"==(lidur,nu)":     "equality over a List resolves and the kernel has no nested arm",
	"==(lidur,null)":   "equality over a List resolves and the kernel has no nested arm",
	"==(lif32,lif32)":  "equality over a List resolves and the kernel has no nested arm",
	"==(lif32,nu)":     "equality over a List resolves and the kernel has no nested arm",
	"==(lif32,null)":   "equality over a List resolves and the kernel has no nested arm",
	"==(nu,li)":        "equality over a List resolves and the kernel has no nested arm",
	"==(nu,lidur)":     "equality over a List resolves and the kernel has no nested arm",
	"==(nu,lif32)":     "equality over a List resolves and the kernel has no nested arm",

	// EQUALITY OVER A STRUCT. The same shape as the List class, four labels, listed
	// apart because a Struct is not ordered — so only the four equality-family
	// operators reach the kernel at all.
	"!=(sr,sr)":  "equality over a Struct resolves and the kernel has no nested arm",
	"<!>(sr,sr)": "equality over a Struct resolves and the kernel has no nested arm",
	"<=>(sr,sr)": "equality over a Struct resolves and the kernel has no nested arm",
	"==(sr,sr)":  "equality over a Struct resolves and the kernel has no nested arm",

	// kernel.NullColumn CANNOT BUILD A NULL STRUCT. 13 labels, and a different fix
	// site from the class above: these fail EARLIER, in the cast that lifts a Null
	// operand to the common type, before any comparison is attempted. NullColumn has
	// arms for Bool, for string storage and for List, then falls through to
	// nullFixed's "cannot build a null column of %s". A Struct in a conditional or
	// on the null side of an outer join hits the same wall.
	"!=(nu,sr)":                             "kernel.NullColumn has no Struct arm",
	"!=(sr,nu)":                             "kernel.NullColumn has no Struct arm",
	"!=(sr,null)":                           "kernel.NullColumn has no Struct arm",
	"<!>(nu,sr)":                            "kernel.NullColumn has no Struct arm",
	"<!>(sr,nu)":                            "kernel.NullColumn has no Struct arm",
	"<!>(sr,null)":                          "kernel.NullColumn has no Struct arm",
	"<=>(nu,sr)":                            "kernel.NullColumn has no Struct arm",
	"<=>(sr,nu)":                            "kernel.NullColumn has no Struct arm",
	"<=>(sr,null)":                          "kernel.NullColumn has no Struct arm",
	"==(nu,sr)":                             "kernel.NullColumn has no Struct arm",
	"==(sr,nu)":                             "kernel.NullColumn has no Struct arm",
	"==(sr,null)":                           "kernel.NullColumn has no Struct arm",
	"cast(nu->Struct(f: Int64, g: String))": "kernel.NullColumn has no Struct arm",

	// CAST BETWEEN LIST TYPES. 6 labels. dtype.CanCast promises List -> List;
	// kernel.castTo refuses anything whose physical types are not both numeric. This
	// pair was invisible to BOTH cast instruments at once: this fixture had no List
	// column and no List cast target, and TestCanCastAgreesWithTheKernel names List
	// and Struct as unsamplable SOURCES. Deriving the target axis is what found it.
	"cast(li->List(Duration(ns)))":    "CanCast promises List -> List and the kernel refuses it",
	"cast(li->List(Float32))":         "CanCast promises List -> List and the kernel refuses it",
	"cast(lidur->List(Float32))":      "CanCast promises List -> List and the kernel refuses it",
	"cast(lidur->List(Int64))":        "CanCast promises List -> List and the kernel refuses it",
	"cast(lif32->List(Duration(ns)))": "CanCast promises List -> List and the kernel refuses it",
	"cast(lif32->List(Int64))":        "CanCast promises List -> List and the kernel refuses it",

	// STRING -> INT128. One label, and nothing to do with nested types: it appeared
	// because Int128 became a cast target when the axis was derived. CanCast
	// promises it, and the kernel answers "cannot narrow to Int128".
	"cast(st->Int128)": "CanCast promises String -> Int128 and the kernel cannot narrow",
}

var seenContractGaps = map[string]bool{}

// TestEvaluatorContract walks the op × dtype matrix the doc has promised since the
// evaluator was written.
func TestEvaluatorContract(t *testing.T) {
	b := contractBatch(t)
	// The name axis comes from the fixture, so the two cannot drift: a column added
	// to contractColumns is swept the moment it is built, with no second list to
	// remember to update.
	contractCols := b.Schema().Names()

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
		if len(contractBinaryOps) < 18 || len(contractCols) < 25 {
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
		for _, to := range contractCastTargetsOf(contractColumns(t)) {
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
