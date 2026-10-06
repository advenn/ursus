package physical

import (
	"context"
	"errors"
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

// TestAParallelWorkerSwitchesAtHalfTheBudget: a parallel worker asks for serial
// once the query holds half its budget, not all of it, leaving room for the fold of
// the workers' tables that follows. Switching at the budget itself doubled it there,
// and h2o gb10 was OOM-killed under the cap its serial run spills within.
func TestAParallelWorkerSwitchesAtHalfTheBudget(t *testing.T) {
	ctx := context.Background()
	s := testSchema(t)
	consumeUnder := func(limit int64) error {
		budget := execopt.NewBudget(limit, "")
		h := newTestAggSink(t)
		h.mem, h.budget, h.parallel = budget.Account("group_by"), budget, true
		return h.Consume(ctx, testBatch(t, s, []int64{1, 2, 3}, []int64{1, 2, 3}))
	}
	// How much one batch charges, measured with no limit.
	probe := execopt.NewBudget(0, "")
	h := newTestAggSink(t)
	h.mem, h.budget = probe.Account("group_by"), probe
	if err := h.Consume(ctx, testBatch(t, s, []int64{1, 2, 3}, []int64{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	used := probe.Used()

	if err := consumeUnder(2*used + 2); err != nil {
		t.Errorf("under half the budget: %v, want nil", err)
	}
	if err := consumeUnder(2*used - 2); !errors.Is(err, errWantSerial) {
		t.Errorf("just past half the budget, and under all of it: %v, want errWantSerial", err)
	}
}

// TestAFoldedWorkerHoldsNoTable: after the parallel driver folds a worker into the
// survivor, the worker's table is gone, so the fold holds about one table at a time.
func TestAFoldedWorkerHoldsNoTable(t *testing.T) {
	ctx := context.Background()
	s := testSchema(t)
	a, b := newTestAggSink(t), newTestAggSink(t)
	child := newBatchOperator(s,
		testBatch(t, s, []int64{1, 2, 3}, []int64{1, 2, 3}),
		testBatch(t, s, []int64{4, 5, 6}, []int64{4, 5, 6}))
	p := newParallelSink(child, []Sink{a, b})
	if _, err := p.Next(ctx); err != nil {
		t.Fatal(err)
	}
	if n := b.ids.Len(); n != 0 {
		t.Errorf("the folded worker still holds %d keys", n)
	}
	if n := a.ids.Len(); n != 6 {
		t.Errorf("the survivor holds %d keys, want 6", n)
	}
}
