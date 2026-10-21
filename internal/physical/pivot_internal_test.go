package physical

import (
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// TestPivotSinkMergeEquivalence (step 303): two pivot sinks, the second holding the
// later pairs, merged, lay them out as one sink holding them all, the index in the
// order each first appears. The merge appends, as reverseSink's must; prepending
// would put index 3 before 2.
func TestPivotSinkMergeEquivalence(t *testing.T) {
	ctx := t.Context()
	in := dtype.MustSchema(dtype.Of("k", dtype.Int64), dtype.Of("on", dtype.String), dtype.Of("v", dtype.Int64),
		dtype.Of("n", dtype.Uint64))
	pairs := func(ks []int64, ons []string, vs []int64) *data.Batch {
		t.Helper()
		ones := make([]uint64, len(ks))
		for i := range ones {
			ones[i] = 1
		}
		b, err := data.NewBatch(in, []*data.Column{
			data.NewFixed("k", dtype.Int64, ks, bitmap.AllSet(len(ks))),
			data.NewString("on", ons, bitmap.AllSet(len(ons))),
			data.NewFixed("v", dtype.Int64, vs, bitmap.AllSet(len(vs))),
			data.NewFixed("n", dtype.Uint64, ones, bitmap.AllSet(len(ones))),
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	scan := func(s *dtype.Schema, bs ...*data.Batch) *plan.Scan {
		t.Helper()
		src, err := memsrc.New(s, bs...)
		if err != nil {
			t.Fatal(err)
		}
		return &plan.Scan{Src: src, Full: s}
	}
	early := pairs([]int64{2, 1}, []string{"x", "y"}, []int64{20, 10})
	late := pairs([]int64{1, 3}, []string{"x", "y"}, []int64{11, 30})
	onSchema := dtype.MustSchema(dtype.Of("on", dtype.String))
	on, err := data.NewBatch(onSchema, []*data.Column{data.NewString("on", []string{"x", "y"}, bitmap.AllSet(2))})
	if err != nil {
		t.Fatal(err)
	}
	p := &plan.Pivot{Input: scan(in, early, late), OnColumns: scan(onSchema, on), Index: []string{"k"},
		On: []string{"on"}, Values: []string{"v"}, Names: []string{"x", "y"}}
	out, err := p.Schema()
	if err != nil {
		t.Fatal(err)
	}
	budget := execopt.NewBudget(0, t.TempDir())
	sink := func() *pivotSink {
		t.Helper()
		op, err := planPipelined(ctx, p.OnColumns, Options{Threads: 1, BatchSize: 100, Budget: budget})
		if err != nil {
			t.Fatal(err)
		}
		return &pivotSink{p: p, in: in, out: out, on: op, mem: budget.Account("pivot")}
	}
	layout := func(s *pivotSink) [][]int64 {
		t.Helper()
		defer s.Close()
		op, err := s.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}
		bs := drain(t, op)
		if len(bs) != 1 {
			t.Fatalf("%d batches", len(bs))
		}
		var cols [][]int64
		for _, c := range bs[0].Columns() {
			vals := slices.Clone(data.MustValues[int64](c))
			for i := range vals {
				if !c.IsValid(i) {
					vals[i] = -1
				}
			}
			cols = append(cols, vals)
		}
		return cols
	}

	one := sink()
	for _, b := range []*data.Batch{early, late} {
		if err := one.Consume(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	want := layout(one)
	if !slices.EqualFunc(want, [][]int64{{2, 1, 3}, {20, 11, -1}, {-1, 10, 30}}, slices.Equal[[]int64]) {
		t.Fatalf("one sink lays out %v", want)
	}
	a, b := sink(), sink()
	if err := a.Consume(ctx, early); err != nil {
		t.Fatal(err)
	}
	if err := b.Consume(ctx, late); err != nil {
		t.Fatal(err)
	}
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	b.Close()
	if got := layout(a); !slices.EqualFunc(got, want, slices.Equal[[]int64]) {
		t.Errorf("merged, %v; one sink, %v", got, want)
	}
}
