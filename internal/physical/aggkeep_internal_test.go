package physical

// Step 400: an open group-by sink does not list its batch's rows as resident, all of
// them, as it did a batch at a time; and it gives its group ids a batch's room at
// once (B3 of v0.8-scope.md).

import (
	"io"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// TestAnOpenSinkListsNoRows: a group-by of an integer key, of two, of a string's and of
// an encoded float's, fed 3,000 rows in batches of 500, keeps no list of its rows,
// and its group ids had a batch's room from the first: 500, not the 512 doubling
// grows to.
func TestAnOpenSinkListsNoRows(t *testing.T) {
	k, v := &expr.Col{Name: "k"}, &expr.Col{Name: "v"}
	for _, c := range []struct {
		name string
		keys []expr.Node
		path func(groupKeys) bool
	}{
		{"an integer", []expr.Node{k}, func(g groupKeys) bool { return g.ints != nil }},
		{"two integers", []expr.Node{k, v}, func(g groupKeys) bool { return g.pairs != nil }},
		{"a string", []expr.Node{&expr.Cast{Child: k, To: dtype.String}}, func(g groupKeys) bool { return g.raw }},
		{"a float", []expr.Node{&expr.Binary{Op: expr.OpMul, L: k, R: &expr.Lit{Value: 0.5, DT: dtype.Float64}}},
			func(g groupKeys) bool { return g.ints == nil && g.pairs == nil && !g.raw }},
	} {
		in := keyedScan(t, rand.New(rand.NewPCG(400, 2)), "k", "v", dtype.Int64, 3_000, 100)
		opts := Options{Threads: 1, BatchSize: 500, Budget: execopt.NewBudget(0, "")}
		op, err := planAggregate(t.Context(), &plan.Aggregate{Input: in, Keys: c.keys,
			Aggs: []expr.Node{&expr.Alias{Name: "n", Child: &expr.Agg{Op: expr.AggSum, Child: v}}}}, opts)
		if err != nil {
			t.Fatal(err)
		}
		s := op.(*stage).child.(*breaker).sink.(*hashAggSink)
		if !c.path(s.ids) {
			t.Fatalf("%s: keyed by %+v", c.name, s.ids)
		}
		src, err := planPipelined(t.Context(), in, opts)
		if err != nil {
			t.Fatal(err)
		}
		for {
			b, err := src.Next(t.Context())
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Consume(t.Context(), b); err != nil {
				t.Fatal(err)
			}
			if cap(s.keep) != 0 || cap(s.groups) != 500 {
				t.Fatalf("%s: an open sink's kept rows %d, its group ids' room %d, want none and 500",
					c.name, cap(s.keep), cap(s.groups))
			}
		}
		src.Close()
		op.Close()
	}
}
