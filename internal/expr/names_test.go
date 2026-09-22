package expr

// This file is `package expr`, not `expr_test`, because the sentinel constants it
// checks — unaryOpCount, aggOpCount and the rest — are unexported and are exactly
// what makes the check possible. The precedent is internal/physical/sink_test.go
// and internal/kernel/differential_test.go, both internal for the same reason.

import (
	"fmt"
	"strings"
	"testing"
)

// TestEveryEnumConstantHasADistinctName is the structural defence against the
// single most expensive class of bug in this codebase.
//
// # Why an empty name is a wrong answer, not a cosmetic one
//
// FOUR maps key on a rendered String(), over three code sites, because
// extractAggs is instantiated once per aggregate and once per temporal group:
//
//	internal/plan/resolve_window.go   window temporaries
//	internal/physical/agg.go          aggregate temporaries
//	internal/physical/tempgroup.go    the same, for GroupByDynamic and Rolling
//	internal/physical/window.go       partition-key sharing
//
// The fourth went unlisted here, in udf.go and in expr/udf.go at once, until step
// 65 counted them — which is the same way an enum name goes quiet.
//
// So two expressions that RENDER THE SAME become one computation, and both names
// get the first one's answer. Step 7 shipped exactly that — Agg.String() hid its
// parameters, so two different quantiles collapsed and both returned the median —
// and step 8 shipped it again for partition keys.
//
// Every one of these name tables is `[somethingCount]string`, sized by the enum's
// own sentinel. That means `int(o) < len(names)` is true for every DECLARED
// constant, so the "?" fallback in each String() was unreachable and a forgotten
// entry rendered as the EMPTY STRING — not "?", not a panic, just a silently
// colliding name. dtype.TypeID.String() has always had the `!= ""` half of the
// guard; these did not until step 11 added it.
//
// This test is what makes appending a constant safe. Step 11 alone appends to
// three of these tables.
// # CallFn is the eighth, and it did not fit the table as written
//
// Seven enums here are one contiguous block sized by one sentinel, and render "?"
// when they run off the end. CallFn is neither. It has SIX family sentinels with
// hundred-wide gaps between them, and its fallback is "call(N)".
//
// The honest reading is that CallFn's fallback is the BETTER of the two. "?" makes
// every unnamed constant render identically, which is precisely the collision this
// file exists to catch — the guard against it is that the name tables are arrays
// sized by a sentinel, so "?" is unreachable for a declared constant. CallFn's
// names live in a MAP, where a missing entry is perfectly reachable, and "call(247)"
// and "call(248)" are distinct. So the test generalises and the enum does not move:
// a range list instead of a count, and a per-enum predicate for "this is the
// fallback" instead of a hard-coded "?".
type enumTable struct {
	enum    string
	ranges  [][2]int // half-open [lo, hi)
	str     func(int) string
	unknown func(string) bool // reports whether a name is the fallback rendering
}

func isQuestionMark(s string) bool { return s == "?" }

func upTo(n int) [][2]int { return [][2]int{{0, n}} }

func enumTables() []enumTable {
	return []enumTable{
		{"UnaryOp", upTo(int(unaryOpCount)), func(i int) string { return UnaryOp(i).String() }, isQuestionMark},
		{"BinaryOp", upTo(int(binaryOpCount)), func(i int) string { return BinaryOp(i).String() }, isQuestionMark},
		{"AggOp", upTo(int(aggOpCount)), func(i int) string { return AggOp(i).String() }, isQuestionMark},
		{"WinFnOp", upTo(int(winFnCount)), func(i int) string { return WinFnOp(i).String() }, isQuestionMark},
		{"RankMethod", upTo(int(rankMethodCount)), func(i int) string { return RankMethod(i).String() }, isQuestionMark},
		{"MappingStrategy", upTo(int(mappingCount)), func(i int) string { return MappingStrategy(i).String() }, isQuestionMark},
		{"Interpolation", upTo(int(interpCount)), func(i int) string { return Interpolation(i).String() }, isQuestionMark},
		{
			enum: "CallFn",
			ranges: [][2]int{
				{int(FnStrContains), int(fnStrEnd)},
				{int(FnDtYear), int(fnDtEnd)},
				{int(FnIsIn), int(fnGenEnd)},
				{int(FnMathRound), int(fnMathEnd)},
				{int(FnListLen), int(fnListEnd)},
				{int(FnStructField), int(fnStructEnd)},
			},
			str:     func(i int) string { return CallFn(i).String() },
			unknown: func(s string) bool { return strings.HasPrefix(s, "call(") },
		},
	}
}

