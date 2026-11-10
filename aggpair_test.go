package ursus_test

// Step 175: Corr, Cov, MinBy and MaxBy. Their answers are Polars 2.1.0's since step
// 500, recorded in the aggpair group of testdata/oracle and spelled in
// oracle_aggpair_test.go, which took this file's tables. What stays holds many groups
// over many batches to a brute force, and asserts the names, the plan and the refusals.

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// floats175 reads a Float64 column, a null as NaN's opposite: ok false.
func floats175(t *testing.T, df *ursus.DataFrame, name string) ([]float64, []bool) {
	t.Helper()
	col, err := df.Column[float64](name)
	if err != nil {
		t.Fatal(err)
	}
	vals, ok := make([]float64, col.Len()), make([]bool, col.Len())
	for i := range vals {
		vals[i], ok[i] = col.Get(i)
	}
	return vals, ok
}

// near175 is a and b equal to a few ulps, NaN equal to NaN.
func near175(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Abs(a-b) <= 1e-12*math.Max(1, math.Abs(b))
}

// TestPairedAggregatesAgreeWithABruteForce: many groups over many batches, on one
// worker and on four, and under a budget that spills, against answers computed here
// row by row. by takes few values, so ties are common and the earliest must win
// across batches and merges.
func TestPairedAggregatesAgreeWithABruteForce(t *testing.T) {
	const rows, groups = 40_000, 1_500
	rng := rand.New(rand.NewPCG(175, 1))
	g, v, by := make([]int64, rows), make([]int64, rows), make([]int64, rows)
	x, y := make([]float64, rows), make([]float64, rows)
	xok := make([]bool, rows)
	for i := range rows {
		g[i], v[i], by[i] = rng.Int64N(groups), int64(i), rng.Int64N(20)
		x[i] = 1e6 + rng.Float64()*10
		y[i] = 3*x[i] + rng.NormFloat64()
		xok[i] = rng.IntN(10) != 0
	}
	type want struct {
		lo, hi           int64
		n                float64
		sx, sy           float64
		xs, ys           []float64
		loBy, hiBy, seen int64
	}
	ws := make([]want, groups)
	for i := range rows {
		w := &ws[g[i]]
		if w.seen == 0 || by[i] < w.loBy {
			w.lo, w.loBy = v[i], by[i]
		}
		if w.seen == 0 || by[i] > w.hiBy {
			w.hi, w.hiBy = v[i], by[i]
		}
		w.seen++
		if xok[i] {
			w.xs, w.ys = append(w.xs, x[i]), append(w.ys, y[i])
		}
	}
	frame := ursus.Frame(ursus.Values("g", g), ursus.Values("v", v), ursus.Values("by", by),
		ursus.ValuesNullable("x", x, xok), ursus.Values("y", y))
	c := ursus.Col
	var spilled ursus.MemoryStats
	for _, run := range []struct {
		name string
		opts []ursus.CollectOption
	}{
		{"one worker", []ursus.CollectOption{ursus.WithThreads(1), ursus.WithBatchSize(1000)}},
		{"four workers", []ursus.CollectOption{ursus.WithThreads(4), ursus.WithBatchSize(1000)}},
		{"spilling", []ursus.CollectOption{ursus.WithThreads(2), ursus.WithBatchSize(1000),
			ursus.WithMemoryLimit(256 << 10), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&spilled)}},
	} {
		t.Run(run.name, func(t *testing.T) {
			df, err := frame.GroupBy(c("g")).Agg(
				c("v").MinBy(c("by")).Alias("lo"), c("v").MaxBy(c("by")).Alias("hi"),
				ursus.Corr(c("x"), c("y")).Alias("corr"), ursus.Cov(c("x"), c("y"), 1).Alias("cov"),
			).Sort(ursus.Asc(c("g"))).Collect(t.Context(), run.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if df.Height() != groups {
				t.Fatalf("%d groups, want %d", df.Height(), groups)
			}
			lo, _ := df.Column[int64]("lo")
			hi, _ := df.Column[int64]("hi")
			corr, _ := floats175(t, df, "corr")
			cov, covOK := floats175(t, df, "cov")
			for i := range groups {
				w := ws[i]
				if l, _ := lo.Get(i); l != w.lo {
					t.Fatalf("group %d: min_by %d, want %d", i, l, w.lo)
				}
				if h, _ := hi.Get(i); h != w.hi {
					t.Fatalf("group %d: max_by %d, want %d", i, h, w.hi)
				}
				// Two passes, the textbook way, for the reference.
				n := float64(len(w.xs))
				var mx, my float64
				for k := range w.xs {
					mx += w.xs[k] / n
					my += w.ys[k] / n
				}
				var sxy, sxx, syy float64
				for k := range w.xs {
					dx, dy := w.xs[k]-mx, w.ys[k]-my
					sxy, sxx, syy = sxy+dx*dy, sxx+dx*dx, syy+dy*dy
				}
				if r := sxy / math.Sqrt(sxx*syy); math.Abs(corr[i]-r) > 1e-9 {
					t.Fatalf("group %d: corr %v, want %v", i, corr[i], r)
				}
				if len(w.xs) < 2 {
					if covOK[i] {
						t.Fatalf("group %d of %d pairs: cov %v, want null", i, len(w.xs), cov[i])
					}
				} else if want := sxy / (n - 1); math.Abs(cov[i]-want) > 1e-9*math.Max(1, math.Abs(want)) {
					t.Fatalf("group %d: cov %v, want %v", i, cov[i], want)
				}
			}
		})
	}
	if spilled.Spills == 0 {
		t.Errorf("the spilling run wrote no file; its budget no longer forces a spill")
	}
}

