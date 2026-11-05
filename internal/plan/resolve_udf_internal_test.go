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
// the tests that keep the others honest. Since step 72 the merges key on
// expr.Identity, so these pin both halves: what must merge, and what must not.

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

// TestExtractWindowsMergesOnIdentity pins internal/plan/resolve_window.go's byKey.
//
// It merged on the rendering until step 72, and two windows whose udfs shared a
// name collapsed to ONE spec — the losing subtree not merely shared but DROPPED:
// extractWindows returns a Col reference and never appends it. Two literals that
// render alike did the same. It merges on expr.Identity now, and what it must still
// merge is one expression used twice.
func TestExtractWindowsMergesOnIdentity(t *testing.T) {
	src := &Scan{}
	one := expr.NewUDF(&expr.Col{Name: "v"}, dtype.Int64, "f", "map_elements", nil)
	plus := func(l *expr.Lit) expr.Node { return &expr.Binary{Op: expr.OpAdd, L: &expr.Col{Name: "v"}, R: l} }

	for _, tc := range []struct {
		name string
		a, b expr.Node
		want int
	}{
		{"one udf, used twice", one, one, 1},
		{"two udfs sharing a name",
			expr.NewUDF(&expr.Col{Name: "v"}, dtype.Int64, "f", "map_elements", nil),
			expr.NewUDF(&expr.Col{Name: "v"}, dtype.Int64, "f", "map_elements", nil), 2},
		{"two udfs, two names", udfNode("f"), udfNode("g"), 2},
		{"int8 and int64", plus(&expr.Lit{Value: int8(100), DT: dtype.Int8}),
			plus(&expr.Lit{Value: int64(100), DT: dtype.Int64}), 2},
		{"one literal, written twice", plus(&expr.Lit{Value: int8(100), DT: dtype.Int8}),
			plus(&expr.Lit{Value: int8(100), DT: dtype.Int8}), 1},
	} {
		exprs := []expr.Node{
			&expr.Alias{Child: winOver(tc.a), Name: "a"},
			&expr.Alias{Child: winOver(tc.b), Name: "b"},
		}
		_, _, temps, err := extractWindows(src, exprs, "select")
		if err != nil {
			t.Fatal(err)
		}
		if len(temps) != tc.want {
			t.Errorf("%s: %d window temporaries, want %d", tc.name, len(temps), tc.want)
		}
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
