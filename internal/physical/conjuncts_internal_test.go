package physical

import (
	"testing"

	"github.com/advenn/ursus/internal/expr"
)

// TestAFiltersAndsAreItsConjuncts: a filter applies every top-level And's sides in
// turn, left first, so the right side sees only the left's survivors (step 164). An
// And under anything else is one predicate.
func TestAFiltersAndsAreItsConjuncts(t *testing.T) {
	col := func(n string) expr.Node { return &expr.Col{Name: n} }
	and := func(l, r expr.Node) expr.Node { return &expr.Binary{Op: expr.OpAnd, L: l, R: r} }
	or := &expr.Binary{Op: expr.OpOr, L: col("e"), R: and(col("f"), col("g"))}
	not := &expr.Unary{Op: expr.OpNot, Child: and(col("h"), col("i"))}
	got := conjuncts([]expr.Node{and(and(col("a"), col("b")), col("c")), col("d"), or, not})
	want := []expr.Node{col("a"), col("b"), col("c"), col("d"), or, not}
	if len(got) != len(want) {
		t.Fatalf("%d conjuncts: %s", len(got), expr.StringAll(got))
	}
	for i := range want {
		if expr.StringAll(got[i:i+1]) != expr.StringAll(want[i:i+1]) {
			t.Fatalf("conjunct %d is %s, want %s", i, expr.StringAll(got[i:i+1]), expr.StringAll(want[i:i+1]))
		}
	}
}
