package expr_test

// Identity: equal exactly when two expressions are one computation.
//
// The literal sweep over every type the public API can lift lives beside the
// Literal constraint, in the root package's literal_identity_test.go. These pin the
// structure: where a literal sits, a udf's id, and the list form.

import (
	"math"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

func TestIdentitySeesWhatTheRenderingDoesNot(t *testing.T) {
	col := &expr.Col{Name: "x"}
	add := func(l *expr.Lit) expr.Node { return &expr.Binary{Op: expr.OpAdd, L: col, R: l} }
	i8 := &expr.Lit{Value: int8(100), DT: dtype.Int8}
	i64 := &expr.Lit{Value: int64(100), DT: dtype.Int64}
	f := newUDF("f", fakeImpl{})
	g := newUDF("f", fakeImpl{})
	c := *f // how Rebuild and extractAggs copy a node

	for _, tc := range []struct {
		name string
		a, b expr.Node
		same bool
	}{
		{"int8 and int64, deep in a tree", add(i8), add(i64), false},
		{"one tree built twice", add(i8), add(&expr.Lit{Value: int8(100), DT: dtype.Int8}), true},
		{"a weak literal and a strong one", &expr.Lit{Value: int64(1), DT: dtype.Int64, Weak: true}, i64, false},
		{"-0 and +0", &expr.Lit{Value: math.Copysign(0, -1), DT: dtype.Float64},
			&expr.Lit{Value: 0.0, DT: dtype.Float64}, false},
		{"NaN and NaN", &expr.Lit{Value: math.NaN(), DT: dtype.Float64},
			&expr.Lit{Value: math.NaN(), DT: dtype.Float64}, true},
		{"a typed null and another", &expr.Lit{DT: dtype.Int8}, &expr.Lit{DT: dtype.Int64}, false},
		{"two udfs sharing a name", f, g, false},
		{"one udf and its copy", f, &c, true},
		{"which side the literal is on", &expr.Binary{Op: expr.OpAdd, L: i8, R: i64},
			&expr.Binary{Op: expr.OpAdd, L: i64, R: i8}, false},
	} {
		got := expr.Identity(tc.a) == expr.Identity(tc.b)
		if got != tc.same {
			t.Errorf("%s: same identity = %v, want %v\n %s\n %s", tc.name, got, tc.same,
				expr.Identity(tc.a), expr.Identity(tc.b))
		}
	}
	// The premise: the first case, the "which side" case and both udf cases render
	// alike, so without Identity nothing here could tell them apart.
	if add(i8).String() != add(i64).String() || f.String() != g.String() {
		t.Fatal("the fixture is wrong: these no longer render alike")
	}
}

// TestIdentityAllIsTheList: a window's partition keys are one unit, and a list is
// equal only to the same list.
func TestIdentityAllIsTheList(t *testing.T) {
	a, b := &expr.Col{Name: "a"}, &expr.Col{Name: "b"}
	i8 := &expr.Lit{Value: int8(2), DT: dtype.Int8}
	i64 := &expr.Lit{Value: int64(2), DT: dtype.Int64}
	for _, tc := range []struct {
		name string
		x, y []expr.Node
		same bool
	}{
		{"the same keys", []expr.Node{a, b}, []expr.Node{a, b}, true},
		{"reordered", []expr.Node{a, b}, []expr.Node{b, a}, false},
		{"one fewer", []expr.Node{a, b}, []expr.Node{a}, false},
		{"none", nil, []expr.Node{}, true},
		{"a key's literal type", []expr.Node{&expr.Binary{Op: expr.OpMul, L: a, R: i8}},
			[]expr.Node{&expr.Binary{Op: expr.OpMul, L: a, R: i64}}, false},
	} {
		if got := expr.IdentityAll(tc.x) == expr.IdentityAll(tc.y); got != tc.same {
			t.Errorf("%s: same identity = %v, want %v", tc.name, got, tc.same)
		}
	}
}
