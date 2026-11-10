package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/uerr"
)

// TestToDummies is tier 2 of v0.6.7-scope.md (step 320): Polars' to_dummies. Its
// columns, their names by each type's display and their cells are the dummies group's,
// Polars 2.1.0's (oracle_dummies_test.go); here stay the frame of none left, the
// memory limit and the refusals.
func TestToDummies(t *testing.T) {
	ctx := t.Context()
	df := dfOf(t, i64s("foo", 1, 2, 3, 1), strs("bar", "a", "b", nil, "a"), f64s("x", 1.5, 2.5, 3.5, 4.5))

	t.Run("nothing left", func(t *testing.T) {
		got, err := dfOf(t, i64s("c", nil, nil)).ToDummies(ctx, ursus.DummiesOptions{DropNulls: true})
		if err != nil {
			t.Fatal(err)
		}
		if got.Width() != 0 || got.Height() != 0 {
			t.Errorf("no dummies left should be a frame of none, as Polars': %d × %d", got.Height(), got.Width())
		}
	})

	t.Run("past the memory limit", func(t *testing.T) {
		// A million distinct values over a million rows would be 10^12 bytes of
		// dummies: refused before any is made, not the process killed.
		const n = 1_000_000
		if lim := execopt.DefaultLimit(); lim == 0 || lim >= n*n {
			t.Skipf("no default limit below %d bytes here: %d", n*n, lim)
		}
		ids := make([]int64, n)
		for i := range ids {
			ids[i] = int64(i)
		}
		_, err := dfOf(t, ursus.Values("id", ids)).ToDummies(ctx, ursus.DummiesOptions{})
		if !errors.Is(err, uerr.ErrResource) || !strings.Contains(err.Error(), "1000000 values to encode") {
			t.Errorf("a million dummies of a million rows: %v", err)
		}
	})

	t.Run("refused", func(t *testing.T) {
		for name, tc := range map[string]struct {
			df   *ursus.DataFrame
			o    ursus.DummiesOptions
			want string
		}{
			"an unknown column":        {df, ursus.DummiesOptions{Columns: []string{"nope"}}, "nope"},
			"a name twice":             {dfOf(t, strs("c", "a"), i64s("c_a", 1)), ursus.DummiesOptions{Columns: []string{"c"}}, "c_a"},
			"the text null and a null": {dfOf(t, strs("c", "null", nil)), ursus.DummiesOptions{}, "c_null"},
			"a list": {collect(t, ursus.Frame(i64s("a", 1, 2)).Select(ursus.ConcatList(ursus.Col("a")).Alias("l"))),
				ursus.DummiesOptions{}, "List"},
		} {
			if _, err := tc.df.ToDummies(ctx, tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want an error naming %q", name, err, tc.want)
			}
		}
		if _, err := df.ToDummies(ctx, ursus.DummiesOptions{Columns: []string{"nope"}}); !errors.Is(err, uerr.ErrSchema) {
			t.Errorf("an unknown column should be a schema error: %v", err)
		}
	})
}

// dfOf is a frame of these columns.
func dfOf(t *testing.T, cols ...*ursus.Column) *ursus.DataFrame {
	t.Helper()
	return collect(t, ursus.Frame(cols...))
}
