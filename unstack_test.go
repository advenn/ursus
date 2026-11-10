package ursus_test

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/uerr"
)

// TestUnstack is tier 2 of v0.6.7-scope.md (step 320): Polars' unstack. Its grids,
// fills and padded names are the unstack group's, Polars 2.1.0's
// (oracle_unstack_test.go); here stay the frame of no rows, which Polars refuses, the
// memory limit and the refusals.
func TestUnstack(t *testing.T) {
	ctx := t.Context()
	grid := dfOf(t, strs("x", "a", "b", "c", "d", "e", "f", "g", "h"), i64s("y", 0, 1, 2, 3, 4, 5, 6, 7))
	ints := func(name string, n int) *ursus.DataFrame {
		vs := make([]any, n)
		for i := range vs {
			vs[i] = i
		}
		return dfOf(t, i64s(name, vs...))
	}

	t.Run("no rows", func(t *testing.T) {
		// Polars refuses an empty frame, by a logarithm of no columns; here it is the
		// grid of no values.
		empty := ints("x", 0)
		got, err := empty.Unstack(ctx, ursus.UnstackOptions{Step: 2})
		if err != nil || got.Width() != 0 || got.Height() != 0 {
			t.Errorf("vertically: %v, %v", got, err)
		}
		got, err = empty.Unstack(ctx, ursus.UnstackOptions{Step: 2, How: ursus.UnstackHorizontal})
		if err != nil || !slices.Equal(got.Columns(), []string{"x_0", "x_1"}) || got.Height() != 0 {
			t.Errorf("horizontally: %v, %v", got, err)
		}
	})

	t.Run("past the memory limit", func(t *testing.T) {
		// The largest step pads three rows with 2^31 slots in each of eight
		// columns, 2^37 bytes: refused before any is made.
		wide := dfOf(t, i64s("a", 1, 2, 3), i64s("b", 1, 2, 3), i64s("c", 1, 2, 3), i64s("d", 1, 2, 3),
			i64s("e", 1, 2, 3), i64s("f", 1, 2, 3), i64s("g", 1, 2, 3), i64s("h", 1, 2, 3))
		if lim := execopt.DefaultLimit(); lim == 0 || lim >= 1<<37 {
			t.Skipf("no default limit below 2^37 bytes here: %d", lim)
		}
		_, err := wide.Unstack(ctx, ursus.UnstackOptions{Step: math.MaxInt32})
		if !errors.Is(err, uerr.ErrResource) {
			t.Errorf("a step of 2^31-1: %v", err)
		}
	})

	for _, tc := range []struct {
		o    ursus.UnstackOptions
		want string
	}{
		{ursus.UnstackOptions{}, "a step of at least 1"},
		{ursus.UnstackOptions{Step: -2}, "a step of at least 1"},
		{ursus.UnstackOptions{Step: 2, Fill: []any{1, 2, 3}}, "Fill gives 3 values for 2 columns"},
		{ursus.UnstackOptions{Step: 2, Columns: []string{"nope"}}, "nope"},
	} {
		if _, err := grid.Unstack(ctx, tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", fmt.Sprint(tc.o), err, tc.want)
		}
	}
}
