package expr

// This file is `package expr`, not `expr_test`, because the sentinel constants it
// checks — unaryOpCount, aggOpCount and the rest — are unexported and are exactly
// what makes the check possible. The precedent is internal/physical/sink_test.go
// and internal/kernel/differential_test.go, both internal for the same reason.

import (
	"fmt"
	"testing"
)

// TestEveryEnumConstantHasADistinctName is the structural defence against the
// single most expensive class of bug in this codebase.
//
// # Why an empty name is a wrong answer, not a cosmetic one
//
// Three maps key on a rendered String():
//
//	internal/plan/resolve_window.go   window temporaries
//	internal/physical/agg.go          aggregate temporaries
//	internal/physical/window.go       partition-key sharing
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
func TestEveryEnumConstantHasADistinctName(t *testing.T) {
	tables := []struct {
		enum  string
		count int
		str   func(int) string
	}{
		{"UnaryOp", int(unaryOpCount), func(i int) string { return UnaryOp(i).String() }},
		{"BinaryOp", int(binaryOpCount), func(i int) string { return BinaryOp(i).String() }},
		{"AggOp", int(aggOpCount), func(i int) string { return AggOp(i).String() }},
		{"WinFnOp", int(winFnCount), func(i int) string { return WinFnOp(i).String() }},
		{"RankMethod", int(rankMethodCount), func(i int) string { return RankMethod(i).String() }},
		{"MappingStrategy", int(mappingCount), func(i int) string { return MappingStrategy(i).String() }},
		{"Interpolation", int(interpCount), func(i int) string { return Interpolation(i).String() }},
	}

	for _, tab := range tables {
		t.Run(tab.enum, func(t *testing.T) {
			if tab.count == 0 {
				t.Fatalf("%s has no constants, so this test proves nothing", tab.enum)
			}
			seen := make(map[string]int, tab.count)
			for i := range tab.count {
				name := tab.str(i)
				switch {
				case name == "":
					t.Errorf("%s(%d) renders as the empty string — add it to the name "+
						"table; two unnamed constants render alike and collide in the "+
						"three maps that dedup on String()", tab.enum, i)
				case name == "?":
					t.Errorf("%s(%d) renders as %q, the unknown-constant fallback",
						tab.enum, i, name)
				}
				if prev, dup := seen[name]; dup {
					t.Errorf("%s(%d) and %s(%d) both render as %q — one of them will "+
						"silently take the other's computation", tab.enum, prev, tab.enum, i, name)
				}
				seen[name] = i
			}
		})
	}
}

// TestUnknownConstantsRenderAsUnknown pins the other half: a value BEYOND the
// sentinel must reach the fallback rather than panic on an out-of-range index.
func TestUnknownConstantsRenderAsUnknown(t *testing.T) {
	cases := []struct {
		enum string
		got  string
	}{
		{"UnaryOp", UnaryOp(unaryOpCount).String()},
		{"BinaryOp", BinaryOp(binaryOpCount).String()},
		{"AggOp", AggOp(aggOpCount).String()},
		{"WinFnOp", WinFnOp(winFnCount).String()},
		{"RankMethod", RankMethod(rankMethodCount).String()},
		{"MappingStrategy", MappingStrategy(mappingCount).String()},
		{"Interpolation", Interpolation(interpCount).String()},
	}
	for _, c := range cases {
		if c.got != "?" {
			t.Errorf("%s past its sentinel renders as %q, want %q", c.enum, c.got, "?")
		}
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
