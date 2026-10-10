package physical

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
)

// TestEachConcurrentlyReturnsAPanicAsAnError: a join's partitioned build and a
// group-by's partitioned fold fan out through eachConcurrently (step 166). The fold's
// routing goroutines had no guard of their own, so a panic in one ended the process;
// here it is the call's error, and the other workers finish.
func TestEachConcurrentlyReturnsAPanicAsAnError(t *testing.T) {
	err := eachConcurrently(40, 4, func(i int) error {
		if i == 17 {
			var m map[string]int
			m["x"] = 1 // a write to a nil map panics
		}
		return nil
	})
	var pe *uerr.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("a panic in a worker gave %v, not its PanicError", err)
	}

	five := errors.New("five")
	err = eachConcurrently(10, 3, func(i int) error {
		if i == 5 {
			return five
		}
		return nil
	})
	if !errors.Is(err, five) {
		t.Fatalf("a worker's error came back as %v", err)
	}

	var ran atomic.Int64
	seen := make([]atomic.Bool, 100)
	if err := eachConcurrently(len(seen), 7, func(i int) error {
		if seen[i].Swap(true) {
			t.Errorf("item %d ran twice", i)
		}
		ran.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != int64(len(seen)) {
		t.Fatalf("%d of %d items ran", ran.Load(), len(seen))
	}
}
