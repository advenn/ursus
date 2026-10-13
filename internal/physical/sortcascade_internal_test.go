package physical

import (
	"math/rand/v2"
	"os"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// TestASortMergesItsRunsAsTheyGather: a sort that spills at every batch, past
// sortFanIn² runs, holds fewer than sortFanIn runs of each level, with two levels
// above the first, and still answers as the stable in-memory sort: ties in input
// order, across every merge (step 239).
func TestASortMergesItsRunsAsTheyGather(t *testing.T) {
	sch := testSchema(t)
	budget := execopt.NewBudget(1, t.TempDir()) // over at every batch
	keys := []plan.SortKey{{Expr: &expr.Col{Name: "k"}}}
	kSch, err := keySchema(keys, sch)
	if err != nil {
		t.Fatal(err)
	}
	s := &sortSink{schema: sch, keys: keys, kSch: kSch, budget: budget,
		mem: budget.Account("sort"), batch: 7}
	defer s.Close()
	rng := rand.New(rand.NewPCG(239, 1))
	type row struct{ k, v int64 }
	var all []row
	const runs = sortFanIn*sortFanIn + 20
	for r := range runs {
		ks, vs := make([]int64, 5), make([]int64, 5)
		for i := range ks {
			ks[i], vs[i] = int64(rng.IntN(9)), int64(r*5+i) // v is the input order
			all = append(all, row{ks[i], vs[i]})
		}
		if err := s.Consume(t.Context(), testBatch(t, sch, ks, vs)); err != nil {
			t.Fatal(err)
		}
	}
	top := 0
	per := map[int]int{}
	for _, l := range s.levels {
		per[l]++
		top = max(top, l)
	}
	for l, n := range per {
		if n >= sortFanIn {
			t.Errorf("level %d holds %d runs", l, n)
		}
	}
	if top < 2 {
		t.Errorf("the highest level is %d after %d runs; merged runs were not put up a level", top, runs)
	}
	if files, err := os.ReadDir(s.dir); err != nil || len(files) != len(s.runs) {
		t.Errorf("%d files for %d runs: merged runs were left on disk (%v)", len(files), len(s.runs), err)
	}
	op, err := s.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, op)
	op.Close()
	slices.SortStableFunc(all, func(a, b row) int { return int(a.k - b.k) })
	i := 0
	for _, b := range got {
		ks, vs := data.MustValues[int64](b.Column(0)), data.MustValues[int64](b.Column(1))
		for j := range ks {
			if ks[j] != all[i].k || vs[j] != all[i].v {
				t.Fatalf("row %d is (%d, %d), want (%d, %d)", i, ks[j], vs[j], all[i].k, all[i].v)
			}
			i++
		}
	}
	if i != len(all) {
		t.Fatalf("%d rows out, %d in", i, len(all))
	}
}

// TestASortWithAMinimumRunWaitsForIt: a sort given a minimum run does not spill while
// it holds less, however far over its limit the query is because of another's state,
// and spills once it holds that much (step 239).
func TestASortWithAMinimumRunWaitsForIt(t *testing.T) {
	sch := testSchema(t)
	budget := execopt.NewBudget(1<<10, t.TempDir())
	other := budget.Account("other")
	other.RetainBytes(4 << 10) // the query is over its limit, by someone else
	defer other.Release()
	keys := []plan.SortKey{{Expr: &expr.Col{Name: "k"}}}
	kSch, err := keySchema(keys, sch)
	if err != nil {
		t.Fatal(err)
	}
	s := &sortSink{schema: sch, keys: keys, kSch: kSch, budget: budget,
		mem: budget.Account("sort"), batch: 64, minRun: 600}
	defer s.Close()
	var one int64 // what a batch adds to the sort's holding
	for r := 0; ; r++ {
		held := s.mem.Used()
		if err := s.Consume(t.Context(), testBatch(t, sch, []int64{int64(r)}, []int64{int64(r)})); err != nil {
			t.Fatal(err)
		}
		if len(s.runs) > 0 {
			// It spilled when this batch brought it to the minimum, not before.
			if held+one < s.minRun {
				t.Fatalf("spilled at %d bytes and a batch of %d, under the minimum run of %d",
					held, one, s.minRun)
			}
			return
		}
		if r == 0 {
			one = s.mem.Used()
		}
		if r > 10_000 {
			t.Fatal("never spilled")
		}
	}
}
