package expr

import (
	"testing"

	"github.com/advenn/ursus/dtype"
)

// TestEveryAggregateOverADecimalHasItsStatedType walks the whole AggOp space and
// requires every op, over a Decimal, to resolve to the type step 69 committed to —
// or to be refused for a reason stated here. A new op cannot arrive without saying
// what it does to a Decimal.
//
// The rule, measured against Polars, DuckDB and PyArrow:
//
//	select or count   the input type, or the count/index type as for any input
//	sum               Decimal(38, s): exact, the precision all three return
//	anything else     Float64, read through the scaled float reader
//
// The type is not the whole of it — a Float64 that is 100x too large is still a
// Float64 — and TestFloatAggregatesOverADecimalReadTheScale in kernel checks the
// values. This checks that nothing slips into a type nobody chose.
func TestEveryAggregateOverADecimalHasItsStatedType(t *testing.T) {
	in := dtype.Decimal(10, 2)
	sameAsForInt64 := "same as for Int64" // a count or an index ignores the input type
	want := map[string]string{
		"sum":        "Decimal(38, 2)",
		"mean":       "Float64",
		"min":        "Decimal(10, 2)",
		"max":        "Decimal(10, 2)",
		"first":      "Decimal(10, 2)",
		"last":       "Decimal(10, 2)",
		"count":      sameAsForInt64,
		"len":        sameAsForInt64,
		"n_unique":   sameAsForInt64,
		"null_count": sameAsForInt64,
		"arg_min":    sameAsForInt64,
		"arg_max":    sameAsForInt64,
		"any":        "refused: Boolean only",
		"all_true":   "refused: Boolean only",
		"var":        "Float64",
		"std":        "Float64",
		"product":    "Float64",
		"median":     "Float64",
		"quantile":   "Float64",
		"implode":    "List(Decimal(10, 2))",
	}

	for i := range int(aggOpCount) {
		op := AggOp(i)
		w, ok := want[op.String()]
		if !ok {
			t.Errorf("%s: no stated type over a Decimal — add it to want", op)
			continue
		}
		delete(want, op.String())

		bind, err := ResolveAggBinding(op, in)
		switch {
		case len(w) > 8 && w[:8] == "refused:":
			if err == nil {
				t.Errorf("%s over %s resolved to %s, want %s", op, in, bind.Out, w)
			}
		case err != nil:
			t.Errorf("%s over %s refused: %v", op, in, err)
		case w == sameAsForInt64:
			ref, err := ResolveAggBinding(op, dtype.Int64)
			if err != nil || ref.Out != bind.Out {
				t.Errorf("%s over %s is %s, over Int64 %s (%v)", op, in, bind.Out, ref.Out, err)
			}
		case bind.Out.String() != w:
			t.Errorf("%s over %s = %s, want %s", op, in, bind.Out, w)
		}
	}
	for name := range want {
		t.Errorf("want names %q, which is not an AggOp — delete it", name)
	}
}
