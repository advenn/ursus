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
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
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

// TestCheckUDFNamesRejectsAnUnidentifiedUDF pins the detectable failure mode.
//
// A composite literal outside internal/expr still compiles — Go forbids NAMING an
// unexported field, not omitting it — so a UDF built without expr.NewUDF carries
// id 0. Two of those would look identical to each other, and the check would merge
// exactly what it exists to keep apart. So it refuses instead, loudly, as an ursus
// bug rather than a user error.
func TestCheckUDFNamesRejectsAnUnidentifiedUDF(t *testing.T) {
	p := &Project{Input: &Scan{}, Exprs: []expr.Node{udfNode("f")}}

	err := CheckUDFNames(p)
	if err == nil {
		t.Fatal("a udf with no identity must be refused")
	}
	if !errors.Is(err, uerr.ErrInternal) {
		t.Errorf("want an internal error, got %v", err)
	}
	if !strings.Contains(err.Error(), "expr.NewUDF") {
		t.Errorf("the error should name the constructor: %v", err)
	}
}

// TestCheckUDFNamesAcceptsOneUDFTwice: the same node in two slots is one udf, and
// the check must not refuse it. It is reachable by holding an Expr in a variable
// and using it twice, which is also the workaround the refusal recommends.
func TestCheckUDFNamesAcceptsOneUDFTwice(t *testing.T) {
	u := expr.NewUDF(&expr.Col{Name: "v"}, dtype.Int64, "f", "map_elements", nil)
	p := &Project{Input: &Scan{}, Exprs: []expr.Node{
		&expr.Alias{Child: u, Name: "a"},
		&expr.Alias{Child: u, Name: "b"},
	}}
	if err := CheckUDFNames(p); err != nil {
		t.Errorf("one udf used twice must be legal: %v", err)
	}
}

// TestCheckUDFNamesRefusesTwoIdentities is the check in one line, without a query.
func TestCheckUDFNamesRefusesTwoIdentities(t *testing.T) {
	mk := func() expr.Node {
		return expr.NewUDF(&expr.Col{Name: "v"}, dtype.Int64, "f", "map_elements", nil)
	}
	p := &Project{Input: &Scan{}, Exprs: []expr.Node{
		&expr.Alias{Child: mk(), Name: "a"},
		&expr.Alias{Child: mk(), Name: "b"},
	}}
	err := CheckUDFNames(p)
	if err == nil {
		t.Fatal("two different udfs named \"f\" must be refused")
	}
	if !strings.Contains(err.Error(), `two different udfs are both named "f"`) {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}
