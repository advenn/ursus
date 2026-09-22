package plan

// The merges that make a udf name load-bearing, asserted where they happen.
//
// The end-to-end tests in udfcollide_test.go are the evidence that two udfs sharing
// a name answer wrongly. They cannot, on their own, prove WHICH map merged them —
// and once the collision is refused at plan time, every one of them degrades to
// "the planner refused something" and would keep passing if a dedup map were
// deleted outright.
//
// So each merge is pinned here, by calling the merging function directly. These are
// the tests that keep the others honest.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// udfNode builds a UDF over col("v") with the given name. The Impl is nil, which is
// fine: nothing here evaluates it, and the point is that the RENDERING cannot tell
// two of these apart.
func udfNode(name string) expr.Node {
	return &expr.UDF{
		Child: &expr.Col{Name: "v"},
		Out:   dtype.Int64,
		Name:  name,
		Kind:  "map_elements",
	}
}

func winOver(child expr.Node) expr.Node {
	return &expr.Window{Child: &expr.Agg{Op: expr.AggMax, Child: child},
		PartitionBy: []expr.Node{&expr.Col{Name: "g"}}}
}

// TestExtractWindowsMergesByRendering pins internal/plan/resolve_window.go:76.
//
// Two windows whose udfs share a name render alike and collapse to ONE spec — and
// the losing subtree is not merely shared, it is DROPPED: extractWindows returns a
// Col reference and never appends it. That is why a name check has to run before
// Resolve's walk; after it, the second udf is not in the tree to be found.
func TestExtractWindowsMergesByRendering(t *testing.T) {
	src := &Scan{}

	same := []expr.Node{
		&expr.Alias{Child: winOver(udfNode("f")), Name: "a"},
		&expr.Alias{Child: winOver(udfNode("f")), Name: "b"},
	}
	_, _, temps, err := extractWindows(src, same, "select")
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 1 {
		t.Errorf("two same-named udfs produced %d window temporaries, want 1 — "+
			"the merge this defect depends on is gone", len(temps))
	}

	// The control: different names render differently and must NOT merge.
	diff := []expr.Node{
		&expr.Alias{Child: winOver(udfNode("f")), Name: "a"},
		&expr.Alias{Child: winOver(udfNode("g")), Name: "b"},
	}
	if _, _, temps, err = extractWindows(src, diff, "select"); err != nil {
		t.Fatal(err)
	}
	if len(temps) != 2 {
		t.Errorf("two differently-named udfs produced %d window temporaries, "+
			"want 2 — the name is not reaching the rendering", len(temps))
	}
}

// TestPartitionKeysRenderAlike pins the precondition for
// internal/physical/window.go:377, which shares a PARTITIONING between windows
// whose keys render the same.
//
// The physical map itself needs a planned operator to reach; what makes it fire is
// this, and this is what would break first if the rendering changed.
func TestPartitionKeysRenderAlike(t *testing.T) {
	keyA := []expr.Node{udfNode("bucket")}
	keyB := []expr.Node{udfNode("bucket")}
	if expr.StringAll(keyA) != expr.StringAll(keyB) {
		t.Fatalf("two same-named udf partition keys render differently:\n %s\n %s",
			expr.StringAll(keyA), expr.StringAll(keyB))
	}
	// And the windows they belong to must be able to differ, or resolve_window
	// merges them first and the partition map is never reached.
	sum := &expr.Window{Child: &expr.Agg{Op: expr.AggSum, Child: &expr.Col{Name: "v"}},
		PartitionBy: keyA}
	max := &expr.Window{Child: &expr.Agg{Op: expr.AggMax, Child: &expr.Col{Name: "v"}},
		PartitionBy: keyB}
	if sum.String() == max.String() {
		t.Fatal("the two windows render alike, so the window temporary map would " +
			"merge them before the partition map could")
	}
}
