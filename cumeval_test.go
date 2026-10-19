package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestCumulativeEval is tier 2 of v0.6.6-scope.md (step 286): Polars'
// cumulative_eval. Every answer is Polars 1.44.1's.
func TestCumulativeEval(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	x := c("x")
	lf := ursus.Frame(
		ursus.ValuesNullable("x", []float64{1, 2, 0, 4, 8}, []bool{true, true, false, true, true}),
		ursus.Values("g", []int64{1, 1, 1, 2, 2}),
		ursus.Values("i", []int64{3, 1, 4, 1, 5}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"first less last squared", lf.Select(x.CumulativeEval(el().First().Sub(el().Last().Pow(2)))),
			[]any{0.0, -3.0, nil, -15.0, -63.0}},
		{"sum", lf.Select(x.CumulativeEval(el().Sum())), []any{1.0, 3.0, 3.0, 7.0, 15.0}},
		{"mean from 2", lf.Select(x.CumulativeEval(el().Mean(), ursus.MinSamples(2))),
			[]any{nil, 1.5, 1.5, 2.3333333333333335, 3.75}},
		{"sum over g", lf.Select(x.CumulativeEval(el().Sum()).Over(c("g"))), []any{1.0, 3.0, 3.0, 4.0, 12.0}},
		{"a rank's last", lf.Select(c("i").CumulativeEval(el().Rank(ursus.RankAverage, false).Last())),
			[]any{1.0, 1.0, 3.0, 1.5, 5.0}},
		{"a null first", ursus.Frame(ursus.ValuesNullable("x", []float64{0, 1, 2}, []bool{false, true, true})).
			Select(x.CumulativeEval(el().Sum())), []any{nil, 1.0, 3.0}},
		{"a null first, first", ursus.Frame(ursus.ValuesNullable("x", []float64{0, 1, 2}, []bool{false, true, true})).
			Select(x.CumulativeEval(el().First())), []any{nil, nil, nil}},
	} {
		if got := typedCells[float64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	if got := typedCells[uint32](t, lf.Select(x.CumulativeEval(el().Count()))); !sameCells(got,
		[]any{uint32(1), uint32(2), uint32(2), uint32(3), uint32(4)}) {
		t.Errorf("count: %v", got)
	}
	if got := typedCells[uint32](t, lf.Select(x.CumulativeEval(el().Len()))); !sameCells(got,
		[]any{uint32(1), uint32(2), uint32(3), uint32(4), uint32(5)}) {
		t.Errorf("len: %v", got)
	}
	if got := typedCells[int64](t, lf.Select(c("i").CumulativeEval(el().Max().Sub(el().Min())))); !sameCells(got,
		[]any{int64(0), int64(2), int64(3), int64(3), int64(4)}) {
		t.Errorf("integers, max less min: %v", got)
	}
	if got := typedCells[string](t, ursus.Frame(ursus.Values("s", []string{"b", "a", "c"})).
		Select(c("s").CumulativeEval(el().Min()))); !sameCells(got, []any{"b", "a", "a"}) {
		t.Errorf("text, min: %v", got)
	}
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
