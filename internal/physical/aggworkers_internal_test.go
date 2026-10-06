package physical

import (
	"testing"

	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// TestAggregationStaysParallelUnderABudget: a memory budget does not make a
// group-by serial (step 91). Since step 90 every query has a budget by default, and
// "serial under any limit" made every group-by serial — h2o's small ones about half
// again as slow. A parallel group-by now switches to serial when the budget bites,
// so the gate only declines what cannot merge at all.
func TestAggregationStaysParallelUnderABudget(t *testing.T) {
	sum := []aggSpec{{op: expr.AggSum}}
	opts := func(limit int64) Options {
		return Options{Threads: 8, Budget: execopt.NewBudget(limit, "")}
	}
	if got := aggWorkers(&plan.Aggregate{}, sum, opts(4<<30)); got != 8 {
		t.Errorf("a sum under a 4 GiB budget runs on %d workers, want 8", got)
	}
	if got := aggWorkers(&plan.Aggregate{}, sum, opts(0)); got != 8 {
		t.Errorf("a sum with no budget runs on %d workers, want 8", got)
	}
	// What still cannot merge stays serial, budget or not.
	if got := aggWorkers(&plan.Aggregate{MaintainOrder: true}, sum, opts(0)); got != 1 {
		t.Errorf("MaintainOrder runs on %d workers, want 1", got)
	}
	if got := aggWorkers(&plan.Aggregate{}, []aggSpec{{op: expr.AggFirst}}, opts(0)); got != 1 {
		t.Errorf("an order-dependent aggregate runs on %d workers, want 1", got)
	}
}
