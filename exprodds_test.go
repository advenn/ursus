package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestExpressionOdds is step 324: Polars' limit, truediv, pipe, arctan2, to_physical,
// reinterpret, item, lower_bound, upper_bound and is_sorted. The answers are Polars
// 2.1.0's, recorded in the exprodds group of testdata/oracle and spelled in
// oracle_exprodds_test.go since step 497, which took this file's pasted values. What
// stays asserts the refusals and is_sorted across batches, a property of a column
// built here.
func TestExpressionOdds(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	ints := ursus.Frame(ursus.Values("a", []int64{3, 1, 2}), ursus.Values("b", []int64{2, 2, 2}))

	t.Run("reinterpret", func(t *testing.T) {
		if _, err := ursus.Frame(ursus.Values("f", []float64{1.5})).Select(c("f").Reinterpret(true)).Collect(ctx); err == nil ||
			!strings.Contains(err.Error(), "reinterpret") {
			t.Errorf("a float reinterpreted: %v", err)
		}
	})

	t.Run("item", func(t *testing.T) {
		empty := ints.Head(0)
		for name, tc := range map[string]struct {
			lf   *ursus.LazyFrame
			want string
		}{
			"three values": {ints.Select(c("a").Item(false)), "expected a single value, got 3 values"},
			"none":         {empty.Select(c("a").Item(false)), "expected a single value, got none"},
			"a group of two": {ursus.Frame(ursus.Values("g", []int64{1, 1, 2}), ursus.Values("v", []int64{1, 2, 3})).
				GroupBy(c("g")).Agg(c("v").Item(false)), "got 2 values"},
		} {
			if _, err := tc.lf.Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want an error saying %q", name, err, tc.want)
			}
		}
	})

	t.Run("bounds", func(t *testing.T) {
		for _, col := range []*ursus.Column{ursus.Values("b", []bool{true}), ursus.Values("s", []string{"a"})} {
			if _, err := ursus.Frame(col).Select(c(col.Name()).LowerBound()).Collect(ctx); err == nil ||
				!strings.Contains(err.Error(), "bound") {
				t.Errorf("the bound of a %s: %v", col.DType(), err)
			}
		}
	})

	t.Run("is_sorted across batches", func(t *testing.T) {
		long := make([]int64, 1000)
		for i := range long {
			long[i] = int64(i / 3)
		}
		got, err := ursus.Frame(ursus.Values("a", long)).Select(c("a").IsSorted(false, false)).
			Collect(ctx, ursus.WithBatchSize(7))
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _ := got.At[bool](0, "a"); !v {
			t.Error("a sorted column read 7 rows at a time is not sorted")
		}
		long[500] = -1
		got, err = ursus.Frame(ursus.Values("a", long)).Select(c("a").IsSorted(false, false)).
			Collect(ctx, ursus.WithBatchSize(7))
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _ := got.At[bool](0, "a"); v {
			t.Error("a column with one value out of place, read 7 rows at a time, is sorted")
		}
	})
}
