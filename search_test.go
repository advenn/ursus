package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// M3 of v0.6.6-scope.md (steps 279 and 280): search_sorted and index_of, the peaks,
// unique_counts and value_counts. Their answers are Polars 2.1.0's since step 500,
// recorded in the search group of testdata/oracle and spelled in
// oracle_search_test.go, which took this file's tables. What stays asserts the
// refusals, and Hash's properties, its numbers being ursus's own.

// searchFrame is Polars' frame: g [1, 1, 1, 2, 2, 2], a [1, 3, 3, 5, None, 9] and s
// ["x", "y", "x", None, "x", "z"].
func searchFrame() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}),
		ursus.ValuesNullable("a", []int64{1, 3, 3, 5, 0, 9}, []bool{true, true, true, true, false, true}),
		ursus.ValuesNullable("s", []string{"x", "y", "x", "", "x", "z"}, []bool{true, true, true, false, true, true}))
}

func TestSearchSortedAndIndexOf(t *testing.T) {
	lf := searchFrame()
	if _, err := lf.Select(ursus.Col("a").SearchSorted(3, "middle", false)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "not a side") {
		t.Errorf("a side that is none: %v", err)
	}
}

func TestPeaks(t *testing.T) {
	if _, err := ursus.Frame(ursus.Values("x", []string{"a", "b"})).Select(ursus.Col("x").PeakMax()).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "no zero") {
		t.Errorf("text: %v", err)
	}
}

// TestHash: equal values hash alike, -0 with 0 and NaN with NaN, a null too; the seed
// changes them. The numbers are ursus's, not Polars' (v0.6.6-scope.md §6.3).
func TestHash(t *testing.T) {
	c := ursus.Col
	x := c("x")
	lf := ursus.Frame(ursus.ValuesNullable("x", []float64{0, math.Copysign(0, -1), math.NaN(), math.NaN(), 0, 1.5},
		[]bool{true, true, true, true, false, true}))
	df, err := lf.Select(x.Hash(0).Alias("h"), x.Hash(9).Alias("s")).Collect(t.Context())
	if err != nil || df.Schema().Field(0).Type != ursus.Uint64 {
		t.Fatalf("%v, %v", df, err)
	}
	rows := strings.Split(strings.TrimSpace(csvOf(t, lf.Select(x.Hash(0).Alias("h"), x.Hash(9).Alias("s")))), "\n")[1:]
	h := func(i int) string { return strings.Split(rows[i], ",")[0] }
	s := func(i int) string { return strings.Split(rows[i], ",")[1] }
	if h(0) != h(1) || h(2) != h(3) {
		t.Errorf("-0 and 0, or NaN and NaN, hashed apart: %v", rows)
	}
	if h(0) == h(2) || h(0) == h(4) || h(0) == h(5) || h(2) == h(4) {
		t.Errorf("unequal values hashed alike: %v", rows)
	}
	if h(4) == "" {
		t.Errorf("a null has no hash: %v", rows)
	}
	for i := range rows {
		if h(i) == s(i) {
			t.Errorf("the seed changed nothing at row %d: %v", i, rows)
		}
	}
	if a, b := csvOf(t, lf.Select(x.Hash(0))), csvOf(t, lf.Select(x.Hash(0))); a != b {
		t.Errorf("one hash, two answers")
	}
}

func TestUniqueAndValueCounts(t *testing.T) {
	c := ursus.Col
	lf := searchFrame()
	if _, err := lf.Select(c("g"), c("s").ValueCounts(false, false)).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "length") {
		t.Errorf("beside a column of another length: %v", err)
	}
}
