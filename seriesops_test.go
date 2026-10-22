package ursus_test

import (
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/advenn/ursus"
)

// TestSeriesOperations is V3 of v0.6.7-scope.md (step 319): a Series' operations, as
// Polars' Series has them, each the engine's over a one-column frame. Every answer is
// Polars 1.44.1's.
func TestSeriesOperations(t *testing.T) {
	ctx := t.Context()
	df, err := ursus.Frame(i64s("x", 3, nil, 1, 3, 2), strs("s", "b", "a", nil, "a", "c")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	x, err := df.Column[int64]("x")
	if err != nil {
		t.Fatal(err)
	}
	vals := func(s *ursus.Series[int64]) []string {
		out := make([]string, s.Len())
		for i := range out {
			if v, ok := s.Get(i); ok {
				out[i] = strconv.FormatInt(v, 10)
			} else {
				out[i] = "null"
			}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		s    func() (*ursus.Series[int64], error)
		want []string
	}{
		{"Sort", func() (*ursus.Series[int64], error) { return x.Sort(ctx) }, []string{"null", "1", "2", "3", "3"}},
		{"SortDesc", func() (*ursus.Series[int64], error) { return x.SortDesc(ctx) }, []string{"null", "3", "3", "2", "1"}},
		{"Unique", func() (*ursus.Series[int64], error) { return x.Unique(ctx) }, []string{"3", "null", "1", "2"}},
		{"Reverse", func() (*ursus.Series[int64], error) { return x.Reverse(ctx) }, []string{"2", "3", "1", "null", "3"}},
		{"DropNulls", func() (*ursus.Series[int64], error) { return x.DropNulls(ctx) }, []string{"3", "1", "3", "2"}},
		{"FillNull forward", func() (*ursus.Series[int64], error) { return x.FillNull(ctx, ursus.FillForward) },
			[]string{"3", "3", "1", "3", "2"}},
		{"Head", func() (*ursus.Series[int64], error) { return x.Head(2), nil }, []string{"3", "null"}},
		{"Tail", func() (*ursus.Series[int64], error) { return x.Tail(2), nil }, []string{"3", "2"}},
		{"Slice", func() (*ursus.Series[int64], error) { return x.Slice(1, 2), nil }, []string{"null", "1"}},
		{"Slice from the end", func() (*ursus.Series[int64], error) { return x.Slice(-2, 5), nil }, []string{"3", "2"}},
		{"Slice before the start", func() (*ursus.Series[int64], error) { return x.Slice(-7, 3), nil }, []string{"3"}},
	} {
		s, err := tc.s()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := vals(s); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := x.Rename("y").Name(); got != "y" {
		t.Errorf("Rename: %q", got)
	}

	// The aggregates: Polars' 3, 1, 9, 2.25, 2.5, and a std and var of one degree.
	if v, ok, err := x.Max(ctx); err != nil || !ok || v != 3 {
		t.Errorf("Max: %d %v %v", v, ok, err)
	}
	if v, ok, err := x.Min(ctx); err != nil || !ok || v != 1 {
		t.Errorf("Min: %d %v %v", v, ok, err)
	}
	if v, err := x.Sum(ctx); err != nil || v != 9 {
		t.Errorf("Sum: %d %v", v, err)
	}
	if v, ok, err := x.Mean(ctx); err != nil || !ok || v != 2.25 {
		t.Errorf("Mean: %v %v %v", v, ok, err)
	}
	if v, ok, err := x.Median(ctx); err != nil || !ok || v != 2.5 {
		t.Errorf("Median: %v %v %v", v, ok, err)
	}
	if v, ok, err := x.Var(ctx, 1); err != nil || !ok || math.Abs(v-0.9166666666666666) > 1e-15 {
		t.Errorf("Var: %v %v %v", v, ok, err)
	}
	if v, ok, err := x.Std(ctx, 1); err != nil || !ok || math.Abs(v-0.9574271077563381) > 1e-15 {
		t.Errorf("Std: %v %v %v", v, ok, err)
	}
	if n, err := x.Count(ctx); err != nil || n != 4 {
		t.Errorf("Count: %d %v", n, err)
	}
	// Polars' n_unique counts the null, 4; ursus's NUnique does not (§9).
	if n, err := x.NUnique(ctx); err != nil || n != 3 {
		t.Errorf("NUnique: %d %v", n, err)
	}

	t.Run("Cast", func(t *testing.T) {
		f, err := x.Cast[float64](ctx, ursus.Float64)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := f.Get(2); !ok || v != 1.0 {
			t.Errorf("the cast's third value is %v, %v", v, ok)
		}
		if _, err := x.Cast[string](ctx, ursus.Float64); err == nil {
			t.Error("a Float64 read as a string was taken")
		}
	})

	t.Run("none", func(t *testing.T) {
		empty := x.Head(0)
		if _, ok, err := empty.Max(ctx); err != nil || ok {
			t.Errorf("the max of none: %v %v", ok, err)
		}
		if _, ok, err := empty.Mean(ctx); err != nil || ok {
			t.Errorf("the mean of none: %v %v", ok, err)
		}
	})

	t.Run("text", func(t *testing.T) {
		s, err := df.Column[string]("s")
		if err != nil {
			t.Fatal(err)
		}
		sorted, err := s.Sort(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := texts(t, mustFrame(t, sorted.Column()), "s"); !slices.Equal(got, []string{"null", "a", "a", "b", "c"}) {
			t.Errorf("sorted text: %q", got)
		}
		if _, err := s.Sum(ctx); err == nil {
			t.Error("a sum of text was taken")
		}
	})
}

func mustFrame(t *testing.T, c *ursus.Column) *ursus.DataFrame {
	t.Helper()
	df, err := ursus.Frame(c).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df
}
