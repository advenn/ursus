package physical

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// TestATailKeepsOnlyWhatItsLastRowsNeed: past the budget a tail writes the rows it
// holds to runs (step 217), and drops a run, file and all, once the rows after it
// cover n; so after the input it holds n rows and less than a piece more, and its
// directory a file a run. Sixty batches of 100 rows, the last 1,500, under a budget a
// batch or two can pass; and with no budget, where it holds n rows and a batch.
func TestATailKeepsOnlyWhatItsLastRowsNeed(t *testing.T) {
	ctx := t.Context()
	sch := dtype.MustSchema(dtype.Of("x", dtype.Int64))
	var bs []*data.Batch
	for b := range 60 {
		v := make([]int64, 100)
		for i := range v {
			v[i] = int64(b*100 + i)
		}
		batch, err := data.NewBatch(sch, []*data.Column{data.NewFixed("x", dtype.Int64, v, bitmap.AllSet(100))})
		if err != nil {
			t.Fatal(err)
		}
		bs = append(bs, batch)
	}
	src, err := memsrc.New(sch, bs...)
	if err != nil {
		t.Fatal(err)
	}
	budget := execopt.NewBudget(2000, t.TempDir())
	child, err := planPipelined(ctx, &plan.Scan{Src: src, Full: sch},
		Options{Threads: 1, BatchSize: 100, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	const n = 1_500
	op := &tailOp{child: child, n: n, mem: budget.Account("tail"), budget: budget}
	defer op.Close()
	got := int64(0)
	next := int64(60*100 - n)
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range data.MustValues[int64](b.Column(0)) {
			if v != next {
				t.Fatalf("row %d of the tail is %d, want %d", got, v, next)
			}
			next++
			got++
		}
		if got == 100 { // the pieces as drain left them, after the first batch out
			first := op.pieces[0].rows
			if op.held-first >= n || op.held < n {
				t.Fatalf("holds %d rows in %d pieces, the first of %d: more than n needs", op.held,
					len(op.pieces), first)
			}
			if budget.Spills() == 0 {
				t.Fatal("nothing spilled")
			}
			runs := 0
			for _, p := range op.pieces {
				if p.run != "" {
					runs++
				}
			}
			if files, _ := os.ReadDir(op.dir); len(files) != runs {
				t.Fatalf("%d files for %d runs: a dropped run's file is kept", len(files), runs)
			}
		}
	}
	if got != n {
		t.Fatalf("%d rows, want %d", got, n)
	}
}

// TestATailWithNoBudgetHoldsNAndABatch: with nothing to spill to, the ring drops
// whole batches from the front while the rest covers n.
func TestATailWithNoBudgetHoldsNAndABatch(t *testing.T) {
	ctx := t.Context()
	sch := dtype.MustSchema(dtype.Of("x", dtype.Int64))
	var bs []*data.Batch
	for range 60 {
		batch, err := data.NewBatch(sch, []*data.Column{data.NewFixed("x", dtype.Int64, make([]int64, 100),
			bitmap.AllSet(100))})
		if err != nil {
			t.Fatal(err)
		}
		bs = append(bs, batch)
	}
	src, err := memsrc.New(sch, bs...)
	if err != nil {
		t.Fatal(err)
	}
	budget := execopt.NewBudget(0, "")
	child, err := planPipelined(ctx, &plan.Scan{Src: src, Full: sch},
		Options{Threads: 1, BatchSize: 100, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	op := &tailOp{child: child, n: 1_500, mem: budget.Account("tail"), budget: budget}
	defer op.Close()
	if _, err := op.Next(ctx); err != nil {
		t.Fatal(err)
	}
	if op.held >= 1_500+100 {
		t.Fatalf("holds %d rows for a tail of 1,500 in batches of 100", op.held)
	}
}
