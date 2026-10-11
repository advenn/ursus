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

// TestAnUnkeyedRollingGroupHoldsABatchsWorth: an unkeyed rolling group hands its
// windows out as they close (step 207), so what it keeps between batches is its
// open segments and the rows a window yet to come may reach. Those rows point into
// the batches they came in; when they are few, they are gathered into one at every
// batch, not once eight batches are held. Forty batches of 256 rows a minute apart,
// a 30-minute window: after each batch it holds two batches at most, and at most
// two segments.
func TestAnUnkeyedRollingGroupHoldsABatchsWorth(t *testing.T) {
	ctx := t.Context()
	sch := dtype.MustSchema(dtype.Of("t", dtype.Datetime(dtype.Micro, "")), dtype.Of("v", dtype.Float64))
	minute := int64(time.Minute / time.Microsecond)
	const rows, batches = 256, 40
	var bs []*data.Batch
	for b := range batches {
		ts, vs := make([]int64, rows), make([]float64, rows)
		for i := range rows {
			ts[i], vs[i] = int64(b*rows+i)*minute, float64(i)
		}
		batch, err := data.NewBatch(sch, []*data.Column{
			data.NewFixed("t", dtype.Datetime(dtype.Micro, ""), ts, bitmap.View{}),
			data.NewFixed("v", dtype.Float64, vs, bitmap.View{}),
		})
		if err != nil {
			t.Fatal(err)
		}
		bs = append(bs, batch)
	}
	src, err := memsrc.New(sch, bs...)
	if err != nil {
		t.Fatal(err)
	}
	sum := []expr.Node{&expr.Alias{Name: "s", Child: &expr.Agg{Op: expr.AggSum, Child: &expr.Col{Name: "v"}}}}
	op, err := planTemporalGroup(ctx, &plan.TemporalGroup{Input: &plan.Scan{Src: src, Full: sch},
		Index: &expr.Col{Name: "t"}, Aggs: sum, Rolling: true, Period: dtype.Every("30m")},
		Options{Threads: 1, BatchSize: rows, Budget: execopt.NewBudget(0, "")})
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	st, ok := op.(*stage)
	if !ok {
		t.Fatalf("planned a %T", op)
	}
	ts, ok := st.child.(*tempStreamOp)
	if !ok {
		t.Fatalf("an unkeyed rolling group planned a %T, not a tempStreamOp", st.child)
	}
	got := 0
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got += b.Rows()
		if s := ts.sink; !ts.done && (len(s.parts) > 2 || len(s.segs) > 2) {
			t.Fatalf("after %d windows out: %d batches and %d segments held", got, len(s.parts), len(s.segs))
		}
	}
	if got != rows*batches {
		t.Fatalf("%d windows, want %d", got, rows*batches)
	}
}
