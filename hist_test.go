package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestHist is tier 2 of v0.6.6-scope.md (step 286): Polars' hist. Its answers are
// Polars 2.1.0's since step 500, recorded in the hist group of testdata/oracle and
// spelled in oracle_hist_test.go, which took this file's tables. What stays asserts
// the refusals.
func TestHist(t *testing.T) {
	x := ursus.Col("x")
	h := ursus.Frame(ursus.ValuesNullable("x", []float64{1, 2, 2.5, 4, 8, 0, math.NaN()},
		[]bool{true, true, true, true, true, false, true}))
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "bins must increase monotonically".
		"edges falling": {h.Select(x.Hist(ursus.HistBins(5, 0, 2))), "must increase"},
		// Polars: "can only provide one of `bin_count` or `bins`".
		"both": {h.Select(x.Hist(ursus.HistBins(0, 1), ursus.HistBinCount(2))), "not both"},
		// Polars: "'hist' is only supported for numeric data".
		"text":   {ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.Hist()), "only defined for numbers"},
		"a date": {h.Select(x.Cast(ursus.Int32).Cast(ursus.Date).Hist()), "only defined for numbers"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
