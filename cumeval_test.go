package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestCumulativeEval is tier 2 of v0.6.6-scope.md (step 286): Polars'
// cumulative_eval. Its answers are Polars 2.1.0's since step 500, recorded in the
// cumeval group of testdata/oracle and spelled in oracle_cumeval_test.go, which took
// this file's table. What stays asserts the refusals.
func TestCumulativeEval(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	x := c("x")
	lf := ursus.Frame(
		ursus.ValuesNullable("x", []float64{1, 2, 0, 4, 8}, []bool{true, true, false, true, true}),
		ursus.Values("i", []int64{3, 1, 4, 1, 5}))
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "`cumulative_eval` is not allowed with non-scalar output".
		"a value an element": {lf.Select(x.CumulativeEval(el().Mul(2))), "one value a prefix"},
		// Polars: "named columns are not allowed in `eval` functions".
		"another column": {lf.Select(x.CumulativeEval(c("i").Sum())), `reads col("i")`},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestCumulativeEvalPastAChunk: prefixes are built a run of rows at a time, about a
// million values at once, so a partition of 3,000 rows takes several runs. A sum over
// each prefix is the running sum, which CumSum answers exactly here: whole numbers.
// Under a memory limit it needs its partition in memory, and the refusal says so.
func TestCumulativeEvalPastAChunk(t *testing.T) {
	const n = 3000
	v, k := make([]float64, n), make([]int64, n)
	for i := range n {
		v[i], k[i] = float64(i%97), int64(i%2)
	}
	c, el := ursus.Col, ursus.Element
	big := ursus.Frame(ursus.Values("v", v), ursus.Values("k", k))
	for name, pair := range map[string][2]ursus.Expr{
		"the whole frame": {c("v").CumulativeEval(el().Sum()), c("v").CumSum(false)},
		"over k":          {c("v").CumulativeEval(el().Sum()).Over(c("k")), c("v").CumSum(false).Over(c("k"))},
	} {
		got := typedCells[float64](t, big.Select(pair[0].Alias("a")))
		want := typedCells[float64](t, big.Select(pair[1].Alias("a")))
		if !sameCells(got, want) {
			t.Errorf("%s: the prefixes' sums are not the running sum", name)
		}
	}
	_, err := big.Select(c("v").CumulativeEval(el().Sum()).Alias("a")).Collect(t.Context(),
		ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "but cumulative_eval") {
		t.Errorf("under 16 KiB: %v, want a refusal naming cumulative_eval", err)
	}
}
