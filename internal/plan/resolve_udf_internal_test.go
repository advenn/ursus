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

// TestPartitionKeysShareOnIdentity pins the precondition for
// internal/physical/window.go's partition map, which shares a PARTITIONING between
// windows whose keys share an identity. The map itself needs a planned operator to
// reach; literal_identity_test.go's "window partition" case reaches it end to end.
func TestPartitionKeysShareOnIdentity(t *testing.T) {
	one := expr.NewUDF(&expr.Col{Name: "t"}, dtype.Int64, "bucket", "map_elements", nil)
	times := func(l *expr.Lit) expr.Node { return &expr.Binary{Op: expr.OpMul, L: &expr.Col{Name: "k"}, R: l} }

	for _, tc := range []struct {
		name string
		a, b expr.Node
		same bool
	}{
		{"one udf", one, one, true},
		{"two udfs sharing a name",
			expr.NewUDF(&expr.Col{Name: "t"}, dtype.Int64, "bucket", "map_elements", nil),
			expr.NewUDF(&expr.Col{Name: "t"}, dtype.Int64, "bucket", "map_elements", nil), false},
		{"int8 and int64", times(&expr.Lit{Value: int8(2), DT: dtype.Int8}),
			times(&expr.Lit{Value: int64(2), DT: dtype.Int64}), false},
	} {
		keyA, keyB := []expr.Node{tc.a}, []expr.Node{tc.b}
		if expr.StringAll(keyA) != expr.StringAll(keyB) {
			t.Fatalf("%s: the fixture is wrong, the keys render differently", tc.name)
		}
		if got := expr.IdentityAll(keyA) == expr.IdentityAll(keyB); got != tc.same {
			t.Errorf("%s: shared = %v, want %v", tc.name, got, tc.same)
		}
	}
	// And the windows they belong to must be able to differ, or resolve_window
	// merges them first and the partition map is never reached.
	key := []expr.Node{one}
	sum := &expr.Window{Child: &expr.Agg{Op: expr.AggSum, Child: &expr.Col{Name: "v"}},
		PartitionBy: key}
	max := &expr.Window{Child: &expr.Agg{Op: expr.AggMax, Child: &expr.Col{Name: "v"}},
		PartitionBy: key}
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
