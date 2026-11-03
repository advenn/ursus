package execopt_test

import (
	"testing"

	"github.com/advenn/ursus/internal/execopt"
)

// TestBudgetHoldsASliceAsItsAllocation (step 424): a batch's head and the batch are
// one allocation, held at its size whichever is retained first and by whichever
// account, and released only when the last holder lets go. Alone, the head is its
// own bytes.
func TestBudgetHoldsASliceAsItsAllocation(t *testing.T) {
	b := batch(t, "v", make([]int64, 1024))
	whole := b.NBytes()
	for _, part := range []struct {
		name string
		lo   int
	}{{"its head", 0}, {"its tail", 1}} {
		p := b.Slice(part.lo, 10)

		bud := execopt.NewBudget(0, "")
		a := bud.Account("sort")
		a.Retain(p)
		if a.Used() != 10*8 || bud.Used() != 10*8 {
			t.Errorf("%s alone: account %d, budget %d, want its own %d", part.name, a.Used(), bud.Used(), 10*8)
		}
		a.Retain(b)
		if a.Used() != whole || bud.Used() != whole {
			t.Errorf("one account, %s then the batch: account %d, budget %d, want %d",
				part.name, a.Used(), bud.Used(), whole)
		}
		a.Release()
		if bud.Used() != 0 {
			t.Errorf("one account, %s: %d after release, want 0", part.name, bud.Used())
		}

		bud = execopt.NewBudget(0, "")
		one, two := bud.Account("sort"), bud.Account("join")
		one.Retain(p)
		two.Retain(b)
		if bud.Used() != whole || bud.Peak() != whole {
			t.Errorf("%s in one account, the batch in another: used %d, peak %d, want %d",
				part.name, bud.Used(), bud.Peak(), whole)
		}
		two.Release()
		if bud.Used() != whole {
			t.Errorf("%s still held: %d, want the allocation's %d", part.name, bud.Used(), whole)
		}
		one.Release()
		if bud.Used() != 0 {
			t.Errorf("%s: %d after both released, want 0", part.name, bud.Used())
		}
	}
}
