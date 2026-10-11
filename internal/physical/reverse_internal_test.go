package physical

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
)

// TestAMergedReverseKeepsItsPiecesInOrder: a reverse sink holds its input as pieces,
// held batches last and runs before them (step 206). Two sinks, each holding its
// share or having spilled it, merge into one that gives the whole input back
// backwards, and leave no file behind.
func TestAMergedReverseKeepsItsPiecesInOrder(t *testing.T) {
	sch := dtype.MustSchema(dtype.Of("x", dtype.Int64))
	batch := func(lo, n int) *data.Batch {
		v := make([]int64, n)
		for i := range v {
			v[i] = int64(lo + i)
		}
		b, err := data.NewBatch(sch, []*data.Column{data.NewFixed("x", dtype.Int64, v, bitmap.AllSet(n))})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, spillA := range []bool{false, true} {
		for _, spillB := range []bool{false, true} {
			name := fmt.Sprintf("first spilled %v, second spilled %v", spillA, spillB)
			dir := t.TempDir()
			// A limit of a byte spills every batch; none, never.
			budget := func(spills bool) *execopt.Budget {
				if spills {
					return execopt.NewBudget(1, dir)
				}
				return execopt.NewBudget(0, dir)
			}
			sink := func(b *execopt.Budget) *reverseSink {
				return &reverseSink{schema: sch, mem: b.Account("reverse"), budget: b}
			}
			a, b := sink(budget(spillA)), sink(budget(spillB))
			for i, s := range []*reverseSink{a, a, b, b, b} {
				if err := s.Consume(t.Context(), batch(i*10, 10)); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Merge(b); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for i, p := range a.pieces {
				if p.run == "" && i != len(a.pieces)-1 {
					t.Fatalf("%s: held batches in piece %d of %d", name, i, len(a.pieces))
				}
			}
			op, err := a.Finish(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var got []int64
			for {
				out, err := op.Next(t.Context())
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				// A run read back is held, and charged, while it is given out.
				if got == nil && spillB && a.mem.Used() == 0 {
					t.Errorf("%s: a run is given out uncharged", name)
				}
				got = append(got, data.MustValues[int64](out.Column(0))...)
			}
			want := make([]int64, 50)
			for i := range want {
				want[i] = int64(49 - i)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: %v", name, got)
			}
			if err := op.Close(); err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(a.Close(), b.Close()); err != nil {
				t.Fatal(err)
			}
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("%s: %d entries left", name, len(left))
			}
		}
	}
}