func TestEveryEnumConstantHasADistinctName(t *testing.T) {
	for _, tab := range enumTables() {
		t.Run(tab.enum, func(t *testing.T) {
			var count int
			seen := make(map[string]int)
			for _, r := range tab.ranges {
				for i := r[0]; i < r[1]; i++ {
					count++
					name := tab.str(i)
					switch {
					case name == "":
						t.Errorf("%s(%d) renders as the empty string — add it to the name "+
							"table; two unnamed constants render alike and collide in the "+
							"three maps that dedup on String()", tab.enum, i)
					case tab.unknown(name):
						t.Errorf("%s(%d) renders as %q, the unknown-constant fallback — "+
							"it is declared but has no name", tab.enum, i, name)
					}
					if prev, dup := seen[name]; dup {
						t.Errorf("%s(%d) and %s(%d) both render as %q — one of them will "+
							"silently take the other's computation", tab.enum, prev, tab.enum, i, name)
					}
					seen[name] = i
				}
			}
			if count == 0 {
				t.Fatalf("%s has no constants, so this test proves nothing", tab.enum)
			}
		})
	}
}

// TestTheEnumSweepCoversEveryEnumItClaimsTo is the anti-vacuity guard on the table
// above, and CallFn is why it exists: a range list can be short in a way a count
// cannot. Dropping one of the six family ranges would leave that family unswept and
// nothing else would say so.
func TestTheEnumSweepCoversEveryEnumItClaimsTo(t *testing.T) {
	want := map[string]int{
		"UnaryOp": 18, "BinaryOp": 18, "AggOp": 19, "WinFnOp": 5,
		"RankMethod": 5, "MappingStrategy": 3, "Interpolation": 4,
		"CallFn": 62,
	}
	for _, tab := range enumTables() {
		var count int
		for _, r := range tab.ranges {
			count += r[1] - r[0]
		}
		w, ok := want[tab.enum]
		if !ok {
			t.Errorf("%s is swept but not counted here; add it", tab.enum)
			continue
		}
		if count < w {
			t.Errorf("%s sweeps %d constants, want at least %d — a range is missing "+
				"or a sentinel moved", tab.enum, count, w)
		}
		delete(want, tab.enum)
	}
	for enum := range want {
		t.Errorf("%s is counted here but no longer swept", enum)
	}
}

// TestUnknownConstantsRenderAsUnknown pins the other half: a value BEYOND the
// sentinel must reach the fallback rather than panic on an out-of-range index.
//
// CallFn is included through the same predicate the sweep uses, so the two halves
// cannot disagree about what "the fallback" means. Its value is taken from the gap
// between two families, which is where an undeclared CallFn actually lives —
// fnStrEnd+1 is not past the end of anything, it is between the string block and the
// temporal one.
func TestUnknownConstantsRenderAsUnknown(t *testing.T) {
	cases := []struct {
		enum    string
		got     string
		unknown func(string) bool
	}{
		{"UnaryOp", UnaryOp(unaryOpCount).String(), isQuestionMark},
		{"BinaryOp", BinaryOp(binaryOpCount).String(), isQuestionMark},
		{"AggOp", AggOp(aggOpCount).String(), isQuestionMark},
		{"WinFnOp", WinFnOp(winFnCount).String(), isQuestionMark},
		{"RankMethod", RankMethod(rankMethodCount).String(), isQuestionMark},
		{"MappingStrategy", MappingStrategy(mappingCount).String(), isQuestionMark},
		{"Interpolation", Interpolation(interpCount).String(), isQuestionMark},
		{"CallFn", CallFn(fnStrEnd + 1).String(),
			func(s string) bool { return strings.HasPrefix(s, "call(") }},
	}
	for _, c := range cases {
		if !c.unknown(c.got) {
			t.Errorf("%s past its sentinel renders as %q, which is not the "+
				"unknown-constant fallback", c.enum, c.got)
		}
	}
}

