package physical

import (
	"context"
	"testing"

	"github.com/advenn/ursus/internal/execopt"
)

// TestHashAggSinkMergeReleasesTheMergedAccount: after Merge, what the merged-in
// sink held is charged once, to the survivor. Before step 91 its account kept it
// to the end of the query, so the budget counted the folded state twice — which
// mattered once a parallel group-by could go on, serially, under a budget.
func TestHashAggSinkMergeReleasesTheMergedAccount(t *testing.T) {
	ctx := context.Background()
	s := testSchema(t)
	budget := execopt.NewBudget(0, "")
	sink := func() *hashAggSink {
		h := newTestAggSink(t)
		h.mem, h.budget = budget.Account("group_by"), budget
		return h
	}
	a, b := sink(), sink()
	if err := a.Consume(ctx, testBatch(t, s, []int64{1, 2, 3}, []int64{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	if err := b.Consume(ctx, testBatch(t, s, []int64{4, 5, 6}, []int64{4, 5, 6})); err != nil {
		t.Fatal(err)
	}
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	if used := b.mem.Used(); used != 0 {
		t.Errorf("the merged-in sink still holds %d bytes", used)
	}
	if total, own := budget.Used(), a.mem.Used(); total != own {
		t.Errorf("the budget holds %d bytes, the survivor %d: the folded state is counted twice", total, own)
	}
}
