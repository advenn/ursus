package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestDuplicatesOverColumns is R2 of v0.6.7-scope.md (step 290): duplicates over
// several columns, and a frame's distinct rows. Every answer is Polars 1.44.1's.
func TestDuplicatesOverColumns(t *testing.T) {
	c := ursus.Col
	nan, negz := math.NaN(), math.Copysign(0, -1)
	lf := ursus.Frame(
		ursus.ValuesNullable("a", []int64{1, 1, 0, 0, 2, 1}, []bool{true, true, false, false, true, true}),
		ursus.ValuesNullable("b", []string{"x", "x", "", "", "y", "z"}, []bool{true, true, false, false, true, true}),
		ursus.Values("f", []float64{nan, nan, 0, negz, 1, 1}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// Nulls with nulls, NaN with NaN and -0 with 0, as Unique compares them.
		{"duplicated a, b", lf.Select(ursus.IsDuplicated(c("a"), c("b"))), "a\ntrue\ntrue\ntrue\ntrue\nfalse\nfalse\n"},
		{"unique a, b", lf.Select(ursus.IsUnique(c("a"), c("b"))), "a\nfalse\nfalse\nfalse\nfalse\ntrue\ntrue\n"},
		{"duplicated a, f", lf.Select(ursus.IsDuplicated(c("a"), c("f"))), "a\ntrue\ntrue\ntrue\ntrue\nfalse\nfalse\n"},
		{"a filter", lf.Filter(ursus.IsUnique(c("a"), c("b"))).Select(c("b")), "b\ny\nz\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dup, err := df.IsDuplicated(t.Context())
	if err != nil || !sameBools(dup.Values(), []bool{true, true, true, true, false, false}) {
		t.Errorf("the frame's duplicated rows: %v, %v", dup, err)
	}
	uniq, err := df.IsUnique(t.Context())
	if err != nil || !sameBools(uniq.Values(), []bool{false, false, false, false, true, true}) {
		t.Errorf("the frame's unique rows: %v, %v", uniq, err)
	}
	for _, tc := range []struct {
		subset []string
		want   int
	}{{nil, 4}, {[]string{"a", "b"}, 4}, {[]string{"a"}, 3}} {
		if got, err := df.NUnique(t.Context(), tc.subset...); err != nil || got != tc.want {
			t.Errorf("n_unique of %v: %d, %v, want %d", tc.subset, got, err, tc.want)
		}
	}
	empty, err := lf.Head(0).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := empty.NUnique(t.Context()); err != nil || got != 0 {
		t.Errorf("n_unique of no rows: %d, %v", got, err)
	}
	if _, err := lf.Select(ursus.IsDuplicated()).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "needs the columns") {
		t.Errorf("no columns: %v", err)
	}
}

func sameBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
