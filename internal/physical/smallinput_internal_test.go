package physical

// Step 396: a small query runs small (B1 of v0.8-scope.md). Every pipeline got a
// dispatcher and a worker a thread, and a group-by a sink a thread, whatever its rows:
// a 1,000-row group-by took 104 µs on eight threads and 42 on one.

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// unknownRows is a source that does not know its rows, as a CSV file does not.
type unknownRows struct{ s *plan.Scan }

func (u unknownRows) Name() string     { return "unknown" }
func (u unknownRows) Describe() string { return "" }
func (u unknownRows) Caps() plan.Caps  { return plan.Caps{} }
func (u unknownRows) Schema(ctx context.Context) (*dtype.Schema, error) {
	return u.s.Src.Schema(ctx)
}

// TestLargestInputIsTheLargestSource: the most rows any one source gives, through a
// join, capped by a scan's row hint; and not known where one source does not know.
func TestLargestInputIsTheLargestSource(t *testing.T) {
	rng := rand.New(rand.NewPCG(396, 1))
	small, big := sideScan(t, rng, "k", "a", 100, 10, 1<<30), sideScan(t, rng, "k", "b", 3000, 10, 1<<30)
	join := &plan.Join{Left: big, Right: small}
	if rows, ok := plan.LargestInput(join); !ok || rows != 3000 {
		t.Errorf("a join of 3,000 and 100 rows: %d, %v", rows, ok)
	}
	if rows, ok := plan.LargestInput(big.WithMaxRows(10)); !ok || rows != 10 {
		t.Errorf("a scan of 3,000 rows hinted at 10: %d, %v", rows, ok)
	}
	unknown := &plan.Scan{Src: unknownRows{small}, Full: small.Full}
	if _, ok := plan.LargestInput(&plan.Join{Left: unknown, Right: small}); ok {
		t.Error("a join reading a source of no count has a largest input")
	}
}

// TestASmallInputRunsSmall: where the caller gave no WithThreads (Fewer), a pipeline
// whose sources give at most fewPipelineRows runs on the goroutine that pulls it, and
// a group-by over at most fewAggRows builds one sink; past them, and at any size on
// the caller's own threads, as many as Threads.
func TestASmallInputRunsSmall(t *testing.T) {
	rng := rand.New(rand.NewPCG(396, 2))
	opts := func(fewer bool) Options {
		return Options{BatchSize: 512, Threads: 8, Fewer: fewer, Budget: execopt.NewBudget(0, "")}
	}
	pipeline := func(rows int) plan.Node {
		s := sideScan(t, rng, "k", "name", rows, 100, 1<<30)
		return &plan.Project{Input: s, Exprs: []expr.Node{&expr.Col{Name: "k"}}}
	}
	parallel := func(n plan.Node, o Options) bool {
		t.Helper()
		op, err := PlanRoot(t.Context(), n, o)
		if err != nil {
			t.Fatal(err)
		}
		defer op.Close()
		_, par := op.(*parallelOp)
		return par
	}
	for _, c := range []struct {
		rows  int
		fewer bool
		want  bool
	}{
		{fewPipelineRows, true, false},
		{fewPipelineRows + 512, true, true},
		{512, false, true},
	} {
		if got := parallel(pipeline(c.rows), opts(c.fewer)); got != c.want {
			t.Errorf("a pipeline of %d rows, the threads %s: parallel %v, want %v",
				c.rows, map[bool]string{true: "the default", false: "the caller's"}[c.fewer], got, c.want)
		}
	}

	sum := []aggSpec{{op: expr.AggSum}}
	for _, c := range []struct {
		rows  int
		fewer bool
		want  int
	}{
		{fewAggRows, true, 1},
		{fewAggRows + 512, true, 8},
		{512, false, 8},
	} {
		a := &plan.Aggregate{Input: sideScan(t, rng, "k", "name", c.rows, 100, 1<<30)}
		if got := aggWorkers(a, sum, opts(c.fewer)); got != c.want {
			t.Errorf("a group-by of %d rows, the threads %s: %d sinks, want %d",
				c.rows, map[bool]string{true: "the default", false: "the caller's"}[c.fewer], got, c.want)
		}
	}
}
