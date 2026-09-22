package expr_test

// The identity a rendering cannot carry.
//
// Two udfs sharing a name render identically — that is the defect the id exists to
// let the planner see. These tests pin the three properties the check depends on,
// and the first one is the one that fails for every scheme derived from the closure.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

type fakeImpl struct{}

func (fakeImpl) UDFKernel() {}

func newUDF(name string, impl expr.UDFImpl) *expr.UDF {
	return expr.NewUDF(&expr.Col{Name: "v"}, dtype.Int64, name, "map_elements", impl)
}

// TestEveryUDFGetsADistinctIdentity is the tooth against deriving the id from the
// implementation.
//
// The same impl value is passed twice ON PURPOSE. Four design documents record that
// identity "has to go through reflect.ValueOf(impl).Pointer()", and every scheme of
// that shape answers "same" here — as it does for every MapElements udf in a real
// program, which all close over one func literal. A check built on it detects
// nothing.
func TestEveryUDFGetsADistinctIdentity(t *testing.T) {
	impl := fakeImpl{}
	a, b := newUDF("f", impl), newUDF("f", impl)

	if a.ID() == b.ID() {
		t.Error("two udfs built from one impl share an identity; anything derived " +
			"from the closure does this, and it is why the id is minted instead")
	}
	if a.ID() == 0 || b.ID() == 0 {
		t.Error("NewUDF minted a zero id, which means 'not from NewUDF'")
	}
	// The rendering genuinely cannot tell them apart — the premise of the defect.
	if a.String() != b.String() {
		t.Fatalf("the fixture is wrong: these render differently\n %s\n %s", a, b)
	}
}

// TestIdentitySurvivesRebuild: Rebuild and extractAggs copy this node with `c := *t`
// on every rewrite, so node-pointer identity would be useless and a lost field
// would make two copies of ONE udf look like two different ones — which would
// refuse a legal query, including multi-column expansion.
func TestIdentitySurvivesRebuild(t *testing.T) {
	u := newUDF("f", fakeImpl{})
	got := expr.Rebuild(u, []expr.Node{&expr.Col{Name: "w"}})

	c, ok := got.(*expr.UDF)
	if !ok {
		t.Fatalf("Rebuild returned %T", got)
	}
	if c == u {
		t.Fatal("the fixture is wrong: Rebuild returned the same pointer, so this " +
			"proves nothing about copying")
	}
	if c.ID() != u.ID() {
		t.Errorf("the copy's id is %d and the original's is %d; a rewrite must not "+
			"change which udf a node is", c.ID(), u.ID())
	}
}

// TestBareLiteralHasNoIdentity pins the detectable failure. A composite literal
// outside this package still compiles — Go forbids naming an unexported field, not
// omitting it — so the check has to be able to tell that it happened.
func TestBareLiteralHasNoIdentity(t *testing.T) {
	u := &expr.UDF{Child: &expr.Col{Name: "v"}, Out: dtype.Int64,
		Name: "f", Kind: "map_elements"}
	if u.ID() != 0 {
		t.Errorf("a bare literal carries id %d; 0 is what marks it as not minted",
			u.ID())
	}
}
