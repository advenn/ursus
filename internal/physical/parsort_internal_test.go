package physical

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// TestASortFindsEachRowsBatch is step 361's: the sorted rows' places in their
// batches are found by a shift when every batch but the last holds a power of two's
// rows, and by a search otherwise. Each layout answers as the stable sort does, on
// one thread and on four: a last batch shorter, longer, a middle batch short, none of
// a power of two, one batch alone.
func TestASortFindsEachRowsBatch(t *testing.T) {
	sch := testSchema(t)
	keys := []plan.SortKey{{Expr: &expr.Col{Name: "k"}}}
	kSch, err := keySchema(keys, sch)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(361, 4))
	for _, layout := range [][]int{{8, 8, 8, 3}, {8, 8, 8, 20}, {8, 5, 8, 8}, {6, 6, 6}, {16}, {1, 1, 1}} {
		for _, threads := range []int{1, 4} {
			s := &sortSink{schema: sch, keys: keys, kSch: kSch, batch: 8, threads: threads}
			type row struct{ k, v int64 }
			var all []row
			for _, rows := range layout {
				ks, vs := make([]int64, rows), make([]int64, rows)
				for i := range ks {
					ks[i], vs[i] = int64(rng.IntN(5)), int64(len(all))
					all = append(all, row{ks[i], vs[i]})
				}
				if err := s.Consume(t.Context(), testBatch(t, sch, ks, vs)); err != nil {
					t.Fatal(err)
				}
			}
			op, err := s.Finish(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var got []row
			for _, b := range drain(t, op) {
				ks, vs := data.MustValues[int64](b.Column(0)), data.MustValues[int64](b.Column(1))
				for j := range ks {
					got = append(got, row{ks[j], vs[j]})
				}
			}
			op.Close()
			slices.SortStableFunc(all, func(a, b row) int { return int(a.k - b.k) })
			if !slices.Equal(got, all) {
				t.Errorf("batches of %v on %d threads: %v, want %v", layout, threads,
					fmt.Sprint(got), fmt.Sprint(all))
			}
		}
	}
}

// TestASortsGatherAheadKeepsItsErrors: a batch gathered ahead that fails, by an error
// or a panic, fails in its turn, after the batches before it, and Close waits for
// the window.
func TestASortsGatherAheadKeepsItsErrors(t *testing.T) {
	sch := testSchema(t)
	src := testBatch(t, sch, []int64{1, 2, 3, 4}, []int64{5, 6, 7, 8})
	for _, bad := range []rowRef{{src: 3}, {src: 0, row: 99}} {
		pick := []rowRef{{0, 0}, {0, 1}, {0, 2}, {0, 3}, bad, {0, 0}, {0, 1}, {0, 2}}
		r := &sortedBufferRun{schema: sch, srcs: []*data.Batch{src}, pick: pick, n: 2, threads: 4}
		for i := range 2 {
			if b, err := r.Next(); err != nil || b.Rows() != 2 {
				t.Fatalf("%+v: batch %d: %v", bad, i, err)
			}
		}
		if _, err := r.Next(); err == nil || errors.Is(err, io.EOF) {
			t.Errorf("%+v: the third batch's gather answered %v", bad, err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
