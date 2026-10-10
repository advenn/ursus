package ursus_test

// Step 175: Corr, Cov, MinBy and MaxBy. The expected answers are Polars 1.44.1's,
// from the bench's own environment:
//
//	df.group_by("g").agg(pl.corr("x", "y"), pl.cov("x", "y", ddof=d))
//	df.select(pl.col("v").min_by("by"), pl.col("v").max_by("by"))

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
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

func TestCorrCovAnswerAsPolars(t *testing.T) {
	c := ursus.Col
	nan := math.NaN()
	frame := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a", "b", "b", "c"}),
		ursus.ValuesNullable("x", []float64{1, 2, 4, 1, 0, 3}, []bool{true, true, true, true, false, true}),
		ursus.Values("y", []float64{2, 4, 9, 5, 6, 7}),
	)
	df, err := frame.GroupBy(c("g")).Agg(
		ursus.Corr(c("x"), c("y")).Alias("corr"),
		ursus.Cov(c("x"), c("y"), 1).Alias("cov1"),
		ursus.Cov(c("x"), c("y"), 0).Alias("cov0"),
	).Sort(ursus.Asc(c("g"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Group b has one pair left once its null is dropped; c has one row. Polars'
	// cov1 of c is 0.0, of b null: here both are null (Cov's doc).
	for _, w := range []struct {
		name string
		vals []float64
		ok   []bool
	}{
		{"corr", []float64{0.9986254289035241, nan, nan}, []bool{true, true, true}},
		{"cov1", []float64{5.5, 0, 0}, []bool{true, false, false}},
		{"cov0", []float64{3.6666666666666665, 0, 0}, []bool{true, true, true}},
	} {
		vals, ok := floats175(t, df, w.name)
		for i := range vals {
			if ok[i] != w.ok[i] || (ok[i] && !near175(vals[i], w.vals[i])) {
				t.Errorf("%s of group %d: %v (valid %v), want %v (valid %v)",
					w.name, i, vals[i], ok[i], w.vals[i], w.ok[i])
			}
		}
	}

	for _, tc := range []struct {
		name     string
		x, y     []float64
		xok      []bool
		corr     float64
		cov, ok  bool
		cov1     float64
		cov1Null bool
	}{
		{name: "two rows", x: []float64{1, 2}, y: []float64{2, 4}, corr: 1, cov1: 1},
		{name: "a null x in one of three, dropped pairwise", x: []float64{1, 0, 3}, y: []float64{1, 5, 2},
			xok: []bool{true, false, true}, corr: 1, cov1: 1},
		{name: "a NaN", x: []float64{1, nan, 3}, y: []float64{1, 2, 3}, corr: nan, cov1: nan},
		{name: "every x null", x: []float64{0, 0}, y: []float64{1, 2}, xok: []bool{false, false},
			corr: nan, cov1Null: true},
		{name: "x does not vary", x: []float64{1, 1, 1}, y: []float64{1, 2, 3}, corr: nan, cov1: 0},
		{name: "near-equal large values", x: []float64{1e9 + 1, 1e9 + 2, 1e9 + 4}, y: []float64{2, 4, 9},
			corr: 0.9986254289035241, cov1: 5.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			xok := tc.xok
			if xok == nil {
				xok = make([]bool, len(tc.x))
				for i := range xok {
					xok[i] = true
				}
			}
			df, err := ursus.Frame(ursus.ValuesNullable("x", tc.x, xok), ursus.Values("y", tc.y)).
				GroupBy().Agg(ursus.Corr(c("x"), c("y")).Alias("corr"), ursus.Cov(c("x"), c("y"), 1).Alias("cov1")).
				Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			corr, cok := floats175(t, df, "corr")
			cov, vok := floats175(t, df, "cov1")
			if !cok[0] || !near175(corr[0], tc.corr) {
				t.Errorf("corr %v (valid %v), want %v", corr[0], cok[0], tc.corr)
			}
			if vok[0] == tc.cov1Null || (vok[0] && !near175(cov[0], tc.cov1)) {
				t.Errorf("cov %v (valid %v), want %v (null %v)", cov[0], vok[0], tc.cov1, tc.cov1Null)
			}
		})
	}

	// Integers read as numbers, and a frame's one group as any other.
	df, err = ursus.Frame(ursus.Values("i", []int64{1, 2, 3, 4, 5, 6})).
		GroupBy().Agg(ursus.Corr(c("i"), c("i")).Alias("corr"), ursus.Cov(c("i"), c("i"), 1).Alias("cov")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if corr, _ := floats175(t, df, "corr"); !near175(corr[0], 1) {
		t.Errorf("corr of a column with itself %v", corr[0])
	}
	if cov, _ := floats175(t, df, "cov"); !near175(cov[0], 3.5) {
		t.Errorf("cov of 1..6 with itself %v, want its variance 3.5", cov[0])
	}
}

func TestMinByMaxByAnswerAsPolars(t *testing.T) {
	c := ursus.Col
	nan := math.NaN()
	type pair struct{ lo, hi string }
	run := func(t *testing.T, frame *ursus.LazyFrame) pair {
		t.Helper()
		df, err := frame.GroupBy().Agg(c("v").MinBy(c("by")).Alias("lo"), c("v").MaxBy(c("by")).Alias("hi")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		get := func(name string) string {
			col, err := df.Column[string](name)
			if err != nil {
				t.Fatal(err)
			}
			if v, ok := col.Get(0); ok {
				return v
			}
			return "∅"
		}
		return pair{get("lo"), get("hi")}
	}
	floats := func(v []string, by []float64, ok []bool) *ursus.LazyFrame {
		if ok == nil {
			ok = make([]bool, len(by))
			for i := range ok {
				ok[i] = true
			}
		}
		return ursus.Frame(ursus.Values("v", v), ursus.ValuesNullable("by", by, ok))
	}
	for _, tc := range []struct {
		name  string
		frame *ursus.LazyFrame
		want  pair
	}{
		{"a tie goes to the earliest row", floats([]string{"a", "b", "c", "d"}, []float64{1, 1, 3, 3}, nil), pair{"a", "c"}},
		{"a null by is skipped", floats([]string{"a", "b", "c"}, []float64{0, 2, 1}, []bool{false, true, true}), pair{"c", "b"}},
		{"a null by last", floats([]string{"a", "b", "c"}, []float64{2, 1, 0}, []bool{true, true, false}), pair{"b", "a"}},
		{"every by null", floats([]string{"a", "b"}, []float64{0, 0}, []bool{false, false}), pair{"∅", "∅"}},
		{"a null value at the least", ursus.Frame(ursus.ValuesNullable("v", []string{"", "b", "c"}, []bool{false, true, true}),
			ursus.Values("by", []float64{0, 2, 1})), pair{"∅", "b"}},
		// Polars' max_by answers c here, skipping the NaN (MaxBy's doc).
		{"a NaN is the greatest", floats([]string{"a", "b", "c"}, []float64{1, nan, 3}, nil), pair{"a", "b"}},
		{"only NaNs", floats([]string{"a", "b"}, []float64{nan, nan}, nil), pair{"a", "a"}},
		{"by a string", ursus.Frame(ursus.Values("v", []string{"a", "b", "c"}), ursus.Values("by", []string{"y", "x", "z"})), pair{"b", "c"}},
		{"by an integer", ursus.Frame(ursus.Values("v", []string{"a", "b", "c"}), ursus.Values("by", []int64{5, 2, 9})), pair{"b", "c"}},
		{"by a Date", ursus.Frame(ursus.Values("v", []string{"a", "b", "c"}),
			ursus.Values("by", []time.Time{time.Unix(5*86400, 0), time.Unix(2*86400, 0), time.Unix(9*86400, 0)})).
			Select(c("v"), c("by").Cast(dtype.Date)), pair{"b", "c"}},
		{"by a Boolean", ursus.Frame(ursus.Values("v", []string{"a", "b", "c"}), ursus.Values("by", []bool{true, false, true})), pair{"b", "a"}},
		{"by a Decimal", ursus.Frame(ursus.Values("v", []string{"a", "b", "c"}), ursus.Values("by", []float64{1.5, -2.25, 9})).
			Select(c("v"), c("by").Cast(dtype.Decimal(10, 2))), pair{"b", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.frame); got != tc.want {
				t.Errorf("min_by, max_by %v, want %v", got, tc.want)
			}
		})
	}

	// Per group, the value keeping its type; and as a window.
	frame := ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2}), ursus.Values("v", []int64{10, 20, 30, 40}),
		ursus.ValuesNullable("by", []float64{2, 1, 0, 0}, []bool{true, true, false, false}))
	df, err := frame.GroupBy(c("g")).Agg(c("v").MinBy(c("by")).Alias("lo"), c("v").MaxBy(c("by").Mul(ursus.Lit(2.0))).Alias("hi")).
		Sort(ursus.Asc(c("g"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Schema().Field(1).Type != dtype.Int64 {
		t.Errorf("min_by of an Int64 is %s", df.Schema().Field(1).Type)
	}
	if lo, hi := rendered172(t, df, "lo"), rendered172(t, df, "hi"); lo != "20 ∅ " || hi != "10 ∅ " {
		t.Errorf("per group: lo %q, hi %q", lo, hi)
	}
	df, err = frame.Select(c("v").MinBy(c("by")).Over(c("g"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := rendered172(t, df, "v"); got != "20 20 ∅ ∅ " {
		t.Errorf("over g: %q", got)
	}
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
	if kk, _ := floats175(t, df, "kk"); kk[0] != 3 {
		t.Errorf("k at k's greatest: %v", kk[0])
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
	a, _ := floats175(t, df, "a")
	b, _ := floats175(t, df, "b")
	if strings.Join(df.Schema().Names(), ",") != "a,b" || a[0] != 2 || b[0] != 4 {
		t.Errorf("expanded: %v, a %v, b %v", df.Schema().Names(), a, b)
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