// TestPairedAggregatesNameRenderAndExpand: named after the first input, even when
// both are one column; rendered as written; two aggregates of one first input are
// two answers; and a selection of several columns expands to one aggregate each.
func TestPairedAggregatesNameRenderAndExpand(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("a", []float64{1, 2, 4}), ursus.Values("b", []float64{2, 4, 9}),
		ursus.Values("k", []float64{3, 1, 2}))
	lf := frame.GroupBy().Agg(
		ursus.Corr(c("a"), c("b")),
		ursus.Corr(c("b"), c("k")),
		ursus.Cov(c("k"), c("a"), 0),
		ursus.Cov(c("k"), c("b"), 0).Alias("kb"),
		c("k").MaxBy(c("k")).Alias("kk"),
	)
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df.Schema().Names(), ","); got != "a,b,k,kb,kk" {
		t.Fatalf("named %s", got)
	}
	ka, _ := floats175(t, df, "k")
	kb, _ := floats175(t, df, "kb")
	if ka[0] == kb[0] {
		t.Errorf("cov(k, a) and cov(k, b) are one answer, %v", ka[0])
	}
	plan, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`col("a").corr(col("b"))`, `col("k").cov(col("a"), 0)`, `col("k").max_by(col("k"))`} {
		if !strings.Contains(plan, w) {
			t.Errorf("the plan does not show %s:\n%s", w, plan)
		}
	}
	df, err = frame.GroupBy().Agg(ursus.Col("a", "b").MinBy(c("k"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df.Schema().Names(), ","); got != "a,b" {
		t.Errorf("expanded to %s, want a,b", got)
	}
}

func TestPairedAggregatesRefuse(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("s", []string{"x"}), ursus.Values("f", []float64{1}),
		ursus.Values("l", []float64{1})).WithColumns(c("l").Implode().Over(c("s")).Alias("l"))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"corr of a string", ursus.Corr(c("s"), c("f")), "corr() requires numeric operands"},
		{"cov of a string", ursus.Cov(c("f"), c("s"), 1), "cov() requires numeric operands"},
		{"a negative ddof", ursus.Cov(c("f"), c("f"), -1), "ddof must be between 0 and 255"},
		{"min_by a List", c("f").MinBy(c("l")), "has no order"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertUserError(t, frame.GroupBy().Agg(tc.e), tc.want)
		})
	}
}
