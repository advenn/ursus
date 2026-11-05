package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// Tier 2 of v0.6.6-scope.md (step 284): rolling_skew, rolling_kurtosis, rolling_rank,
// rolling_cov and rolling_corr. Their answers are Polars 2.1.0's, recorded in the
// rollmore group of testdata/oracle and spelled in oracle_rollmore_test.go since step
// 496, which took this file's 97 hand-typed lines of values. What stays asserts the
// refusals, an invariance, and the streamed answer against the in-memory one.

func TestRollingSkewAndKurtosis(t *testing.T) {
	c := ursus.Col
	x := c("x")
	ints := ursus.Frame(ursus.Values("i", []int64{1, 4, 2}))
	for name, q := range map[string]*ursus.LazyFrame{
		// Polars: "`rolling_skew` operation not supported for dtype ...".
		"a Boolean": ursus.Frame(ursus.Values("x", []bool{true, false, true})).Select(x.RollingSkew(3, true)),
		"a date":    ints.Select(c("i").Cast(ursus.Date).RollingKurtosis(3, true, true)),
		"a Decimal": ints.Select(c("i").Cast(ursus.Decimal(38, 1)).RollingSkew(3, true)),
		"text":      ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.RollingSkew(1, true)),
	} {
		if _, err := q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "not defined") {
			t.Errorf("%s: got %v, want a refusal", name, err)
		}
	}
}

func TestRollingRank(t *testing.T) {
	c := ursus.Col
	x := c("x")
	ints := ursus.Frame(ursus.Values("x", []int64{1, 4, 2}))
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "rolling_rank operation not supported for dtype `str`".
		"text":      {ursus.Frame(ursus.Values("x", []string{"b", "a"})).Select(x.RollingRank(2, ursus.RankMin)), "not defined"},
		"a Decimal": {ints.Select(x.Cast(ursus.Decimal(38, 1)).RollingRank(2, ursus.RankMin)), "not defined"},
		// Polars: "rank `method` must be one of {'min', 'max', 'average', 'dense', 'random'}".
		"ordinal": {ints.Select(x.RollingRank(2, ursus.RankOrdinal)), "min, max, average or dense"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestRollingCovAndCorr: a correlation is invariant of ddof, as Polars 1.44.1 said of
// the argument it took ("Correlation is invariant of `ddof`"), so RollingCorr with
// Ddof(0) answers as RollingCorr without; and a covariance of text is refused.
func TestRollingCovAndCorr(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(
		ursus.ValuesNullable("x", []float64{1, 4, 2, 8, 0, 3, 3, 9, -1, 5},
			[]bool{true, true, true, true, false, true, true, true, true, true}),
		ursus.ValuesNullable("y", []float64{2, 1, 5, 3, 4, 0, 6, 2, 0, 7},
			[]bool{true, true, true, true, true, false, true, true, true, true}))
	plain, err := lf.Select(ursus.RollingCorr(c("x"), c("y"), 4)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ddof0, err := lf.Select(ursus.RollingCorr(c("x"), c("y"), 4, ursus.Ddof(0))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, ddof0, plain)
	if _, err := ursus.Frame(ursus.Values("x", []string{"a"}), ursus.Values("y", []float64{1})).
		Select(ursus.RollingCov(c("x"), c("y"), 1)).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Errorf("cov of text: %v", err)
	}
}

// TestRollingMoreStreamPastTheirLimit: each of step 284's rolling functions has a
// bounded need, its window's rows, so over the whole frame it streams past its limit
// as the rolling functions before it do (step 197), and over a key its partitions
// spill. 20,000 rows in batches of 64 under a 16 KiB limit, against the answer
// without one: the ranks equal, and the moments, covariance and correlation to the
// bit. They were within 1e-9: streamed, a window's state was rebuilt from each
// batch's history rows, so its rounding fell elsewhere; it is carried from batch to
// batch since steps 429 and 430.
func TestRollingMoreStreamPastTheirLimit(t *testing.T) {
	const n = 20_000
	v, vok, f, k := make([]int64, n), make([]bool, n), make([]float64, n), make([]int64, n)
	for i := range n {
		v[i], vok[i] = int64(i%977)-400, i%300 >= 150 || i%7 != 0
		f[i] = float64(i%61)/4 + float64(i%13)
		k[i] = int64(i % 3)
	}
	frame := ursus.Frame(ursus.ValuesNullable("v", v, vok), ursus.Values("f", f), ursus.Values("k", k))
	c := ursus.Col
	for name, cols := range map[string][]ursus.Expr{
		"the whole frame": {
			c("f").RollingSkew(7, true).Alias("sk"),
			c("v").RollingKurtosis(6, true, false, ursus.RollingCenter(), ursus.MinSamples(4)).Alias("ku"),
			c("v").RollingRank(9, ursus.RankDense).Alias("rd"),
			c("f").RollingRank(5, ursus.RankAverage, ursus.RollingCenter()).Alias("ra"),
			ursus.RollingCov(c("v"), c("f"), 8).Alias("cov"),
			ursus.RollingCorr(c("v"), c("f"), 10, ursus.MinSamples(4)).Alias("corr"),
		},
		"over a key": {
			c("f").RollingSkew(7, false).Over(c("k")).Alias("sk"),
			c("v").RollingRank(9, ursus.RankMax).Over(c("k")).Alias("rm"),
			ursus.RollingCorr(c("v"), c("f"), 10).Over(c("k")).Alias("corr"),
		},
	} {
		lf := frame.WithColumns(cols...)
		want, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s, under a 16 KiB limit: %v", name, err)
		}
		if st.Spills == 0 {
			t.Fatalf("%s answered without spilling, so it proves nothing", name)
		}
		var exact []string
		for _, col := range want.Columns() {
			wc, err := want.Column[float64](col)
			if err != nil {
				exact = append(exact, col)
				continue
			}
			gc, err := got.Column[float64](col)
			if err != nil {
				t.Fatalf("%s: %s is not a Float64 streamed: %v", name, col, err)
			}
			for i := range want.Height() {
				w, wok := wc.Get(i)
				g, gok := gc.Get(i)
				if wok != gok || wok && math.Float64bits(g) != math.Float64bits(w) {
					t.Fatalf("%s: %s row %d streamed %v (%v), want %v (%v)", name, col, i, g, gok, w, wok)
				}
			}
		}
		keep := make([]ursus.Expr, len(exact))
		for i, col := range exact {
			keep[i] = c(col)
		}
		g, err := got.Select(t.Context(), keep...)
		if err != nil {
			t.Fatal(err)
		}
		w, err := want.Select(t.Context(), keep...)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, g, w)
	}
}
