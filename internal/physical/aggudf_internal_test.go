package physical

// extractAggs merges by rendering, and it runs twice.
//
// One map per plan.Aggregate (agg.go) and one per plan.TemporalGroup
// (tempgroup.go). Three doc comments in this repository say "three maps" and list
// only one of those two. They are the same function, so one test covers both
// merges; what differs is only which operator reaches it.
//
// This pins the merge itself. Without it, the end-to-end collision tests degrade
// to "the planner refused something" once the refusal lands, and would keep passing
// if this dedup were deleted.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

func aggOverUDF(name string) expr.Node {
	return &expr.Agg{Op: expr.AggMax, Child: &expr.UDF{
		Child: &expr.Col{Name: "v"},
		Out:   dtype.Int64,
		Name:  name,
		Kind:  "map_elements",
	}}
}

func TestExtractAggsMergesByRendering(t *testing.T) {
	in := dtype.MustSchema(dtype.NotNull("v", dtype.Int64))

	// Two aggregates over same-named udfs, through ONE byKey — which is what a
	// single Agg() call gives them.
	var specs []aggSpec
	byKey := map[string]string{}
	outs := make([]expr.Node, 2)
	for i, name := range []string{"f", "f"} {
		var err error
		if outs[i], err = extractAggs(aggOverUDF(name), in, &specs, byKey); err != nil {
			t.Fatal(err)
		}
	}
	if len(specs) != 1 {
		t.Errorf("two same-named udfs produced %d aggregate specs, want 1 — the "+
			"merge this defect depends on is gone", len(specs))
	}
	if outs[0].String() != outs[1].String() {
		t.Errorf("the two rewrites differ (%s, %s); they should both be the same "+
			"temporary", outs[0], outs[1])
	}

	// The control.
	specs, byKey = nil, map[string]string{}
	for i, name := range []string{"f", "g"} {
		var err error
		if outs[i], err = extractAggs(aggOverUDF(name), in, &specs, byKey); err != nil {
			t.Fatal(err)
		}
	}
	if len(specs) != 2 {
		t.Errorf("two differently-named udfs produced %d specs, want 2 — the name "+
			"is not reaching the rendering", len(specs))
	}
}
