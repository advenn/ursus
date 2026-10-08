package physical

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// TestFinishedGroupingsDropTheirState: once a group-by, a rolling or a dynamic
// group-by has its answer, its aggregates' state is dead, and the account already
// stopped counting it. It stayed referenced by the sink until the query ended: a
// median holds every value of every group, so the heap held what the budget
// believed was free, and a sort downstream could grow into it (step 120).
func TestFinishedGroupingsDropTheirState(t *testing.T) {
	ctx := t.Context()
	sch := dtype.MustSchema(dtype.Of("g", dtype.Int64),
		dtype.Of("t", dtype.Datetime(dtype.Micro, "")), dtype.Of("v", dtype.Float64))
	hour := int64(time.Hour / time.Microsecond)
	b, err := data.NewBatch(sch, []*data.Column{
		data.NewFixed("g", dtype.Int64, []int64{1, 1, 2}, bitmap.View{}),
		data.NewFixed("t", dtype.Datetime(dtype.Micro, ""), []int64{0, hour, 2 * hour}, bitmap.View{}),
		data.NewFixed("v", dtype.Float64, []float64{1, 2, 3}, bitmap.View{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(sch, b)
	if err != nil {
		t.Fatal(err)
	}
	scan := &plan.Scan{Src: src, Full: sch}
	median := []expr.Node{&expr.Alias{Name: "m", Child: &expr.Agg{Op: expr.AggMedian, Child: &expr.Col{Name: "v"}}}}
	opts := Options{Threads: 1, Budget: execopt.NewBudget(0, "")}

	// sinkOf drains op and returns the sink under its rename stage.
	sinkOf := func(t *testing.T, op Operator) Sink {
		t.Helper()
		defer op.Close()
		for {
			if _, err := op.Next(ctx); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal(err)
			}
		}
		st, ok := op.(*stage)
		if !ok {
			t.Fatalf("planned a %T, not a stage", op)
		}
		br, ok := st.child.(*breaker)
		if !ok {
			t.Fatalf("the stage holds a %T, not a breaker", st.child)
		}
		return br.sink
	}

	t.Run("a group-by", func(t *testing.T) {
		op, err := planAggregate(ctx, &plan.Aggregate{Input: scan,
			Keys: []expr.Node{&expr.Col{Name: "g"}}, Aggs: median}, opts)
		if err != nil {
			t.Fatal(err)
		}
		if s := sinkOf(t, op).(*hashAggSink); s.accs != nil || s.ids.Len() != 0 {
			t.Errorf("the finished sink still holds %d accumulators and %d keys", len(s.accs), s.ids.Len())
		}
	})
	t.Run("a rolling group-by", func(t *testing.T) {
		op, err := planTemporalGroup(ctx, &plan.TemporalGroup{Input: scan,
			Index: &expr.Col{Name: "t"}, Aggs: median, Rolling: true, Period: dtype.Every("2h")}, opts)
		if err != nil {
			t.Fatal(err)
		}
		if s := sinkOf(t, op).(*temporalSink); s.accs != nil {
			t.Errorf("the finished sink still holds %d accumulators", len(s.accs))
		}
	})
}