// TestCallFnFamiliesDoNotOverlap guards the six range comparisons the classifiers
// are built on, as TestMathOpsAreClassified guards IsMath.
//
// Every dispatcher that touches a call — ResolveCall, evalCall, compileCall — is a
// chain of `case fn.IsString():` arms, so a constant that answered yes to two of them
// would take whichever arm came first and a constant that answered no to all of them
// would fall to an Internalf. Both are silent until the call is used.
func TestCallFnFamiliesDoNotOverlap(t *testing.T) {
	var classified int
	for i := range 600 {
		fn := CallFn(i)
		var claims []string
		for _, c := range []struct {
			name string
			ok   bool
		}{
			{"string", fn.IsString()}, {"temporal", fn.IsTemporal()},
			{"general", fn.IsGeneral()}, {"maths", fn.IsMath()},
			{"list", fn.IsList()}, {"struct", fn.IsStruct()},
		} {
			if c.ok {
				claims = append(claims, c.name)
			}
		}
		if len(claims) > 1 {
			t.Errorf("CallFn(%d) is claimed by %v; the dispatchers are chains of "+
				"classifier arms and would take whichever comes first", i, claims)
		}
		if len(claims) == 1 {
			classified++
			if name := fn.String(); strings.HasPrefix(name, "call(") {
				t.Errorf("CallFn(%d) is inside a family range but has no name (%s) — "+
					"a sentinel moved or the block is no longer contiguous", i, name)
			}
		}
	}
	if classified != 62 {
		t.Errorf("%d call functions are classified, want 62 — the families and the "+
			"name table disagree about what exists", classified)
	}
}

// TestMathOpsAreClassified guards the range comparison IsMath is built on. The
// maths block is append-only and contiguous; a constant inserted into the middle
// of it would silently move an op into or out of the widening family, which
// changes its RESULT TYPE with no error anywhere.
func TestMathOpsAreClassified(t *testing.T) {
	widening := map[UnaryOp]bool{
		OpSqrt: true, OpCbrt: true, OpExp: true,
		OpLn: true, OpLog10: true, OpLog1p: true,
	}
	preserving := []UnaryOp{OpNeg, OpAbs, OpSign, OpFloor, OpCeil}

	for op := range UnaryOp(unaryOpCount) {
		if got, want := op.IsMath(), widening[op]; got != want {
			t.Errorf("%s.IsMath() = %v, want %v", op, got, want)
		}
	}
	for _, op := range preserving {
		if op.IsMath() {
			t.Errorf("%s is type-preserving but IsMath() says it widens", op)
		}
	}
	// And the classifiers must not overlap: an op cannot be both maths and a
	// predicate, or ResolveUnary's arms would be reachable in two orders.
	for op := range UnaryOp(unaryOpCount) {
		if op.IsMath() && (op.IsNullPredicate() || op.IsNanPredicate()) {
			t.Errorf("%s is classified as both maths and a predicate", op)
		}
	}
}

// TestPowIsNotClassifiedAsSomethingElse: OpPow was APPENDED after OpXor rather
// than placed beside the other arithmetic, because both dispatchers route
// unclassified ops to arithmetic through a default arm. That only holds while
// every positive classifier says no.
func TestPowIsNotClassifiedAsSomethingElse(t *testing.T) {
	if OpPow.IsComparison() || OpPow.IsLogical() ||
		OpPow.IsMissingComparison() || OpPow.IsOrdering() {
		t.Fatalf("OpPow is claimed by a classifier, so it will not reach the "+
			"arithmetic default arm: comparison=%v logical=%v missing=%v ordering=%v",
			OpPow.IsComparison(), OpPow.IsLogical(),
			OpPow.IsMissingComparison(), OpPow.IsOrdering())
	}
	if got := fmt.Sprint(OpPow); got != "**" {
		t.Errorf("OpPow renders as %q", got)
	}
}
