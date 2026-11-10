package expr

import (
	"testing"

	"github.com/advenn/ursus/dtype"
)

// TestEveryAggregateOverADecimalIsAccountedFor walks the whole AggOp space and
// requires every op, over a Decimal, to be one of three things: refused for a reason
// stated here, typed as it is over an Int64 (a count or an index ignores the input
// type), or recorded — its type and its values are Polars 2.1.0's, in the decimalagg
// group of testdata/oracle, spelled in oracle_decimalagg_test.go since step 503, which
// took this test's table of types (step 69's rule: sum a Decimal(38, s), anything
// else a Float64 read through the scaled float reader, or the input's own type). A
// new op cannot arrive without saying what it does to a Decimal.
//
// Polars 2.1 refuses a bitwise aggregate of a Decimal, as this does (decimalagg/bitwise_and),
// and answers any and all of one, true where a value is not zero, which this refuses
// as step 69 chose (decimalagg/any_all, a finding of step 503, left to the maintainer).
func TestEveryAggregateOverADecimalIsAccountedFor(t *testing.T) {
	in := dtype.Decimal(10, 2)
	sameAsForInt64 := "same as for Int64" // a count or an index ignores the input type
	recorded := "recorded"                // the decimalagg group holds its type and values
	want := map[string]string{
		"sum":        recorded,
		"mean":       recorded,
		"min":        recorded,
		"max":        recorded,
		"first":      recorded,
		"last":       recorded,
		"count":      sameAsForInt64,
		"len":        sameAsForInt64,
		"n_unique":   sameAsForInt64,
		"null_count": sameAsForInt64,
		"arg_min":    sameAsForInt64,
		"arg_max":    sameAsForInt64,
		"any":        "refused: Boolean only",
		"all_true":   "refused: Boolean only",
		"var":        recorded,
		"std":        recorded,
		"product":    recorded,
		"median":     recorded,
		"quantile":   recorded,
		"implode":    recorded,
		"top_k":      recorded,
		"bottom_k":   recorded,
		// Two inputs, as PairOf's struct, so a Decimal alone is not one; a pair of
		// Decimals is Float64 for corr and cov, and min_by keeps its value's type.
		"corr":   "refused: a pair of inputs",
		"cov":    "refused: a pair of inputs",
		"min_by": "refused: a pair of inputs",
		"max_by": "refused: a pair of inputs",
		// Step 276.
		"skew":        recorded,
		"kurtosis":    recorded,
		"bitwise_and": "refused: an integer or a Boolean",
		"bitwise_or":  "refused: an integer or a Boolean",
		"bitwise_xor": "refused: an integer or a Boolean",
		"nan_min":     recorded,
		// Step 277.
		"entropy":       recorded,
		"corr_spearman": "refused: a pair of inputs",
		// Step 324: the bounds are the storage's, as Polars gives them.
		"item":        recorded,
		"lower_bound": recorded,
		"upper_bound": recorded,
		"is_sorted":   recorded,
		// Step 330: a pair, as min_by's.
		"top_k_by":    "refused: a pair of inputs",
		"bottom_k_by": "refused: a pair of inputs",
		// Step 340: the values themselves, a list.
		"mode": recorded,
	}

	for i := range int(aggOpCount) {
		op := AggOp(i)
		w, ok := want[op.String()]
		if !ok {
			t.Errorf("%s: not accounted for over a Decimal — add it to want, and to the decimalagg group", op)
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
		}
	}
	for name := range want {
		t.Errorf("want names %q, which is not an AggOp — delete it", name)
	}
}
