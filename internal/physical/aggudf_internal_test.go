package physical

// extractAggs merges on expr.Identity, and it runs twice.
//
// One map per plan.Aggregate (agg.go) and one per plan.TemporalGroup
// (tempgroup.go). They are the same function, so one test covers both merges; what
// differs is only which operator reaches it. It merged on the rendering until step
// 72, which merged two udfs sharing a name and two literals that render alike.
//
// This pins the merge itself, both halves: what must merge, and what must not.
// Without it, the end-to-end collision tests degrade to "the planner refused
// something", and would keep passing if this dedup were deleted.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

func TestExtractAggsMergesOnIdentity(t *testing.T) {
	in := dtype.MustSchema(dtype.NotNull("v", dtype.Int64))
	udf := func(name string) expr.Node {
		return expr.NewUDF(&expr.Col{Name: "v"}, dtype.Int64, name, "map_elements", nil)
	}
	plus := func(l *expr.Lit) expr.Node { return &expr.Binary{Op: expr.OpAdd, L: &expr.Col{Name: "v"}, R: l} }
	one := udf("f")

	for _, tc := range []struct {
		name string
		a, b expr.Node
		want int
	}{
		{"one udf, used twice", one, one, 1},
		{"two udfs sharing a name", udf("f"), udf("f"), 2},
		{"two udfs, two names", udf("f"), udf("g"), 2},
		{"int8 and int64", plus(&expr.Lit{Value: int8(1), DT: dtype.Int8}),
			plus(&expr.Lit{Value: int64(1), DT: dtype.Int64}), 2},
		{"one literal, written twice", plus(&expr.Lit{Value: int8(1), DT: dtype.Int8}),
			plus(&expr.Lit{Value: int8(1), DT: dtype.Int8}), 1},
	} {
		// Through ONE byKey — which is what a single Agg() call gives them.
		var specs []aggSpec
		byKey := map[string]string{}
		outs := make([]expr.Node, 2)
		for i, child := range []expr.Node{tc.a, tc.b} {
			var err error
			if outs[i], err = extractAggs(&expr.Agg{Op: expr.AggMax, Child: child}, in, &specs, byKey); err != nil {
				t.Fatal(err)
			}
		}
		if len(specs) != tc.want {
			t.Errorf("%s: %d aggregate specs, want %d", tc.name, len(specs), tc.want)
		}
		if merged := outs[0].String() == outs[1].String(); merged != (tc.want == 1) {
			t.Errorf("%s: the rewrites are %s and %s", tc.name, outs[0], outs[1])
		}
	}
}
