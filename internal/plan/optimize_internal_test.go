package plan

// This file is IN-PACKAGE, and deliberately.
//
// Optimizer.rules is unexported and has no Register method, so the fixed-point
// error path cannot be reached from package plan_test at all. The alternative was
// to export a registration API that nothing in production would ever call — a
// public seam whose only consumer is a test is worse than a test file that knows
// one private field, because the seam then has to be supported forever.

import (
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// stubNode is a Node with no children and a fixed schema. Enough to drive the
// rule loop, which is all these tests exercise.
type stubNode struct{}

func (stubNode) planNode()                {}
func (stubNode) Children() []Node         { return nil }
func (stubNode) WithChildren([]Node) Node { return stubNode{} }
func (stubNode) Label() string            { return "STUB" }
func (stubNode) Schema() (*dtype.Schema, error) {
	return dtype.NewSchema(dtype.NotNull("id", dtype.Int64))
}

// flipFlop never converges: it reports a change on every pass forever.
type flipFlop struct{ seen *int }

func (flipFlop) Name() string { return "flip_flop" }

func (f flipFlop) Apply(n Node, _ Flags) (Node, bool, error) {
	*f.seen++
	return n, true, nil
}

// TestOscillatingRuleIsCaught exercises the maxIterations bound.
//
// It has existed since step 2 — "a rule that oscillates between two forms would
// otherwise hang the query; failing loudly is better than hanging" — and until
// this step every registered rule ran Once, so the branch had never executed. A
// bound that has never been reached is a bound nobody has checked the sign of.
func TestOscillatingRuleIsCaught(t *testing.T) {
	seen := 0
	o := &Optimizer{
		folder: &constFolder{},
		rules: []registered{{
			rule:    flipFlop{seen: &seen},
			mode:    UntilStable,
			enabled: func(Flags) bool { return true },
		}},
	}

	_, err := o.Run(stubNode{}, DefaultFlags())
	if err == nil {
		t.Fatal("an oscillating rule reached no fixed point, but Run returned no error")
	}
	if !strings.Contains(err.Error(), "did not reach a fixed point") {
		t.Errorf("error = %v, want it to name the fixed point", err)
	}
	if !strings.Contains(err.Error(), "flip_flop") {
		t.Errorf("error = %v, want it to name the offending rule", err)
	}
	// The bound is a bound: it must stop, and it must stop where it says it does.
	if seen != maxIterations {
		t.Errorf("rule applied %d times, want exactly maxIterations (%d)",
			seen, maxIterations)
	}
}

// TestUntilStableStopsWhenStable is the other half, and the one that would catch
// an off-by-one making every UntilStable rule fail: a rule that converges must
// finish quietly, not trip the bound.
func TestUntilStableStopsWhenStable(t *testing.T) {
	seen := 0
	o := &Optimizer{
		folder: &constFolder{},
		rules: []registered{{
			// Changes once, then reports stable.
			rule:    settles{seen: &seen},
			mode:    UntilStable,
			enabled: func(Flags) bool { return true },
		}},
	}
	if _, err := o.Run(stubNode{}, DefaultFlags()); err != nil {
		t.Fatalf("a rule that reaches a fixed point should not error: %v", err)
	}
	if seen != 2 {
		t.Errorf("rule applied %d times, want 2 (one change, one confirming pass)", seen)
	}
}

type settles struct{ seen *int }

func (settles) Name() string { return "settles" }

func (s settles) Apply(n Node, _ Flags) (Node, bool, error) {
	*s.seen++
	return n, *s.seen < 2, nil
}

// --- Verify ---------------------------------------------------------------------

// fixedNode is a leaf with a given schema.
type fixedNode struct{ s *dtype.Schema }

func (fixedNode) planNode()                        {}
func (fixedNode) Children() []Node                 { return nil }
func (f fixedNode) WithChildren([]Node) Node       { return f }
func (fixedNode) Label() string                    { return "FIXED" }
func (f fixedNode) Schema() (*dtype.Schema, error) { return f.s, nil }

func schemaOf(t *testing.T, fields ...dtype.Field) *dtype.Schema {
	t.Helper()
	s, err := dtype.NewSchema(fields...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// becomes is a rule that replaces the whole plan with to, once.
type becomes struct{ to Node }

func (becomes) Name() string { return "becomes" }

func (b becomes) Apply(Node, Flags) (Node, bool, error) { return b.to, true, nil }

func verifying(to Node) *Optimizer {
	return &Optimizer{
		folder: &constFolder{},
		Verify: true,
		rules: []registered{{
			rule:    becomes{to: to},
			mode:    Once,
			enabled: func(Flags) bool { return true },
		}},
	}
}

// TestVerifyCatchesWhatTheRootSchemaCannotShow: each case is a plan a broken rule
// could produce, with the same root schema as the plan it replaced. Until step 70
// Verify compared only the root schema, and none of these was an error before
// Collect — O1's refusal form and O2's diagonal form reached the physical plan.
func TestVerifyCatchesWhatTheRootSchemaCannotShow(t *testing.T) {
	id := dtype.NotNull("id", dtype.Int64)
	x := dtype.Of("x", dtype.Int64)
	one := fixedNode{schemaOf(t, id)}
	two := fixedNode{schemaOf(t, id, x)}

	for _, c := range []struct {
		name  string
		from  Node
		to    Node
		wants string // "" means Verify must accept it
	}{
		{"an unchanged plan", one, one, ""},
		{"a filter on its own input's column", one,
			&Filter{Input: one, Preds: []expr.Node{&expr.Binary{Op: expr.OpGt,
				L: &expr.Col{Name: "id"}, R: &expr.Lit{Value: int64(0), DT: dtype.Int64}}}}, ""},
		{"a changed root schema", one, two, "changed the output schema"},
		{"a predicate on a column its input does not have", one,
			&Filter{Input: one, Preds: []expr.Node{&expr.Col{Name: "revenue"}}},
			"does not resolve against its input"},
		{"a predicate that is not Boolean", one,
			&Filter{Input: one, Preds: []expr.Node{&expr.Col{Name: "id"}}},
			"not Boolean"},
		{"a diagonal union with a narrowed child",
			&Union{Inputs: []Node{two, two}, Mode: ConcatDiagonal},
			&Union{Inputs: []Node{one, two}, Mode: ConcatDiagonal},
			"union child 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := verifying(c.to).Run(c.from, DefaultFlags())
			switch {
			case c.wants == "" && err != nil:
				t.Errorf("Verify refused a sound plan: %v", err)
			case c.wants != "" && err == nil:
				t.Errorf("Verify accepted it; want %q", c.wants)
			case c.wants != "" && !strings.Contains(err.Error(), c.wants):
				t.Errorf("error = %v, want %q", err, c.wants)
			}
		})
	}
}
