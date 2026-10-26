package physical

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/plan"
)

// passOp hands each batch on.
type passOp struct{ s *dtype.Schema }

func (o passOp) Schema() *dtype.Schema { return o.s }
func (o passOp) Close() error          { return nil }
func (o passOp) Apply(_ context.Context, in *data.Batch) (*data.Batch, error) {
	return in, nil
}

// TestAProfiledPipelineStaysParallel: a profile wraps a stage chain's batch
// transforms, not its stages, so the parallel pool still finds the pipeline by its
// stages' type and runs it on its workers, a profile's edge timing the parallel
// operator in the chain's edge's place (step 369); and a node's transforms count its
// rows, on whichever worker runs them.
func TestAProfiledPipelineStaysParallel(t *testing.T) {
	sch := dtype.MustSchema(dtype.Of("x", dtype.Int64))
	var batches []*data.Batch
	for i := range 40 {
		b, err := data.NewBatch(sch, []*data.Column{
			data.NewFixed("x", dtype.Int64, []int64{int64(i), int64(i)}, bitmap.View{})})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	chain := func() Operator {
		return &stage{child: &stage{child: newBatchOperator(sch, batches...), op: passOp{sch}}, op: passOp{sch}}
	}

	if _, ok := parallelise(chain(), Options{Threads: 4}).(*parallelOp); !ok {
		t.Fatal("the chain is not parallelised unprofiled")
	}

	p := NewProfiler(time.Now(), execopt.NewBudget(0, ""))
	parent := p.enter(&plan.Limit{N: 1})
	pn := p.leave(parent)
	op := p.wrap(chain(), pn)
	base, ops, ok := pipelineOf(op)
	if !ok || len(ops) != 2 {
		t.Fatalf("a profiled chain is a pipeline %v of %d transforms, want 2", ok, len(ops))
	}
	if b, ok := base.(*profOp); !ok || !b.base || b.edge {
		t.Errorf("the chain's base is %T, want its node's base", base)
	}
	for i, o := range ops {
		if po, ok := o.(*profBatchOp); !ok || po.count != (i == 1) {
			t.Errorf("transform %d is %T, the node's output %v", i, o, i == 1)
		}
	}
	par := Options{Threads: 4, Profiler: p}.parallelise(op)
	edge, ok := par.(*profOp)
	if !ok {
		t.Fatalf("a profiled pipeline parallelised is %T, want its edge", par)
	}
	if _, ok := edge.inner.(*parallelOp); !ok {
		t.Fatalf("the edge times %T, want the parallel operator", edge.inner)
	}
	rows := 0
	for {
		b, err := par.Next(t.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		rows += b.Rows()
	}
	par.Close()
	if rows != 80 || pn.rows.Load() != 80 || pn.batches.Load() != 40 {
		t.Errorf("%d rows out; the node counted %d rows in %d batches, want 80 in 40",
			rows, pn.rows.Load(), pn.batches.Load())
	}
	if pn.incl.Load() <= 0 || pn.first.Load() < 0 || pn.last.Load() < pn.first.Load() {
		t.Errorf("the node waited %d ns, from %d to %d", pn.incl.Load(), pn.first.Load(), pn.last.Load())
	}
}

// TestAnAccountKeepsItsPeak: an account's peak is the most it held at once, and a
// budget tells a profile of each account opened (step 369).
func TestAnAccountKeepsItsPeak(t *testing.T) {
	b := execopt.NewBudget(0, "")
	var opened []*execopt.Account
	b.OnAccount(func(a *execopt.Account) { opened = append(opened, a) })
	a := b.Account("sort")
	if len(opened) != 1 || opened[0] != a {
		t.Fatalf("%d accounts told of, want the one opened", len(opened))
	}
	a.RetainBytes(100)
	a.RetainBytes(50)
	a.RetainBytes(-120)
	a.RetainBytes(10)
	if a.Peak() != 150 || a.Used() != 40 {
		t.Errorf("peak %d, used %d, want 150 and 40", a.Peak(), a.Used())
	}
	a.Release()
	a.RetainBytes(30)
	if a.Peak() != 150 || a.Used() != 30 {
		t.Errorf("after a release, peak %d, used %d, want 150 and 30", a.Peak(), a.Used())
	}
}
