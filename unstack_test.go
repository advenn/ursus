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
	"github.com/advenn/ursus/ursustest"
)

// TestUnstack is tier 2 of v0.6.7-scope.md (step 320): Polars' unstack, every answer
// Polars 1.44.1's.
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
	for _, tc := range []struct {
		name string
		df   *ursus.DataFrame
		o    ursus.UnstackOptions
		want []*ursus.Column
	}{
		{"vertical", grid, ursus.UnstackOptions{Step: 3}, []*ursus.Column{strs("x_0", "a", "b", "c"), strs("x_1", "d", "e", "f"),
			strs("x_2", "g", "h", nil), i64s("y_0", 0, 1, 2), i64s("y_1", 3, 4, 5), i64s("y_2", 6, 7, nil)}},
		{"horizontal", grid, ursus.UnstackOptions{Step: 3, How: ursus.UnstackHorizontal}, []*ursus.Column{strs("x_0", "a", "d", "g"),
			strs("x_1", "b", "e", "h"), strs("x_2", "c", "f", nil), i64s("y_0", 0, 3, 6), i64s("y_1", 1, 4, 7), i64s("y_2", 2, 5, nil)}},
		{"a column", grid, ursus.UnstackOptions{Step: 3, Columns: []string{"y"}},
			[]*ursus.Column{i64s("y_0", 0, 1, 2), i64s("y_1", 3, 4, 5), i64s("y_2", 6, 7, nil)}},
		// Horizontally the frame is Step columns wide, whatever its height.
		{"horizontal, two across", ints("x", 10), ursus.UnstackOptions{Step: 2, How: ursus.UnstackHorizontal},
			[]*ursus.Column{i64s("x_0", 0, 2, 4, 6, 8), i64s("x_1", 1, 3, 5, 7, 9)}},
		{"vertical, a step past the height", ints("x", 3), ursus.UnstackOptions{Step: 5},
			[]*ursus.Column{i64s("x_0", 0, 1, 2, nil, nil)}},
		// The fill is cast to each column's type, text to a number null; a value
		// already null stays null.
		{"a fill for all", dfOf(t, i64s("x", 1, 2, 3), strs("s", "a", "b", "c")), ursus.UnstackOptions{Step: 2, Fill: []any{0}},
			[]*ursus.Column{i64s("x_0", 1, 2), i64s("x_1", 3, 0), strs("s_0", "a", "b"), strs("s_1", "c", "0")}},
		{"text for all", dfOf(t, i64s("x", 1, 2, 3), strs("s", "a", "b", "c")), ursus.UnstackOptions{Step: 2, Fill: []any{"z"}},
			[]*ursus.Column{i64s("x_0", 1, 2), i64s("x_1", 3, nil), strs("s_0", "a", "b"), strs("s_1", "c", "z")}},
		{"a fill each", dfOf(t, i64s("x", 1, 2, 3), strs("s", "a", "b", "c")), ursus.UnstackOptions{Step: 2, Fill: []any{2.5, "q"}},
			[]*ursus.Column{i64s("x_0", 1, 2), i64s("x_1", 3, 2), strs("s_0", "a", "b"), strs("s_1", "c", "q")}},
		{"a null kept", dfOf(t, i64s("x", 1, nil, 3)), ursus.UnstackOptions{Step: 2, Fill: []any{0}},
			[]*ursus.Column{i64s("x_0", 1, nil), i64s("x_1", 3, 0)}},
		{"horizontal, filled", dfOf(t, i64s("x", 1, 2, 3)), ursus.UnstackOptions{Step: 2, How: ursus.UnstackHorizontal, Fill: []any{9}},
			[]*ursus.Column{i64s("x_0", 1, 3), i64s("x_1", 2, 9)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.df.Unstack(ctx, tc.o)
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, dfOf(t, tc.want...))
		})
	}

	// The suffixes are padded to the digits of the count of new columns.
	for _, tc := range []struct {
		rows, step int
		how        ursus.UnstackHow
		want       []string
	}{
		{25, 2, ursus.UnstackVertical, []string{"x_00", "x_01", "x_12"}},
		{25, 2, ursus.UnstackHorizontal, []string{"x_0", "x_1"}},
		{100, 1, ursus.UnstackVertical, []string{"x_000", "x_001", "x_099"}},
		{10, 1, ursus.UnstackVertical, []string{"x_00", "x_01", "x_09"}},
		{9, 1, ursus.UnstackVertical, []string{"x_0", "x_1", "x_8"}},
	} {
		got, err := ints("x", tc.rows).Unstack(ctx, ursus.UnstackOptions{Step: tc.step, How: tc.how})
		if err != nil {
			t.Fatal(err)
		}
		names := got.Columns()
		if len(names) > 3 {
			names = append(names[:2], names[len(names)-1])
		}
		if !slices.Equal(names, tc.want) {
			t.Errorf("%d rows a step of %d, %v: names %q, want %q", tc.rows, tc.step, tc.how, names, tc.want)
		}
	}
	last, err := ints("x", 25).Unstack(ctx, ursus.UnstackOptions{Step: 2})
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, collect(t, last.Lazy().Select(ursus.Col("x_12"))), dfOf(t, i64s("x_12", 24, nil)))

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
