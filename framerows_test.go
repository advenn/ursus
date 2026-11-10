package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestRowShortcuts is R5 of v0.6.7-scope.md (step 293): Polars' shift, gather_every,
// clear, first, last, is_empty and equals of a frame. The frames and the Booleans
// equals answers are the framerows group's, Polars 2.1.0's (oracle_framerows_test.go);
// here stay the refusals, is_empty and the eager shift.
func TestRowShortcuts(t *testing.T) {
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2, 3, 4, 5}),
		ursus.Values("s", []string{"p", "q", "r", "s", "t"}),
		ursus.ValuesNullable("f", []float64{1.5, 2.5, 0, 4.5, 5.5}, []bool{true, true, false, true, true}))
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "gather_every(n): n should be positive".
		"every 0": {lf.GatherEvery(0, 0), "positive n"},
		// Polars makes a and f text; ursus's numbers do not meet text.
		"a text fill": {lf.ShiftFill(1, "x"), `column "a"`},
		"clear -1":    {lf.Clear(-1), "-1"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := lf.Head(0).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.IsEmpty() || !empty.IsEmpty() {
		t.Errorf("is_empty: %v, %v", df.IsEmpty(), empty.IsEmpty())
	}
	if got, err := df.Shift(t.Context(), 1); err != nil || got.Height() != 5 {
		t.Errorf("eager shift: %v, %v", got, err)
	}
}
